package auth

import (
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/sqlite"
)

func openAuthTestDB(t *testing.T) *sql.DB {
	t.Helper()
	original := pbkdf2Iterations
	pbkdf2Iterations = 10
	t.Cleanup(func() { pbkdf2Iterations = original })
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db.DB
}

func createTestSession(t *testing.T, m *Manager) string {
	t.Helper()
	token, _, err := m.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	if !m.ValidateSession(token) {
		t.Fatal("new session must validate")
	}
	return token
}

func TestPasswordChangeRevokesSessions(t *testing.T) {
	db := openAuthTestDB(t)
	cfg := &config.Config{UIPassword: "password-A"}
	m := NewManager(db, cfg)
	token := createTestSession(t, m)

	m = NewManager(db, cfg)
	if !m.ValidateSession(token) {
		t.Fatal("same password must preserve the session")
	}

	cfg.UIPassword = "password-B"
	m = NewManager(db, cfg)
	if m.ValidateSession(token) {
		t.Fatal("changed password must revoke the old session")
	}
	createTestSession(t, m)

	for key, size := range map[string]int{"password_salt": 16, "password_fp": 32} {
		var value string
		if err := db.QueryRow(`SELECT v FROM auth_meta WHERE k = ?`, key).Scan(&value); err != nil {
			t.Fatal(err)
		}
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != size {
			t.Fatalf("%s must be hex-encoded with %d bytes", key, size)
		}
		for _, password := range []string{"password-A", "password-B"} {
			if value == password || string(decoded) == password {
				t.Fatalf("%s must not contain the password", key)
			}
		}
	}
}

func TestFirstStartupWithoutFingerprintRevokesExistingSessions(t *testing.T) {
	db := openAuthTestDB(t)
	if _, err := db.Exec(`INSERT INTO sessions (token, expires_at) VALUES ('legacy-token', datetime('now', '+24 hours'))`); err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, &config.Config{UIPassword: "password-A"})
	if m.ValidateSession("legacy-token") {
		t.Fatal("first startup without a fingerprint must revoke existing sessions")
	}
	createTestSession(t, m)
}

func TestDisabledAuthLeavesSessionsAndMetadataUntouched(t *testing.T) {
	db := openAuthTestDB(t)
	if _, err := db.Exec(`INSERT INTO sessions (token, expires_at) VALUES ('existing-token', datetime('now', '+24 hours'))`); err != nil {
		t.Fatal(err)
	}
	NewManager(db, &config.Config{})
	for table, want := range map[string]int{"sessions": 1, "auth_meta": 0} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("%s has %d rows, want %d", table, count, want)
		}
	}
}

func TestFailedSessionDeletionDoesNotRecordChangedPassword(t *testing.T) {
	db := openAuthTestDB(t)
	fatalCalled := captureFatal(t)
	m := NewManager(db, &config.Config{UIPassword: "password-A"})
	token := createTestSession(t, m)
	var originalFingerprint string
	if err := db.QueryRow(`SELECT v FROM auth_meta WHERE k = 'password_fp'`).Scan(&originalFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER prevent_session_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'delete failed'); END`); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{UIPassword: "password-B"}
	NewManager(db, cfg)
	if !*fatalCalled {
		t.Fatal("failed revocation and fallback must refuse startup")
	}
	var fingerprint string
	if err := db.QueryRow(`SELECT v FROM auth_meta WHERE k = 'password_fp'`).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	if fingerprint != originalFingerprint {
		t.Fatal("failed deletion must not record the changed password fingerprint")
	}
	if _, err := db.Exec(`DROP TRIGGER prevent_session_delete`); err != nil {
		t.Fatal(err)
	}
	m = NewManager(db, cfg)
	if m.ValidateSession(token) {
		t.Fatal("startup must retry revocation after a failed deletion")
	}
}

func captureFatal(t *testing.T) *bool {
	t.Helper()
	original := fatalf
	t.Cleanup(func() { fatalf = original })
	called := false
	fatalf = func(string, ...any) { called = true }
	return &called
}

func TestInvalidSaltIsRegenerated(t *testing.T) {
	for _, invalidSalt := range []string{"zz", "00"} {
		t.Run(invalidSalt, func(t *testing.T) {
			db := openAuthTestDB(t)
			cfg := &config.Config{UIPassword: "password-A"}
			m := NewManager(db, cfg)
			token := createTestSession(t, m)
			var oldFingerprint string
			if err := db.QueryRow(`SELECT v FROM auth_meta WHERE k = 'password_fp'`).Scan(&oldFingerprint); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE auth_meta SET v = ? WHERE k = 'password_salt'`, invalidSalt); err != nil {
				t.Fatal(err)
			}
			m = NewManager(db, cfg)
			if m.ValidateSession(token) {
				t.Fatal("invalid salt must cause existing sessions to be revoked")
			}
			for key, size := range map[string]int{"password_salt": 16, "password_fp": 32} {
				var value string
				if err := db.QueryRow(`SELECT v FROM auth_meta WHERE k = ?`, key).Scan(&value); err != nil {
					t.Fatal(err)
				}
				decoded, err := hex.DecodeString(value)
				if err != nil || len(decoded) != size {
					t.Fatalf("%s must be hex-encoded with %d bytes", key, size)
				}
				if key == "password_fp" && value == oldFingerprint {
					t.Fatal("regenerated salt must produce a new fingerprint")
				}
			}
			token = createTestSession(t, m)
			m = NewManager(db, cfg)
			if !m.ValidateSession(token) {
				t.Fatal("same password after salt repair must preserve new sessions")
			}
		})
	}
}

func TestPasswordCheckErrorRevokesSessionsOrRefusesStartup(t *testing.T) {
	for _, blockDelete := range []bool{false, true} {
		name := "fallback succeeds"
		if blockDelete {
			name = "fallback fails"
		}
		t.Run(name, func(t *testing.T) {
			db := openAuthTestDB(t)
			fatalCalled := captureFatal(t)
			cfg := &config.Config{UIPassword: "password-A"}
			m := NewManager(db, cfg)
			createTestSession(t, m)
			if _, err := db.Exec(`DROP TABLE auth_meta`); err != nil {
				t.Fatal(err)
			}
			if blockDelete {
				if _, err := db.Exec(`CREATE TRIGGER prevent_session_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'delete failed'); END`); err != nil {
					t.Fatal(err)
				}
			}
			m = NewManager(db, cfg)
			if *fatalCalled != blockDelete {
				t.Fatalf("fatal called = %v, want %v", *fatalCalled, blockDelete)
			}
			if !blockDelete {
				if m == nil {
					t.Fatal("successful fallback must allow startup")
				}
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatal("password check error must revoke all existing sessions")
				}
			}
		})
	}
}
