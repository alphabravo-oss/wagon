package publish

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
)

func TestTLSCertificateLifecycle(t *testing.T) {
	t.Setenv("HAULER_UI_REGISTRY_DOMAIN", "registry.example")
	t.Setenv("HAULER_UI_REGISTRY_TLS_CERT", "")
	t.Setenv("HAULER_UI_REGISTRY_TLS_KEY", "")
	m := &Manager{cfg: &config.Config{DataDir: t.TempDir()}}
	if m.hasTLS() || m.TLSStatus().Source != "none" {
		t.Fatal("unexpected initial TLS")
	}
	if _, err := m.getCertificate(nil); err == nil {
		t.Fatal("expected missing certificate error")
	}
	m.tls = &tlsState{}
	if _, err := m.getCertificate(nil); err == nil {
		t.Fatal("expected empty certificate error")
	}
	m.bootstrapTLS()
	status := m.TLSStatus()
	if !m.hasTLS() || status.Source != "self-signed" || !reflect.DeepEqual(status.DNSNames, []string{"*.registry.example", "registry.example", "localhost"}) {
		t.Fatalf("status=%+v", status)
	}
	cert, err := m.getCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"app.registry.example", "registry.example", "localhost"} {
		if err := leaf.VerifyHostname(host); err != nil {
			t.Fatal(err)
		}
	}
	expiry, err := time.Parse(time.RFC3339, status.NotAfter)
	if err != nil || !expiry.After(time.Now()) {
		t.Fatalf("expiry=%s err=%v", status.NotAfter, err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyDER, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := m.SetProvidedTLS([]byte("invalid"), keyPEM); err == nil {
		t.Fatal("accepted invalid certificate")
	}
	if m.TLSStatus().Source != "self-signed" {
		t.Fatal("invalid input changed active certificate")
	}
	if err := m.SetProvidedTLS(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"cert.pem": certPEM, "key.pem": keyPEM} {
		path := filepath.Join(m.tlsDir(), name)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s: err=%v", name, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("mode=%v", info.Mode())
		}
	}
	m.bootstrapTLS()
	if m.TLSStatus().Source != "provided" {
		t.Fatal("did not restore certificate from disk")
	}
	t.Setenv("HAULER_UI_REGISTRY_TLS_CERT", filepath.Join(m.tlsDir(), "cert.pem"))
	t.Setenv("HAULER_UI_REGISTRY_TLS_KEY", filepath.Join(m.tlsDir(), "key.pem"))
	other := &Manager{cfg: &config.Config{DataDir: t.TempDir()}}
	other.bootstrapTLS()
	if other.TLSStatus().Source != "provided" {
		t.Fatal("did not load environment certificate")
	}
	if err := m.ClearProvidedTLS(); err != nil {
		t.Fatal(err)
	}
	if m.TLSStatus().Source != "self-signed" {
		t.Fatal("did not reset certificate")
	}
	for _, name := range []string{"cert.pem", "key.pem"} {
		if _, err := os.Stat(filepath.Join(m.tlsDir(), name)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", name, err)
		}
	}
	if _, _, err := readPEMFiles(filepath.Join(other.cfg.DataDir, "missing"), ""); err == nil {
		t.Fatal("expected missing certificate error")
	}
	path := filepath.Join(other.cfg.DataDir, "cert.pem")
	writePublishFixture(t, path, string(certPEM))
	if _, _, err := readPEMFiles(path, path+".missing"); err == nil {
		t.Fatal("expected missing key error")
	}
}
