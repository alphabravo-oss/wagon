package sqlite

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"sort"
	"testing"

	_ "modernc.org/sqlite"
)

// openRawDB opens a bare sql.DB on a temp file using the same modernc "sqlite"
// driver and DSN pragmas as Open, but WITHOUT running applyMigrations. This lets
// a test drive the migration chain up to an arbitrary point, populate stable
// tables, and then finish the chain — reproducing a real in-place UPGRADE.
func openRawDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "upgrade.db")
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("opening raw database: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("pinging raw database: %v", err)
	}
	// SQLite is single-writer; mirror Open's connection settings so migrations
	// that use multiple statements behave identically to production.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	return db
}

// applyMigrationsUpTo mirrors applyMigrations' logic (create schema_migrations,
// read + version-sort the embedded migration files via parseVersion, exec each
// file inside its own tx, and record the version) but only applies migrations
// whose version is <= maxVersion and not already recorded. Calling it twice with
// an increasing maxVersion performs a staged upgrade, exactly like a user who
// installed an older build and later upgraded.
func applyMigrationsUpTo(t *testing.T, db *sql.DB, maxVersion int) {
	t.Helper()

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		t.Fatalf("creating migrations table: %v", err)
	}

	rows, err := db.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("querying applied migrations: %v", err)
	}
	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			if err := rows.Close(); err != nil {
				t.Errorf("close rows: %v", err)
			}
			t.Fatalf("scanning migration version: %v", err)
		}
		applied[version] = true
	}
	if err := rows.Close(); err != nil {
		t.Errorf("close rows: %v", err)
	}

	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("getting migrations sub-filesystem: %v", err)
	}
	migrationFiles, err := fs.Glob(subFS, "*.sql")
	if err != nil {
		t.Fatalf("globbing migration files: %v", err)
	}
	sort.Slice(migrationFiles, func(i, j int) bool {
		return parseVersion(migrationFiles[i]) < parseVersion(migrationFiles[j])
	})

	for _, file := range migrationFiles {
		version := parseVersion(file)
		if version > maxVersion || applied[version] {
			continue
		}
		content, err := fs.ReadFile(subFS, file)
		if err != nil {
			t.Fatalf("reading migration %s: %v", file, err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("beginning tx for migration %d: %v", version, err)
		}
		if _, err := tx.Exec(string(content)); err != nil {
			if err := tx.Rollback(); err != nil {
				t.Errorf("rollback transaction: %v", err)
			}
			t.Fatalf("executing migration %d: %v", version, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version); err != nil {
			if err := tx.Rollback(); err != nil {
				t.Errorf("rollback transaction: %v", err)
			}
			t.Fatalf("recording migration %d: %v", version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("committing migration %d: %v", version, err)
		}
	}
}

// TestMigrationUpgradeIsNonDestructive is the core non-destructive guarantee for
// in-place upgrades. It populates STABLE tables that exist at v4 and are not
// meant to be dropped by later migrations, then finishes the migration chain to
// head and asserts the pre-existing rows SURVIVE.
//
// CONTRACT FOR FUTURE MIGRATIONS: every migration added after 0007 MUST keep
// this test green. That means it must NOT drop, truncate, or otherwise damage
// populated stable tables (settings, jobs, sessions, and going forward, hauls,
// serve_processes, etc.). If you need to reshape such a table, migrate the data
// forward (e.g. add column, or create-new + copy + swap) rather than DROP.
//
// DELIBERATE EXCEPTION: store_contents (recreated by 0005) and saved_manifests
// (recreated by 0006) were intentionally DROPPED-and-recreated during the alpha
// period to add a haul_id column. Rows in those two tables are knowingly reset
// on upgrade, so this test does NOT assert they survive. That one-time alpha
// window is closed: no migration after 0007 may add a new destructive drop of a
// populated stable table.
func TestMigrationUpgradeIsNonDestructive(t *testing.T) {
	db := openRawDB(t)

	// 1. Bring the DB up to v4 — the state a user on an older build would have,
	//    BEFORE the destructive alpha migrations 0005/0006.
	applyMigrationsUpTo(t, db, 4)

	// 2. Populate representative rows in STABLE tables that already exist at v4
	//    and are NOT dropped by any later migration.

	// settings: a custom (non-default) key.
	if _, err := db.Exec(
		"INSERT INTO settings (key, value, description) VALUES (?, ?, ?)",
		"custom_survivor", "keep-me", "row that must survive the upgrade",
	); err != nil {
		t.Fatalf("inserting settings row: %v", err)
	}

	// jobs: a job row. (jobs is ALTERed by 0002 and 0005 but never dropped.)
	jobRes, err := db.Exec(
		"INSERT INTO jobs (command, args, status) VALUES (?, ?, ?)",
		"hauler", `["store","sync"]`, "succeeded",
	)
	if err != nil {
		t.Fatalf("inserting jobs row: %v", err)
	}
	jobID, err := jobRes.LastInsertId()
	if err != nil {
		t.Fatalf("getting job id: %v", err)
	}

	// sessions: an auth token. (sessions is created by 0003 and never dropped.)
	const survivingToken = "survivor-token-abc123"
	if _, err := db.Exec(
		"INSERT INTO sessions (token, expires_at) VALUES (?, datetime('now', '+24 hours'))",
		survivingToken,
	); err != nil {
		t.Fatalf("inserting sessions row: %v", err)
	}

	// serve_processes: a process row. (Exists since 0001; 0007 ADDs role/hostname
	// columns but never drops the table. This is real operational state we must
	// not lose on upgrade.)
	serveRes, err := db.Exec(
		"INSERT INTO serve_processes (serve_type, port, status) VALUES (?, ?, ?)",
		"registry", 5000, "stopped",
	)
	if err != nil {
		t.Fatalf("inserting serve_processes row: %v", err)
	}
	serveID, err := serveRes.LastInsertId()
	if err != nil {
		t.Fatalf("getting serve_processes id: %v", err)
	}

	// 3. Apply the REMAINING migrations (5, 6, 7) to head — the upgrade step.
	applyMigrationsUpTo(t, db, 1<<30)

	// Sanity: we advanced through at least every migration known today. Using >=
	// (not ==) keeps this green when a new migration is added later; the survival
	// assertions below — not a hardcoded count — are what enforce non-destructiveness.
	var migCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migCount); err != nil {
		t.Fatalf("counting migrations: %v", err)
	}
	if migCount < 7 {
		t.Fatalf("expected at least 7 migrations after upgrade, got %d", migCount)
	}

	// 4. Assert the rows inserted at step 2 STILL EXIST — the core guarantee.

	var settingVal string
	if err := db.QueryRow("SELECT value FROM settings WHERE key = ?", "custom_survivor").Scan(&settingVal); err != nil {
		if err == sql.ErrNoRows {
			t.Errorf("DESTRUCTIVE MIGRATION: settings row 'custom_survivor' was lost during upgrade")
		} else {
			t.Fatalf("querying settings survivor: %v", err)
		}
	} else if settingVal != "keep-me" {
		t.Errorf("settings survivor value changed: got %q, want %q", settingVal, "keep-me")
	}

	var jobCmd, jobStatus string
	if err := db.QueryRow("SELECT command, status FROM jobs WHERE id = ?", jobID).Scan(&jobCmd, &jobStatus); err != nil {
		if err == sql.ErrNoRows {
			t.Errorf("DESTRUCTIVE MIGRATION: jobs row id=%d was lost during upgrade", jobID)
		} else {
			t.Fatalf("querying jobs survivor: %v", err)
		}
	} else if jobCmd != "hauler" || jobStatus != "succeeded" {
		t.Errorf("jobs survivor changed: got (%q,%q), want (%q,%q)", jobCmd, jobStatus, "hauler", "succeeded")
	}

	var gotToken string
	if err := db.QueryRow("SELECT token FROM sessions WHERE token = ?", survivingToken).Scan(&gotToken); err != nil {
		if err == sql.ErrNoRows {
			t.Errorf("DESTRUCTIVE MIGRATION: sessions row %q was lost during upgrade", survivingToken)
		} else {
			t.Fatalf("querying sessions survivor: %v", err)
		}
	} else if gotToken != survivingToken {
		t.Errorf("sessions survivor changed: got %q, want %q", gotToken, survivingToken)
	}

	var serveType string
	if err := db.QueryRow("SELECT serve_type FROM serve_processes WHERE id = ?", serveID).Scan(&serveType); err != nil {
		if err == sql.ErrNoRows {
			t.Errorf("DESTRUCTIVE MIGRATION: serve_processes row id=%d was lost during upgrade", serveID)
		} else {
			t.Fatalf("querying serve_processes survivor: %v", err)
		}
	} else if serveType != "registry" {
		t.Errorf("serve_processes survivor changed: got %q, want %q", serveType, "registry")
	}

	// 5. Assert the upgrade actually added the expected new schema objects.

	// 'hauls' table introduced by 0005.
	var haulsCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='hauls'",
	).Scan(&haulsCount); err != nil {
		t.Fatalf("checking hauls table: %v", err)
	}
	if haulsCount != 1 {
		t.Errorf("expected 'hauls' table to exist after upgrade, found %d", haulsCount)
	}

	// serve_processes gained 'role' and 'hostname' columns in 0007.
	serveCols := serveProcessesColumns(t, db)
	for _, col := range []string{"role", "hostname"} {
		if !serveCols[col] {
			t.Errorf("expected serve_processes to have column %q after upgrade", col)
		}
	}
}

// serveProcessesColumns returns the set of column names on serve_processes via
// pragma table_info.
func serveProcessesColumns(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(serve_processes)")
	if err != nil {
		t.Fatalf("reading serve_processes table_info: %v", err)
	}
	defer func() { _ = rows.Close() }() // close error not actionable; rows are fully read or abandoned
	cols := make(map[string]bool)
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scanning table_info row: %v", err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating table_info: %v", err)
	}
	return cols
}

// TestFreshMigrationsSchemaSanity applies ALL migrations fresh through the
// package's normal Open() path and runs a representative query against each key
// table, confirming the head schema is coherent (right tables, right columns).
func TestFreshMigrationsSchemaSanity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("opening fresh database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	}()

	// One representative query per key table. Each SELECT names columns that must
	// exist at head, so a missing table or column fails the query (and the test).
	queries := map[string]string{
		"settings":        "SELECT key, value FROM settings LIMIT 1",
		"jobs":            "SELECT command, status, result, haul_id FROM jobs LIMIT 1",
		"job_logs":        "SELECT job_id, stream, content FROM job_logs LIMIT 1",
		"sessions":        "SELECT token, expires_at FROM sessions LIMIT 1",
		"hauls":           "SELECT name, slug, store_dir FROM hauls LIMIT 1",
		"store_contents":  "SELECT haul_id, content_type, name FROM store_contents LIMIT 1",
		"saved_manifests": "SELECT haul_id, name, yaml_content FROM saved_manifests LIMIT 1",
		"serve_processes": "SELECT serve_type, port, role, hostname, haul_id FROM serve_processes LIMIT 1",
	}
	for table, q := range queries {
		rows, err := db.Query(q)
		if err != nil {
			t.Errorf("representative query on %s failed: %v", table, err)
			continue
		}
		// Iterate (tables are empty; we only care that the query is well-formed
		// against the head schema, i.e. all named columns resolve).
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			t.Errorf("iterating representative query on %s: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Errorf("close rows: %v", err)
		}
	}
}
