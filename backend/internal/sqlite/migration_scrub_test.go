package sqlite

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Job rows written before card 61 hold the registry password in env_overrides.
// Migration 9 must remove every copy of it from app.db and app.db-wal, not
// just hide it from queries.
func TestMigrationScrubsOldRegistryPasswords(t *testing.T) {
	const password = "card73-old-plaintext-password"
	oldEnv := `{"HAULER_REGISTRY_USERNAME":"alice","HAULER_REGISTRY_PASSWORD":"` + password + `"}`

	// An old Wagon at v8: one login row checkpointed into app.db, one still in
	// the WAL, and a row whose env_overrides must survive.
	oldDir := t.TempDir()
	oldDB, err := sql.Open("sqlite", "file:"+filepath.Join(oldDir, "app.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oldDB.Close() }()
	oldDB.SetMaxOpenConns(1)
	applyMigrationsUpTo(t, oldDB, 8)
	insert := func(env string) {
		t.Helper()
		if _, err := oldDB.Exec(`INSERT INTO jobs (command, args, env_overrides, status) VALUES ('hauler', '["login","registry.example.com"]', ?, 'succeeded')`, env); err != nil {
			t.Fatal(err)
		}
	}
	insert(oldEnv)
	if _, err := oldDB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	insert(oldEnv)
	insert(`{"KEEP":"me"}`)

	// Copy the files as an unclean stop leaves them, WAL included.
	dir := t.TempDir()
	for _, name := range []string{"app.db", "app.db-wal"} {
		b, err := os.ReadFile(filepath.Join(oldDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(b, []byte(password)) {
			t.Fatalf("seed: password not in %s, so the test proves nothing", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	db, err := Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoPassword := func(when string) {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte(password)) {
				t.Errorf("%s: password bytes still in %s", when, e.Name())
			}
		}
	}
	assertNoPassword("after migrations")

	var nulled, kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE env_overrides IS NULL`).Scan(&nulled); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE env_overrides = '{"KEEP":"me"}'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if nulled != 2 || kept != 1 {
		t.Errorf("nulled %d rows and kept %d, want 2 and 1", nulled, kept)
	}
	var secureDelete int
	if err := db.QueryRow(`PRAGMA secure_delete`).Scan(&secureDelete); err != nil {
		t.Fatal(err)
	}
	if secureDelete != 0 {
		t.Error("secure_delete left on after the migration")
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoPassword("after close")
}

// A failed scrub (VACUUM needs free disk) must not stop Wagon from starting,
// and the next start must run migration 9 again.
func TestMigrationScrubFailureRetriesNextStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	scrub := afterMigration[9]
	t.Cleanup(func() { afterMigration[9] = scrub })
	afterMigration[9] = func(*sql.DB) error { return errors.New("disk full") }

	countV9 := func() int {
		t.Helper()
		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = db.Close() }()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 9`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countV9(); n != 0 {
		t.Fatalf("migration 9 recorded after a failed scrub")
	}
	afterMigration[9] = scrub
	if n := countV9(); n != 1 {
		t.Fatalf("migration 9 not rerun on the next start")
	}
}
