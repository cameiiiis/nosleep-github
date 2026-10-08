package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

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
