package app

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

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
