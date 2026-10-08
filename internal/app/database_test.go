package app

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- runtime test ---

func TestRuntimeDatabaseCopiesLiveWALWithoutChangingStarter(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "starter.db")
	target := filepath.Join(dir, "runtime.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE marker(value TEXT); INSERT INTO marker VALUES('live WAL record')"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	walBefore, err := os.ReadFile(source + "-wal")
	if err != nil || len(walBefore) == 0 {
		t.Fatalf("Need live WAL: %v", err)
	}
	if err = PrepareRuntimeDatabase(target, source); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(source)
	walAfter, _ := os.ReadFile(source + "-wal")
	if !bytes.Equal(before, after) || !bytes.Equal(walBefore, walAfter) {
		t.Fatal("Copy mutated starter or WAL")
	}
	copyDB, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var value string
	if err = copyDB.QueryRow("SELECT value FROM marker").Scan(&value); err != nil || value != "live WAL record" {
		t.Fatalf("WAL record lost: %q %v", value, err)
	}
	if _, err = copyDB.Exec("INSERT INTO marker VALUES('runtime only')"); err != nil {
		t.Fatal(err)
	}
	if err = PrepareRuntimeDatabase(target, source); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = copyDB.QueryRow("SELECT count(*) FROM marker").Scan(&count); err != nil || count != 2 {
		t.Fatalf("Existing runtime overwritten: %d %v", count, err)
	}
	var tables int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("Source schema was initialized: %d %v", tables, err)
	}
}

func TestMissingStarterAllowsNewRuntimeDatabase(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "runtime.db")
	source := filepath.Join(dir, "missing.db")
	if err := PrepareRuntimeDatabase(target, source); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if _, err = os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("Missing starter was created: %v", err)
	}
}

// --- local config test ---

func TestDatabaseSelectionPersistsAcrossRestartAndPortableFolder(t *testing.T) {
	project := t.TempDir()
	selected, _, err := ResolveDatabase(project, "data/player-records.db")
	if err != nil {
		t.Fatal(err)
	}
	if selected != filepath.Join(project, "data", "player-records.db") {
		t.Fatal(selected)
	}
	again, _, err := ResolveDatabase(project, "")
	if err != nil || again != selected {
		t.Fatalf("Restart switched DB: %s %v", again, err)
	}
	bytes, err := os.ReadFile(filepath.Join(project, "data", "local-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), project) {
		t.Fatal("Project-local DB selection must remain portable")
	}
	different, _, err := ResolveDatabase(project, "data/another.db")
	if err != nil {
		t.Fatal(err)
	}
	again, _, err = ResolveDatabase(project, "")
	if err != nil || again != different {
		t.Fatal("Explicit DB change was not persisted")
	}
}

func TestFirstSelectionRecognizesStrictlyNewerLegacyAndNeverMergesDivergentFiles(t *testing.T) {
	for _, divergent := range []bool{false, true} {
		t.Run(map[bool]string{false: "newer", true: "divergent"}[divergent], func(t *testing.T) {
			project := t.TempDir()
			data := filepath.Join(project, "data")
			if err := os.MkdirAll(data, 0700); err != nil {
				t.Fatal(err)
			}
			starter, runtime := filepath.Join(data, "nosleep.db"), filepath.Join(data, "runtime.db")
			s, err := OpenStore(starter)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Seed(); err != nil {
				t.Fatal(err)
			}
			if _, err = s.CreateUser("Original", "original@example.com", "hash"); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			if err = PrepareRuntimeDatabase(runtime, starter); err != nil {
				t.Fatal(err)
			}
			s, err = OpenStore(starter)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.CreateUser("New player", "new@example.com", "hash"); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if divergent {
				s, err = OpenStore(runtime)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.Exec("UPDATE users SET name='Runtime player' WHERE id=1"); err != nil {
					t.Fatal(err)
				}
				s.Close()
			}
			selected, note, err := ResolveDatabase(project, "")
			if err != nil {
				t.Fatal(err)
			}
			want := starter
			if divergent {
				want = runtime
			}
			if selected != want {
				t.Fatalf("Selected %s, want %s (%s)", selected, want, note)
			}
			if note == "" {
				t.Fatal("Explain initial DB selection")
			}
			old, err := OpenStore(runtime)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			var count int
			old.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
			if count != 1 {
				t.Fatal("Original runtime must not be overwritten")
			}
		})
	}
}

func TestInvalidSavedDatabaseConfigDoesNotSilentlyUseAnotherDatabase(t *testing.T) {
	project := t.TempDir()
	os.MkdirAll(filepath.Join(project, "data"), 0700)
	os.WriteFile(filepath.Join(project, "data", "local-config.json"), []byte("{broken"), 0600)
	if _, _, err := ResolveDatabase(project, ""); err == nil {
		t.Fatal("Malformed config must not silently change the database")
	}
}

func TestFirstSelectionPreservesRuntimeOnlyOperationalRecords(t *testing.T) {
	project := t.TempDir()
	data := filepath.Join(project, "data")
	os.MkdirAll(data, 0700)
	starter, runtime := filepath.Join(data, "nosleep.db"), filepath.Join(data, "runtime.db")
	s, err := OpenStore(starter)
	if err != nil {
		t.Fatal(err)
	}
	s.Seed()
	s.CreateUser("Original", "original@example.com", "hash")
	s.Close()
	if err = PrepareRuntimeDatabase(runtime, starter); err != nil {
		t.Fatal(err)
	}
	s, _ = OpenStore(starter)
	s.CreateUser("New player", "new@example.com", "hash")
	s.Close()
	s, _ = OpenStore(runtime)
	if _, err = s.db.Exec("INSERT INTO audit(actor_id,action,entity_id,created_at) VALUES(1,'shift.closed',1,1)"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	selected, _, err := ResolveDatabase(project, "")
	if err != nil || selected != runtime {
		t.Fatalf("Hidden runtime operational history: %s %v", selected, err)
	}
}

// --- peso migration test ---

func TestExistingDatabaseConvertsToPesosOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile("testdata/legacy.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(legacy)); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
INSERT INTO users VALUES(1,'Admin','admin@example.com','hash','admin',1,1),(2,'Player','player@example.com','hash','customer',1,1),(3,'Cashier','cashier@example.com','hash','cashier',1,1);
INSERT INTO auth_sessions VALUES('existing-session',2,9999999999);
INSERT INTO tiers VALUES(1,'Standard',3055);
INSERT INTO pcs VALUES(1,'PC-01',1,'Test PC',1);
INSERT INTO bookings VALUES(1,'NS-000001',2,1,2000000000,2000007200,'confirmed',2000000000,'original-booking',1);
INSERT INTO booking_items VALUES(1,1,'base',4,3055,12220,'paid',2000000000,2000000000,2000007200,NULL);
INSERT INTO booking_slots VALUES(1,2000000000,1);
INSERT INTO payments VALUES(1,1,1,12220,'cash','Original payment',3,'original-payment',1);
INSERT INTO receipts VALUES(1,'RC-000001',1,1,'Original receipt',12220,1);
INSERT INTO refunds VALUES(1,1,1,12220,'Test refund','completed',2,1,3,1,2);
INSERT INTO audit VALUES(1,3,'payment.recorded',1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		s, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range []struct{ query, want string }{
			{"SELECT price_pesos FROM tiers WHERE id=1", "30.55"},
			{"SELECT unit_pesos FROM booking_items WHERE id=1", "30.55"},
			{"SELECT amount_pesos FROM booking_items WHERE id=1", "122.20"},
			{"SELECT amount_pesos FROM payments WHERE id=1", "122.20"},
			{"SELECT amount_pesos FROM receipts WHERE id=1", "122.20"},
			{"SELECT amount_pesos FROM refunds WHERE id=1", "122.20"},
			{"SELECT reference FROM payments WHERE id=1", "Original payment"},
			{"SELECT description FROM receipts WHERE id=1", "Original receipt"},
			{"SELECT token_hash FROM auth_sessions", "existing-session"},
		} {
			var got string
			if err := s.db.QueryRow(check.query).Scan(&got); err != nil {
				s.Close()
				t.Fatal(err)
			}
			if got != check.want {
				s.Close()
				t.Fatalf("pass %d: %s returned %q, want %q", pass, check.query, got, check.want)
			}
		}
		var enabled int
		if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
			s.Close()
			t.Fatalf("Foreign keys disabled: %d, %v", enabled, err)
		}
		rows, err := s.db.Query("PRAGMA foreign_key_check")
		if err != nil {
			s.Close()
			t.Fatal(err)
		}
		broken := rows.Next()
		rowErr := rows.Err()
		rows.Close()
		if broken || rowErr != nil {
			s.Close()
			t.Fatalf("Broken references after migration: %v", rowErr)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
