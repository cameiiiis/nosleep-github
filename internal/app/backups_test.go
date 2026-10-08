package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreRejectsLookalikeTablesWithoutReplacingDatabase(t *testing.T) {
	project := t.TempDir()
	target, fake := filepath.Join(project, "runtime.db"), filepath.Join(project, "fake.db")
	s, err := OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	s.Seed()
	s.CreateUser("Kept", "kept@example.com", "hash")
	s.Close()
	db, err := sql.Open("sqlite", fake)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range historyTables {
		if _, err = db.Exec("CREATE TABLE " + table + "(id INTEGER PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if _, err = RestoreDatabase(target, fake); err == nil {
		t.Fatal("Accepted an incompatible lookalike database")
	}
	s, err = OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var name string
	if err = s.db.QueryRow("SELECT name FROM users").Scan(&name); err != nil || name != "Kept" {
		t.Fatalf("Original database changed: %s %v", name, err)
	}
}

func TestRestoreRejectsMissingAllocationTable(t *testing.T) {
	project := t.TempDir()
	target, backup := filepath.Join(project, "runtime.db"), filepath.Join(project, "incomplete.db")
	s, err := OpenStore(backup)
	if err != nil {
		t.Fatal(err)
	}
	s.Seed()
	if _, err = s.db.Exec("DROP TABLE booking_slots"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err = RestoreDatabase(target, backup); err == nil {
		t.Fatal("Missing allocation table accepted")
	}
	if _, err = os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("Invalid restore created its target")
	}
}

func TestConsistentBackupsRetainSevenAndDenyUnsafeDownloads(t *testing.T) {
	project := t.TempDir()
	dbPath := filepath.Join(project, "runtime.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Seed(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateUser("Saved player", "saved@example.com", "hash"); err != nil {
		t.Fatal(err)
	}
	manager := NewBackupManager(s, filepath.Join(project, "backups"))
	var latest BackupFile
	for i := 0; i < 9; i++ {
		latest, err = manager.Create()
		if err != nil {
			t.Fatal(err)
		}
	}
	files, status, err := manager.List()
	if err != nil || len(files) != 7 || status.LastBackup == nil {
		t.Fatalf("Backup retention/status: %d %+v %v", len(files), status, err)
	}
	for _, name := range []string{"../runtime.db", "..\\runtime.db", "C:\\secret.db", "unrelated.db"} {
		if _, err = manager.File(name); err == nil {
			t.Fatal("Unsafe backup download accepted", name)
		}
	}
	contents, err := manager.File(latest.Name)
	if err != nil || len(contents) == 0 {
		t.Fatalf("Backup unavailable: %v", err)
	}
	backup, err := openReadOnly(filepath.Join(project, "backups", latest.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var count int
	if err = backup.QueryRow("SELECT COUNT(*) FROM users WHERE name='Saved player'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("WAL record missing: %d %v", count, err)
	}
}

func TestRestorePreservesOldDatabaseAndRejectsInvalidBackup(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, "runtime.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Seed()
	s.CreateUser("Original", "original@example.com", "hash")
	manager := NewBackupManager(s, filepath.Join(project, "backups"))
	file, err := manager.Create()
	if err != nil {
		t.Fatal(err)
	}
	s.CreateUser("Later record", "later@example.com", "hash")
	s.Close()
	invalid := filepath.Join(project, "bad.db")
	os.WriteFile(invalid, []byte("not sqlite"), 0600)
	if _, err = RestoreDatabase(path, invalid); err == nil {
		t.Fatal("Invalid backup was accepted")
	}
	old, err := RestoreDatabase(path, filepath.Join(project, "backups", file.Name))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var count int
	restored.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count != 1 {
		t.Fatalf("Restored %d users", count)
	}
	original, err := OpenStore(old)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	original.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count != 2 {
		t.Fatal("Rollback DB lost original later records")
	}
}

func TestBackupStartCreatesInitialSnapshotAndStopsWithContext(t *testing.T) {
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Seed()
	m := NewBackupManager(s, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	_, before, _ := m.List()
	if _, err = m.Create(); err != nil {
		t.Fatal(err)
	}
	_, after, _ := m.List()
	if before.NextBackup == nil || after.NextBackup == nil || !before.NextBackup.Equal(*after.NextBackup) {
		t.Fatal("Manual backup changed the automatic schedule")
	}
	cancel()
	m.Wait()
	files, status, err := m.List()
	if err != nil || len(files) != 2 || status.NextBackup == nil || status.NextBackup.Before(time.Now()) {
		t.Fatalf("Startup backup/status: %+v %+v %v", files, status, err)
	}
}
