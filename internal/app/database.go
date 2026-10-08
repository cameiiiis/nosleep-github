package app

// SQLite setup, default data, transaction helpers, starter copy and saved database choice.

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// --- SQLite setup, defaults and transaction helpers ---

var (
	ErrForbidden = errors.New("forbidden")
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("conflict")
	ErrInvalid   = errors.New("invalid input")
)

//go:embed schema.sql
var schema embed.FS

type Store struct {
	db      *sql.DB
	Now     func() time.Time
	Backups *BackupManager
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// ponytail: serialize this local app's SQLite transactions; use a server DB for multi-process scale.
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}
	sqlText, err := schema.ReadFile("schema.sql")
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(string(sqlText)); err != nil {
		db.Close()
		return nil, err
	}
	if err = migratePesos(db, string(sqlText)); err != nil {
		db.Close()
		return nil, err
	}
	var hasPublicReason bool
	if err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_table_info('maintenance') WHERE name='public_reason')").Scan(&hasPublicReason); err == nil && !hasPublicReason {
		_, err = db.Exec("ALTER TABLE maintenance ADD COLUMN public_reason TEXT NOT NULL DEFAULT ''")
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = initAccountSchema(db); err == nil {
		err = initCafeSchema(db)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, Now: time.Now}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}
func stamp(t time.Time) int64 { return t.UTC().Unix() }
func at(n int64) time.Time    { return time.Unix(n, 0).UTC() }
func nullableAt(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := at(n.Int64)
	return &t
}
func (s *Store) transact(fn func(*sql.Tx) error) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func audit(tx *sql.Tx, actor int64, action string, id int64, now time.Time) error {
	_, e := tx.Exec("INSERT INTO audit(actor_id,action,entity_id,created_at) VALUES(?,?,?,?)", actor, action, id, stamp(now))
	return e
}
func role(tx *sql.Tx, u User) (string, error) {
	var r string
	var active bool
	e := tx.QueryRow("SELECT role,active FROM users WHERE id=?", u.ID).Scan(&r, &active)
	if errors.Is(e, sql.ErrNoRows) {
		return "", ErrForbidden
	}
	if e != nil {
		return "", e
	}
	if !active {
		return "", ErrForbidden
	}
	return r, nil
}
func require(tx *sql.Tx, u User, roles ...string) error {
	r, e := role(tx, u)
	if e != nil {
		return e
	}
	for _, want := range roles {
		if r == want {
			return nil
		}
	}
	return ErrForbidden
}

// A walk-in's user_id records its staff creator, never the guest's account owner.
func bookingAccess(tx *sql.Tx, actor User, bookingID int64) error {
	r, err := role(tx, actor)
	if err != nil || r != "customer" {
		return err
	}
	var owns bool
	if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM bookings b WHERE b.id=? AND b.user_id=? AND NOT EXISTS(SELECT 1 FROM walk_ins w WHERE w.booking_id=b.id))", bookingID, actor.ID).Scan(&owns); err != nil {
		return err
	}
	if !owns {
		return ErrForbidden
	}
	return nil
}
func (s *Store) Seed() error {
	return s.transact(func(tx *sql.Tx) error {
		for _, seed := range []struct {
			id          int64
			name, price string
		}{{1, "Standard", "30.00"}, {2, "Pro", "45.00"}, {3, "Elite", "60.00"}} {
			price, err := ParsePesos(seed.price)
			if err != nil {
				return err
			}
			t := Tier{seed.id, seed.name, price}
			if _, e := tx.Exec("INSERT OR IGNORE INTO tiers(id,name,price_pesos) VALUES(?,?,?)", t.ID, t.Name, t.PricePesos); e != nil {
				return e
			}
		}
		specs := []string{
			"CPU: Intel Core i5-12400F · RAM: 16 GB · GPU: RTX 4060",
			"CPU: Intel Core i7-12700F · RAM: 32 GB · GPU: RTX 4070",
			"CPU: Intel Core i9-13900F · RAM: 32 GB · GPU: RTX 4080",
		}
		legacySpecs := []string{"RTX 4060 · 16 GB", "RTX 4070 · 32 GB", "RTX 4080 · 32 GB"}
		for i := 1; i <= 12; i++ {
			tier := int64((i-1)/4 + 1)
			code := fmt.Sprintf("PC-%02d", i)
			if _, e := tx.Exec("INSERT OR IGNORE INTO pcs(id,code,tier_id,specs,enabled) VALUES(?,?,?,?,1)", i, code, tier, specs[tier-1]); e != nil {
				return e
			}
			if _, e := tx.Exec("UPDATE pcs SET specs=? WHERE id=? AND code=? AND tier_id=? AND specs=?", specs[tier-1], i, code, tier, legacySpecs[tier-1]); e != nil {
				return e
			}
		}
		return nil
	})
}
func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var n int64
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &n)
	u.CreatedAt = at(n)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

const userCols = "id,name,email,password_hash,role,active,created_at"

func (s *Store) CreateUser(name, email, passwordHash string) (User, error) {
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	if name == "" || email == "" || passwordHash == "" || !strings.Contains(email, "@") {
		return User{}, ErrConflict
	}
	now := s.now()
	r, e := s.db.Exec("INSERT INTO users(name,email,password_hash,role,active,created_at) VALUES(?,?,?,'customer',1,?)", name, email, passwordHash, stamp(now))
	if e != nil {
		return User{}, ErrConflict
	}
	id, _ := r.LastInsertId()
	return s.UserByID(id)
}
func (s *Store) UserByID(id int64) (User, error) {
	return scanUser(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", id))
}
func (s *Store) UserByEmail(email string) (User, error) {
	return scanUser(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE email=?", strings.ToLower(strings.TrimSpace(email))))
}
func (s *Store) SeedDemoUsers(adminHash, cashierHash, playerHash string) error {
	return s.transact(func(tx *sql.Tx) error {
		for _, v := range []struct{ name, email, hash, role string }{{"Demo Admin", "admin@nosleep.local", adminHash, "admin"}, {"Demo Cashier", "cashier@nosleep.local", cashierHash, "cashier"}, {"Demo Player", "player@nosleep.local", playerHash, "customer"}} {
			if v.hash == "" {
				return ErrConflict
			}
			var existingID int64
			e := tx.QueryRow("SELECT id FROM users WHERE email=?", v.email).Scan(&existingID)
			if e == nil {
				// Seeding creates missing accounts; existing account decisions survive restart.
				continue
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			_, e = tx.Exec("INSERT INTO users(name,email,password_hash,role,active,created_at) VALUES(?,?,?,?,1,?)", v.name, v.email, v.hash, v.role, stamp(s.now()))
			if e != nil {
				return e
			}
		}
		return nil
	})
}
func (s *Store) SaveAuth(tokenHash string, userID int64, expires time.Time) error {
	if tokenHash == "" {
		return ErrConflict
	}
	_, e := s.db.Exec("INSERT INTO auth_sessions(token_hash,user_id,expires_at) VALUES(?,?,?) ON CONFLICT(token_hash) DO UPDATE SET user_id=excluded.user_id,expires_at=excluded.expires_at", tokenHash, userID, stamp(expires))
	return e
}
func (s *Store) AuthUser(tokenHash string) (User, error) {
	u, e := scanUser(s.db.QueryRow("SELECT users.id,users.name,users.email,users.password_hash,users.role,users.active,users.created_at FROM users JOIN auth_sessions ON users.id=auth_sessions.user_id WHERE token_hash=? AND expires_at>? AND users.active=1", tokenHash, stamp(s.now())))
	return u, e
}
func (s *Store) DeleteAuth(tokenHash string) error {
	_, e := s.db.Exec("DELETE FROM auth_sessions WHERE token_hash=?", tokenHash)
	return e
}

func expire(tx *sql.Tx, now time.Time) error {
	n := stamp(now)
	// A paid interval ending is not proof that its guest has left the station.
	// Staff EndPlay closes physical occupancy, including sessions in overtime.
	// A quote cannot outlive the interval in which payment is permitted.
	if _, e := tx.Exec("UPDATE booking_items SET hold_until=MIN(hold_until,expected_end) WHERE status='held'"); e != nil {
		return e
	}
	if _, e := tx.Exec("UPDATE bookings SET hold_until=MIN(hold_until,start_at) WHERE status='held'"); e != nil {
		return e
	}
	if _, e := tx.Exec("DELETE FROM booking_slots WHERE item_id IN (SELECT id FROM booking_items WHERE status='held' AND hold_until<=?)", n); e != nil {
		return e
	}
	if _, e := tx.Exec("UPDATE booking_items SET status='expired' WHERE status='held' AND hold_until<=?", n); e != nil {
		return e
	}
	if _, e := tx.Exec("UPDATE bookings SET status='expired' WHERE status='held' AND hold_until<=?", n); e != nil {
		return e
	}
	if _, e := tx.Exec("UPDATE bookings SET status='no_show' WHERE status='confirmed' AND end_at<=? AND id NOT IN (SELECT booking_id FROM plays)", n); e != nil {
		return e
	}
	if _, e := tx.Exec("DELETE FROM booking_slots WHERE item_id IN (SELECT i.id FROM booking_items i JOIN bookings b ON b.id=i.booking_id WHERE b.status IN ('no_show','completed','cancelled','expired'))"); e != nil {
		return e
	}
	return nil
}
func scanPC(row interface{ Scan(...any) error }) (PC, error) {
	var p PC
	e := row.Scan(&p.ID, &p.Code, &p.TierID, &p.Specs, &p.Enabled)
	return p, e
}
func scanBooking(row interface{ Scan(...any) error }) (Booking, error) {
	var b Booking
	var start, end, hold, created int64
	e := row.Scan(&b.ID, &b.Reference, &b.UserID, &b.PCID, &start, &end, &b.Status, &hold, &created)
	b.StartAt = at(start)
	b.EndAt = at(end)
	b.HoldUntil = at(hold)
	b.CreatedAt = at(created)
	return b, e
}
func scanItem(row interface{ Scan(...any) error }) (BookingItem, error) {
	var i BookingItem
	var hold, expected, proposed int64
	e := row.Scan(&i.ID, &i.BookingID, &i.Kind, &i.Slots, &i.UnitPesos, &i.AmountPesos, &i.Status, &hold, &expected, &proposed)
	i.HoldUntil = at(hold)
	i.ExpectedEnd = at(expected)
	i.ProposedEnd = at(proposed)
	return i, e
}
func scanReceipt(row interface{ Scan(...any) error }) (Receipt, error) {
	var r Receipt
	var n int64
	e := row.Scan(&r.ID, &r.Number, &r.PaymentID, &r.BookingID, &r.Description, &r.AmountPesos, &n)
	r.CreatedAt = at(n)
	return r, e
}

const bookingCols = "id,reference,user_id,pc_id,start_at,end_at,status,hold_until,created_at"
const itemCols = "id,booking_id,kind,slots,unit_pesos,amount_pesos,status,hold_until,expected_end,proposed_end"
const receiptCols = "id,number,payment_id,booking_id,description,amount_pesos,created_at"

func (s *Store) Available(start, end time.Time) ([]PC, error) {
	out := []PC{}
	if !validInterval(start, end) {
		return out, ErrConflict
	}
	e := s.transact(func(tx *sql.Tx) error {
		if e := expire(tx, s.now()); e != nil {
			return e
		}
		rows, e := tx.Query("SELECT id,code,tier_id,specs,enabled FROM pcs p WHERE enabled=1 AND NOT EXISTS (SELECT 1 FROM maintenance m WHERE m.pc_id=p.id AND m.start_at<? AND m.end_at>?) AND NOT EXISTS (SELECT 1 FROM booking_slots bs WHERE bs.pc_id=p.id AND bs.start_at+1800>? AND bs.start_at<?) ORDER BY p.id", stamp(end), stamp(start), stamp(start), stamp(end))
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			p, e := scanPC(rows)
			if e != nil {
				return e
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, e
}
func validInterval(start, end time.Time) bool {
	return !start.IsZero() && !end.IsZero() && end.After(start)
}

// --- First-run database copy and consistent snapshots ---

// PrepareRuntimeDatabase preserves the shipped starter and snapshots committed
// WAL records through SQLite before the runtime schema or seeds are opened.
func PrepareRuntimeDatabase(runtimePath, starterPath string) error {
	if _, err := os.Stat(runtimePath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0700); err != nil {
		return err
	}
	if _, err := os.Stat(starterPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	absolute, err := filepath.Abs(starterPath)
	if err != nil {
		return err
	}
	sourceURL := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(absolute), "/"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", sourceURL.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	temporary, cleanup, err := sqliteSnapshot(db, filepath.Dir(runtimePath))
	if err != nil {
		return err
	}
	defer cleanup()
	// A hard link installs the complete snapshot atomically and never replaces
	// another process's already-created runtime database.
	err = os.Link(temporary, runtimePath)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

func sqliteSnapshot(db *sql.DB, parent string) (string, func(), error) {
	dir, err := os.MkdirTemp(parent, "nosleep-snapshot-")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "database.sqlite")
	cleanup := func() { _ = os.Remove(path); _ = os.Remove(dir) }
	if _, err = db.Exec("VACUUM INTO ?", path); err != nil {
		cleanup()
		return "", nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// --- Remember the active database between terminal restarts ---

type LocalConfig struct {
	Database string `json:"database"`
}

// ResolveDatabase remembers an explicitly selected database across terminal
// restarts. A folder shared with a teammate keeps portable relative paths.
func ResolveDatabase(project, override string) (string, string, error) {
	project, err := filepath.Abs(project)
	if err != nil {
		return "", "", err
	}
	configPath := filepath.Join(project, "data", "local-config.json")
	var config LocalConfig
	contents, err := os.ReadFile(configPath)
	if err == nil {
		if err = json.Unmarshal(contents, &config); err != nil || strings.TrimSpace(config.Database) == "" {
			return "", "", fmt.Errorf("invalid local database configuration: %s", configPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	selected, note := strings.TrimSpace(override), ""
	if selected == "" {
		selected = config.Database
	}
	if selected == "" {
		selected = filepath.Join("data", "runtime.db")
		runtime, starter := filepath.Join(project, selected), filepath.Join(project, "data", "nosleep.db")
		if exists(runtime) && exists(starter) {
			newer, err := databaseSuperset(runtime, starter)
			if err != nil {
				return "", "", err
			}
			if newer {
				selected = filepath.Join("data", "nosleep.db")
				note = "Using the newer legacy database; this choice is now saved locally."
			} else {
				identical, err := sameDatabaseHistory(runtime, starter)
				if err != nil {
					return "", "", err
				}
				if !identical {
					note = "The starter and runtime have different records. Keeping the runtime; set NO_SLEEP_DB explicitly if you intended another file. No records were overwritten."
				}
			}
		}
	}
	if !filepath.IsAbs(selected) {
		selected = filepath.Join(project, selected)
	}
	selected, err = filepath.Abs(selected)
	if err != nil {
		return "", "", err
	}
	portable := selected
	if rel, e := filepath.Rel(project, selected); e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		portable = rel
	}
	if config.Database != portable {
		if err = os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
			return "", "", err
		}
		bytes, _ := json.MarshalIndent(LocalConfig{Database: portable}, "", "  ")
		if err = saveLocalConfig(configPath, append(bytes, '\n')); err != nil {
			return "", "", err
		}
	}
	return selected, note, nil
}

func saveLocalConfig(path string, contents []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".local-config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(contents); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func openReadOnly(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(absolute), "/"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// These original tables must exist in a restore source before any migration.
var historyTables = []string{"users", "auth_sessions", "tiers", "pcs", "maintenance", "bookings", "walk_ins", "booking_items", "booking_slots", "payments", "receipts", "plays", "refunds", "audit"}

func databaseRows(path string) (map[string]map[string]string, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string]map[string]string{}
	tables, err := databaseTables(db)
	if err != nil {
		return nil, err
	}
	for _, table := range tables {
		rows, err := db.Query("SELECT * FROM " + quoteIdentifier(table))
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		out[table] = map[string]string{}
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, err
			}
			record := map[string]any{}
			for i, column := range columns {
				record[column] = values[i]
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				rows.Close()
				return nil, err
			}
			// Full-row keys preserve composite keys and duplicate rows in custom tables.
			key := string(encoded)
			out[table][key] += "x"
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func quoteIdentifier(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
func databaseTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// Prove the newer file contains every old business record unchanged. Never
// infer this from file size, mtime, or higher IDs alone.
func databaseSuperset(older, newer string) (bool, error) {
	old, err := databaseRows(older)
	if err != nil {
		return false, err
	}
	latest, err := databaseRows(newer)
	if err != nil {
		return false, err
	}
	additional := false
	for table, records := range old {
		for id, row := range records {
			if latest[table][id] != row {
				return false, nil
			}
		}
		if len(latest[table]) > len(records) {
			additional = true
		}
	}
	return additional, nil
}
func sameDatabaseHistory(a, b string) (bool, error) {
	first, err := databaseRows(a)
	if err != nil {
		return false, err
	}
	second, err := databaseRows(b)
	if err != nil {
		return false, err
	}
	one, _ := json.Marshal(first)
	two, _ := json.Marshal(second)
	return string(one) == string(two), nil
}
