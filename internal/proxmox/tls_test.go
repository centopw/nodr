package proxmox

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func generateTestCert(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "127.0.0.1",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	return cert, priv
}

func TestPinnedHTTPClient_MatchingFingerprint(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	leafCert := ts.Certificate()
	fp := FormatFingerprint(leafCert)

	client := NewPinnedHTTPClient(fp)
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("expected successful GET with pinned fingerprint, got: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestPinnedHTTPClient_MismatchedFingerprint(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	wrongFP := "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"

	client := NewPinnedHTTPClient(wrongFP)
	_, err := client.Get(ts.URL)
	if err == nil {
		t.Fatal("expected fingerprint mismatch error, got nil")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch in error chain, got %v", err)
	}
}

func TestFormatFingerprint_Consistency(t *testing.T) {
	cert, _ := generateTestCert(t)
	fp1 := FormatFingerprint(cert)
	fp2 := FormatFingerprint(cert)
	if fp1 != fp2 {
		t.Errorf("FormatFingerprint is not deterministic: %q vs %q", fp1, fp2)
	}
	if len(fp1) != 95 { // 32 hex bytes * 2 + 31 colons = 95 characters
		t.Errorf("FormatFingerprint length = %d, want 95", len(fp1))
	}
}
