package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
