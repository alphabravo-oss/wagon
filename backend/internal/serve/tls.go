package serve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	CertValidityDays = 365
)

type CertManager struct {
	certsDir string
}

func NewCertManager(baseDataDir string) *CertManager {
	return &CertManager{
		certsDir: filepath.Join(baseDataDir, "certs"),
	}
}

// GetOrGenerateCert returns existing cert paths if valid, otherwise generates new ones
func (cm *CertManager) GetOrGenerateCert(serveType string) (certPath, keyPath string, err error) {
	if err := os.MkdirAll(cm.certsDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create certs dir: %w", err)
	}

	certFilename := fmt.Sprintf("%s.crt", serveType)
	keyFilename := fmt.Sprintf("%s.key", serveType)
	certPath = filepath.Join(cm.certsDir, certFilename)
	keyPath = filepath.Join(cm.certsDir, keyFilename)

	// Check if existing cert is still valid
	if cm.isCertValid(certPath) {
		return certPath, keyPath, nil
	}

	// Generate new certificate
	return cm.generateCert(serveType, certPath, keyPath)
}

func (cm *CertManager) isCertValid(certPath string) bool {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}

	return time.Now().Before(cert.NotAfter)
}

func (cm *CertManager) generateCert(serveType, certPath, keyPath string) (string, string, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("failed to generate serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Wagon"},
			CommonName:   serveType,
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().AddDate(0, 0, CertValidityDays),
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("0.0.0.0")},
	}

	if hostname, err := os.Hostname(); err == nil {
		template.DNSNames = append(template.DNSNames, hostname)
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return "", "", fmt.Errorf("failed to create certificate: %w", err)
	}

	// Write certificate
	certOut, err := os.Create(certPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to create cert file: %w", err)
	}
	if err := encodePEMAndClose(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return "", "", fmt.Errorf("failed to write cert: %w", err)
	}

	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal key: %w", err)
	}

	// Write key
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", "", fmt.Errorf("failed to create key file: %w", err)
	}
	if err := encodePEMAndClose(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes}); err != nil {
		return "", "", fmt.Errorf("failed to write key: %w", err)
	}

	return certPath, keyPath, nil
}

// encodePEMAndClose writes block to w and closes it. A Close error is returned
// (when encoding succeeded) because on some filesystems it is the first point a
// failed write is reported, and a truncated cert/key must not be reported as OK.
func encodePEMAndClose(w io.WriteCloser, block *pem.Block) error {
	if err := pem.Encode(w, block); err != nil {
		_ = w.Close() // already failing; the encode error is the one to report
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
