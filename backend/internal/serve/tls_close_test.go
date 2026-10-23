package serve

import (
	"bytes"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeWriteCloser records writes and returns configurable errors.
type fakeWriteCloser struct {
	buf      bytes.Buffer
	writeErr error
	closeErr error
	closed   bool
}

func (f *fakeWriteCloser) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.buf.Write(p)
}

func (f *fakeWriteCloser) Close() error {
	f.closed = true
	return f.closeErr
}

func TestEncodePEMAndCloseReturnsCloseError(t *testing.T) {
	closeErr := errors.New("disk full on flush")
	w := &fakeWriteCloser{closeErr: closeErr}
	err := encodePEMAndClose(w, &pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")})
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error, got %v", err)
	}
	if !w.closed {
		t.Fatal("writer was not closed")
	}
}

func TestEncodePEMAndCloseReturnsWriteErrorAndCloses(t *testing.T) {
	writeErr := errors.New("write failed")
	w := &fakeWriteCloser{writeErr: writeErr, closeErr: errors.New("close failed")}
	err := encodePEMAndClose(w, &pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")})
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected write error to take precedence, got %v", err)
	}
	if !w.closed {
		t.Fatal("writer was not closed after a write error")
	}
}

func TestEncodePEMAndCloseSuccess(t *testing.T) {
	w := &fakeWriteCloser{}
	if err := encodePEMAndClose(w, &pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b, _ := pem.Decode(w.buf.Bytes()); b == nil || b.Type != "CERTIFICATE" {
		t.Fatalf("unexpected PEM output %q", w.buf.String())
	}
}

func TestGenerateCertWritesValidFiles(t *testing.T) {
	dir := t.TempDir()
	cm := NewCertManager(dir)
	certPath, keyPath, err := cm.generateCert("registry", filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"))
	if err != nil {
		t.Fatalf("generateCert: %v", err)
	}
	for path, typ := range map[string]string{certPath: "CERTIFICATE", keyPath: "EC PRIVATE KEY"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if b, _ := pem.Decode(data); b == nil || b.Type != typ {
			t.Fatalf("%s: expected PEM %s", path, typ)
		}
	}
	if fi, err := os.Stat(keyPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, err %v; want 0600", fi.Mode().Perm(), err)
	}
}
