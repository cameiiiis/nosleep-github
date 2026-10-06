package app

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

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
