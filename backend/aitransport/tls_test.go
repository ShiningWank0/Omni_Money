package aitransport

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestBuildServerTLSConfigRequiresMTLSForRemoteListener(t *testing.T) {
	if config, err := BuildServerTLSConfig("127.0.0.1", "", "", ""); err != nil || config != nil {
		t.Fatalf("loopback plaintext config=%#v err=%v", config, err)
	}
	if _, err := BuildServerTLSConfig("0.0.0.0", "", "", ""); err == nil {
		t.Fatal("remote listener accepted plaintext")
	}
	if _, err := BuildServerTLSConfig("localhost", "", "", ""); err == nil {
		t.Fatal("hostname-based listener bypassed literal loopback requirement")
	}
}

func TestMutualTLSHandshake(t *testing.T) {
	files := writeTestPKI(t)
	serverConfig, err := BuildServerTLSConfig("0.0.0.0", files.serverCert, files.serverKey, files.caCert)
	if err != nil {
		t.Fatal(err)
	}
	if serverConfig.MinVersion != tls.VersionTLS13 || serverConfig.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("server TLS config = %#v", serverConfig)
	}
	clientConfig, err := BuildClientTLSConfig(files.caCert, files.clientCert, files.clientKey, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	if clientConfig.InsecureSkipVerify {
		t.Fatal("client TLS verification was disabled")
	}

	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()
	serverTLS := tls.Server(serverSide, serverConfig)
	clientTLS := tls.Client(clientSide, clientConfig)
	serverResult := make(chan error, 1)
	go func() { serverResult <- serverTLS.Handshake() }()
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("client handshake failed: %v", err)
	}
	if err := <-serverResult; err != nil {
		t.Fatalf("server handshake failed: %v", err)
	}
}

func TestTLSSecretSymlinkIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	files := writeTestPKI(t)
	link := filepath.Join(t.TempDir(), "server-key-link.pem")
	if err := os.Symlink(files.serverKey, link); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildServerTLSConfig("127.0.0.1", files.serverCert, link, ""); err == nil {
		t.Fatal("symlinked TLS key was accepted")
	}
}

func TestWritableTLSCAIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are required")
	}
	files := writeTestPKI(t)
	if err := os.Chmod(files.caCert, 0o664); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildServerTLSConfig("0.0.0.0", files.serverCert, files.serverKey, files.caCert); err == nil {
		t.Fatal("group-writable client CA was accepted")
	}
}

func TestWorldReadableHostTLSKeyIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are required")
	}
	files := writeTestPKI(t)
	if err := os.Chmod(files.serverKey, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildServerTLSConfig("127.0.0.1", files.serverCert, files.serverKey, ""); err == nil {
		t.Fatal("world-readable host TLS key was accepted")
	}
}

type testPKIFiles struct {
	caCert     string
	serverCert string
	serverKey  string
	clientCert string
	clientKey  string
}

func writeTestPKI(t *testing.T) testPKIFiles {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Omni Money test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	caPath := filepath.Join(dir, "ca.pem")
	writePEMFile(t, caPath, 0644, "CERTIFICATE", caDER)
	serverCert, serverKey := writeLeafCertificate(t, dir, "server", caCert, caKey, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCert, clientKey := writeLeafCertificate(t, dir, "client", caCert, caKey, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	return testPKIFiles{
		caCert: caPath, serverCert: serverCert, serverKey: serverKey,
		clientCert: clientCert, clientKey: clientKey,
	}
}

func writeLeafCertificate(t *testing.T, dir, name string, ca *x509.Certificate, caKey *rsa.PrivateKey, usages []x509.ExtKeyUsage) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(int64(len(name) + 10)),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usages,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, name+"-cert.pem")
	keyPath := filepath.Join(dir, name+"-key.pem")
	writePEMFile(t, certPath, 0644, "CERTIFICATE", der)
	writePEMFile(t, keyPath, 0600, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
	return certPath, keyPath
}

func writePEMFile(t *testing.T, path string, mode os.FileMode, blockType string, der []byte) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}
