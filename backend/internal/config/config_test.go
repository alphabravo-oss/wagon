package config

import (
	"path/filepath"
	"testing"
)

// clearEnv sets all env vars consulted by Load to empty so that getEnv falls
// back to its defaults. t.Setenv restores the original values at test end.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"HAULER_DIR",
		"HAULER_STORE_DIR",
		"HAULER_TEMP_DIR",
		"DOCKER_CONFIG",
		"DATABASE_PATH",
		"HOME",
		"HAULER_UI_PASSWORD",
		"HAULER_UI_PUBLISH_USER",
		"HAULER_UI_PUBLISH_PASSWORD",
		"HAULER_UI_INSECURE_NO_AUTH",
		"HAULER_UI_TRUSTED_ORIGINS",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg := Load()

	if cfg.HaulerDir != "/data" {
		t.Errorf("HaulerDir = %q, want %q", cfg.HaulerDir, "/data")
	}
	if cfg.HaulerStoreDir != "/data/store" {
		t.Errorf("HaulerStoreDir = %q, want %q", cfg.HaulerStoreDir, "/data/store")
	}
	if cfg.HaulerTempDir != "/data/tmp" {
		t.Errorf("HaulerTempDir = %q, want %q", cfg.HaulerTempDir, "/data/tmp")
	}
	if cfg.DatabasePath != "/data/app.db" {
		t.Errorf("DatabasePath = %q, want %q", cfg.DatabasePath, "/data/app.db")
	}
	if cfg.DataDir != "/data" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/data")
	}
	if cfg.UIPassword != "" {
		t.Errorf("UIPassword = %q, want empty", cfg.UIPassword)
	}
	// With HOME and DOCKER_CONFIG unset, docker config derives from HaulerDir.
	if want := "/data/.docker/config.json"; cfg.DockerAuthPath != want {
		t.Errorf("DockerAuthPath = %q, want %q", cfg.DockerAuthPath, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)

	t.Setenv("HAULER_DIR", "/custom")
	t.Setenv("HAULER_STORE_DIR", "/custom/mystore")
	t.Setenv("HAULER_TEMP_DIR", "/custom/mytmp")
	t.Setenv("DATABASE_PATH", "/custom/mydb.sqlite")
	t.Setenv("HAULER_UI_PASSWORD", "s3cret")

	cfg := Load()

	if cfg.HaulerDir != "/custom" {
		t.Errorf("HaulerDir = %q, want %q", cfg.HaulerDir, "/custom")
	}
	if cfg.HaulerStoreDir != "/custom/mystore" {
		t.Errorf("HaulerStoreDir = %q, want %q", cfg.HaulerStoreDir, "/custom/mystore")
	}
	if cfg.HaulerTempDir != "/custom/mytmp" {
		t.Errorf("HaulerTempDir = %q, want %q", cfg.HaulerTempDir, "/custom/mytmp")
	}
	if cfg.DatabasePath != "/custom/mydb.sqlite" {
		t.Errorf("DatabasePath = %q, want %q", cfg.DatabasePath, "/custom/mydb.sqlite")
	}
	if cfg.UIPassword != "s3cret" {
		t.Errorf("UIPassword = %q, want %q", cfg.UIPassword, "s3cret")
	}
	// DataDir tracks HaulerDir.
	if cfg.DataDir != "/custom" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/custom")
	}
}

func TestDockerAuthPathFromDockerConfig(t *testing.T) {
	clearEnv(t)

	// DOCKER_CONFIG takes precedence over HOME for the docker config dir.
	t.Setenv("HOME", "/home/user")
	t.Setenv("DOCKER_CONFIG", "/etc/docker-cfg")

	cfg := Load()

	if want := filepath.Join("/etc/docker-cfg", "config.json"); cfg.DockerAuthPath != want {
		t.Errorf("DockerAuthPath = %q, want %q", cfg.DockerAuthPath, want)
	}
}

func TestDockerAuthPathFromHome(t *testing.T) {
	clearEnv(t)

	// With DOCKER_CONFIG unset, the docker dir derives from HOME/.docker.
	t.Setenv("HOME", "/home/user")

	cfg := Load()

	if want := filepath.Join("/home/user", ".docker", "config.json"); cfg.DockerAuthPath != want {
		t.Errorf("DockerAuthPath = %q, want %q", cfg.DockerAuthPath, want)
	}
}

func TestToMapReflectsConfig(t *testing.T) {
	clearEnv(t)

	t.Setenv("HAULER_DIR", "/custom")
	t.Setenv("HAULER_STORE_DIR", "/custom/mystore")
	t.Setenv("HAULER_TEMP_DIR", "/custom/mytmp")
	t.Setenv("DATABASE_PATH", "/custom/mydb.sqlite")
	t.Setenv("DOCKER_CONFIG", "/etc/docker-cfg")

	cfg := Load()
	m := cfg.ToMap()

	checks := map[string]string{
		"haulerDir":       "/custom",
		"haulerStoreDir":  "/custom/mystore",
		"haulerTempDir":   "/custom/mytmp",
		"dockerAuthPath":  filepath.Join("/etc/docker-cfg", "config.json"),
		"databasePath":    "/custom/mydb.sqlite",
		"haulerDirEnv":    "HAULER_DIR",
		"haulerStoreEnv":  "HAULER_STORE_DIR",
		"haulerTempEnv":   "HAULER_TEMP_DIR",
		"dockerConfigEnv": "DOCKER_CONFIG",
		"databasePathEnv": "DATABASE_PATH",
	}
	for key, want := range checks {
		if got := m[key]; got != want {
			t.Errorf("ToMap()[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestToMapAuthEnabled(t *testing.T) {
	clearEnv(t)

	// No password: auth disabled.
	if got := Load().ToMap()["authEnabled"]; got != "false" {
		t.Errorf("authEnabled = %q, want %q (no password)", got, "false")
	}

	// Password set: auth enabled.
	t.Setenv("HAULER_UI_PASSWORD", "hunter2")
	if got := Load().ToMap()["authEnabled"]; got != "true" {
		t.Errorf("authEnabled = %q, want %q (password set)", got, "true")
	}
}

func TestValidateRequiresPassword(t *testing.T) {
	clearEnv(t)
	if err := Load().Validate(); err != ErrNoPassword {
		t.Fatalf("default config: Validate() = %v, want ErrNoPassword", err)
	}

	for _, v := range []string{"1", "yes", "TRUE-ish", "false"} {
		t.Setenv("HAULER_UI_INSECURE_NO_AUTH", v)
		if err := Load().Validate(); err != ErrNoPassword {
			t.Errorf("HAULER_UI_INSECURE_NO_AUTH=%q must not opt out: Validate() = %v", v, err)
		}
	}

	t.Setenv("HAULER_UI_INSECURE_NO_AUTH", "true")
	if err := Load().Validate(); err != nil {
		t.Errorf("explicit insecure opt-out: Validate() = %v, want nil", err)
	}

	t.Setenv("HAULER_UI_INSECURE_NO_AUTH", "")
	t.Setenv("HAULER_UI_PASSWORD", "s3cret")
	if err := Load().Validate(); err != nil {
		t.Errorf("password set: Validate() = %v, want nil", err)
	}
}

func TestTrustedOrigins(t *testing.T) {
	clearEnv(t)
	t.Setenv("HAULER_UI_TRUSTED_ORIGINS", " https://a.example , ,https://b.example:8443")
	got := Load().TrustedOrigins
	if len(got) != 2 || got[0] != "https://a.example" || got[1] != "https://b.example:8443" {
		t.Errorf("TrustedOrigins = %q", got)
	}
}
