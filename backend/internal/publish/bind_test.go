package publish

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
)

// The internal registry must be configured to listen on loopback only so it is
// reachable solely through the authenticated proxy, never on a public interface.
func TestRegistryConfigBindsLoopbackOnly(t *testing.T) {
	cfgYAML := loopbackRegistryConfig("/data/h1/registry", 5999)
	if !strings.Contains(cfgYAML, "addr: 127.0.0.1:5999") {
		t.Errorf("config does not bind loopback:\n%s", cfgYAML)
	}
	for _, bad := range []string{"addr: :5999", "addr: 0.0.0.0", "addr: [::]"} {
		if strings.Contains(cfgYAML, bad) {
			t.Errorf("config exposes a public bind %q:\n%s", bad, cfgYAML)
		}
	}
	for _, want := range []string{`rootdirectory: "/data/h1/registry"`, "readonly:", "enabled: true"} {
		if !strings.Contains(cfgYAML, want) {
			t.Errorf("config missing %q:\n%s", want, cfgYAML)
		}
	}
}

func TestWriteRegistryConfigFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "registry")
	m := &Manager{cfg: &config.Config{DataDir: t.TempDir()}}
	path, err := m.writeRegistryConfig(1, dir, 6001)
	if err != nil {
		t.Fatalf("writeRegistryConfig: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("config written outside registry dir: %s", path)
	}
}
