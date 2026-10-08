package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type BackupFile struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`
}
type BackupStatus struct {
	Enabled    bool       `json:"enabled"`
	LastBackup *time.Time `json:"last_backup"`
	NextBackup *time.Time `json:"next_backup"`
	LastError  string     `json:"last_error"`
	Retained   int        `json:"retained"`
}
type BackupManager struct {
	store   *Store
	dir     string
	mu      sync.Mutex
	workers sync.WaitGroup
	status  BackupStatus
}

func NewBackupManager(store *Store, dir string) *BackupManager {
	return &BackupManager{store: store, dir: dir, status: BackupStatus{Enabled: true, Retained: 7}}
}
func (b *BackupManager) Start(ctx context.Context) {
	b.runAutomatic()
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.runAutomatic()
			}
		}
	}()
}

// Wait joins the automatic worker after its context has been cancelled.
func (b *BackupManager) Wait() { b.workers.Wait() }
func (b *BackupManager) runAutomatic() {
	b.mu.Lock()
	next := b.store.now().Add(time.Hour)
	b.status.NextBackup = &next
	b.mu.Unlock()
	_, _ = b.Create()
}
func (b *BackupManager) Create() (BackupFile, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.store.now()
	file, err := b.create(now)
	if err != nil {
		b.status.LastError = "Backup failed. Check storage permissions and free space."
		return file, err
	}
	b.status.LastError = ""
	b.status.LastBackup = &now
	return file, nil
}
func (b *BackupManager) create(now time.Time) (BackupFile, error) {
	if err := os.MkdirAll(b.dir, 0700); err != nil {
		return BackupFile{}, err
	}
	temporary, cleanup, err := sqliteSnapshot(b.store.db, b.dir)
	if err != nil {
		return BackupFile{}, err
	}
	defer cleanup()
	local := now.In(time.FixedZone("Philippine time", 8*3600))
	name := fmt.Sprintf("nosleep-%s-%d.db", local.Format("20060102-150405"), time.Now().UnixNano())
	target := filepath.Join(b.dir, name)
	if err = os.Link(temporary, target); err != nil {
		return BackupFile{}, err
	}
	files, err := backupFiles(b.dir)
	if err != nil {
		return BackupFile{}, err
	}
	for i := 7; i < len(files); i++ {
		if err = os.Remove(filepath.Join(b.dir, files[i].Name)); err != nil {
			return BackupFile{}, err
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return BackupFile{}, err
	}
	return BackupFile{Name: name, CreatedAt: info.ModTime(), Size: info.Size()}, nil
}
func backupFiles(dir string) ([]BackupFile, error) {
	out := []BackupFile{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !validBackupName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		out = append(out, BackupFile{Name: entry.Name(), CreatedAt: info.ModTime(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt) || out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].Name > out[j].Name
	})
	return out, nil
}
func validBackupName(name string) bool {
	return filepath.Base(name) == name && !strings.ContainsAny(name, "/\\:") && strings.HasPrefix(name, "nosleep-") && strings.HasSuffix(name, ".db")
}
func (b *BackupManager) List() ([]BackupFile, BackupStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	files, err := backupFiles(b.dir)
	return files, b.status, err
}
func (b *BackupManager) File(name string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !validBackupName(name) {
		return nil, ErrInvalid
	}
	path := filepath.Join(b.dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	return os.ReadFile(path)
}
func (a *api) registerBackupRoutes(mux *http.ServeMux) {
	authorized := func(r *http.Request) (User, error) {
		u, err := a.currentUser(r)
		if err != nil {
			return u, err
		}
		err = a.store.transact(func(tx *sql.Tx) error { return require(tx, u, "admin") })
		return u, err
	}
	mux.HandleFunc("GET /api/admin/backups", func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.currentUser(r); err != nil {
			a.unauthorized(w, err)
			return
		}
		if _, err := authorized(r); err != nil {
			a.fail(w, err)
			return
		}
		if a.store.Backups == nil {
			reply(w, 200, map[string]any{"backups": []BackupFile{}, "status": BackupStatus{Retained: 7}})
			return
		}
		files, status, err := a.store.Backups.List()
		if err != nil {
			a.fail(w, err)
			return
		}
		reply(w, 200, map[string]any{"backups": files, "status": status})
	})
	a.post(mux, "/api/admin/backups", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		if err := a.store.transact(func(tx *sql.Tx) error { return require(tx, u, "admin") }); err != nil {
			return nil, err
		}
		if a.store.Backups == nil {
			return nil, inputError("Automatic backups are available when the café server is running.")
		}
		return a.store.Backups.Create()
	})
	mux.HandleFunc("GET /api/admin/backups/file", func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.currentUser(r); err != nil {
			a.unauthorized(w, err)
			return
		}
		if _, err := authorized(r); err != nil {
			a.fail(w, err)
			return
		}
		if a.store.Backups == nil {
			a.fail(w, ErrNotFound)
			return
		}
		name := r.URL.Query().Get("name")
		contents, err := a.store.Backups.File(name)
		if err != nil {
			a.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.sqlite3")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		w.Write(contents)
	})
}

// RestoreDatabase runs before OpenStore and while main owns the server port.
// It validates the backup and retains the old DB and companion files so a
// failure or an incorrect backup selection is recoverable.
func RestoreDatabase(target, source string) (string, error) {
	target, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(target, source) {
		return "", ErrInvalid
	}
	db, err := openReadOnly(source)
	if err != nil {
		return "", err
	}
	var check string
	if err = db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		db.Close()
		return "", inputError("The selected backup is not a valid SQLite database.")
	}
	for _, table := range historyTables {
		var n int
		if err = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			db.Close()
			return "", inputError("The selected backup is missing NO SLEEP tables.")
		}
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		db.Close()
		return "", err
	}
	invalid := rows.Next()
	queryErr := rows.Err()
	rows.Close()
	if queryErr != nil {
		db.Close()
		return "", queryErr
	}
	if invalid {
		db.Close()
		return "", inputError("The backup contains invalid record links.")
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		db.Close()
		return "", err
	}
	temporary, cleanup, err := sqliteSnapshot(db, filepath.Dir(target))
	db.Close()
	if err != nil {
		return "", err
	}
	defer cleanup()
	if err = validateRestoredSchema(temporary); err != nil {
		return "", err
	}
	rollback := target + ".before-restore-" + time.Now().UTC().Format("20060102-150405.000000000") + ".db"
	moved := []string{}
	// Check each exact path before renaming; no recursive operations.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		oldPath := target + suffix
		if _, e := os.Stat(oldPath); errors.Is(e, os.ErrNotExist) {
			continue
		} else if e != nil {
			err = e
			break
		}
		if err = os.Rename(oldPath, rollback+suffix); err != nil {
			break
		}
		moved = append(moved, suffix)
	}
	if err == nil {
		err = os.Rename(temporary, target)
	}
	if err != nil {
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.Rename(rollback+moved[i], target+moved[i])
		}
		return "", err
	}
	if len(moved) == 0 {
		return "", nil
	}
	return rollback, nil
}

// Validate and migrate only the temporary snapshot before touching live files.
func validateRestoredSchema(path string) error {
	restored, err := OpenStore(path)
	if err != nil {
		return inputError("The backup has an incompatible NO SLEEP schema.")
	}
	defer restored.Close()
	reference, err := OpenStore(":memory:")
	if err != nil {
		return err
	}
	defer reference.Close()
	tables, err := databaseTables(reference.db)
	if err != nil {
		return err
	}
	for _, table := range tables {
		expected, err := databaseColumns(reference.db, table)
		if err != nil {
			return err
		}
		actual, err := databaseColumns(restored.db, table)
		if err != nil {
			return err
		}
		for name, shape := range expected {
			if actual[name] != shape {
				return inputError("The backup has incompatible NO SLEEP table columns.")
			}
		}
	}
	return nil
}
func databaseColumns(db *sql.DB, table string) (map[string]string, error) {
	rows, err := db.Query("PRAGMA table_info(" + quoteIdentifier(table) + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, required, key int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&id, &name, &kind, &required, &defaultValue, &key); err != nil {
			return nil, err
		}
		out[name] = fmt.Sprintf("%s/%d/%d", strings.ToUpper(kind), required, key)
	}
	return out, rows.Err()
}
