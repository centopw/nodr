package proxmox

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// NewPinnedHTTPClient returns an *http.Client whose TLS handshakes are
// verified against fingerprint (Proxmox VE's own colon-separated uppercase
// hex SHA-256 format, as returned by GetCertFingerprint) instead of the
// system certificate pool. A handshake with a server whose leaf certificate
// does not produce this fingerprint fails with an error wrapping
// ErrFingerprintMismatch.
//
// Handshakes are verified via VerifyPeerCertificate; InsecureSkipVerify is
// set to true to suppress Go's default CA-pool verification, matching
// Proxmox's own trust model for self-signed cluster certificates.
func NewPinnedHTTPClient(fingerprint string) *http.Client {
	want := normalizeFingerprint(fingerprint)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("tls: no peer certificates presented")
			}
			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("tls: parse peer certificate: %w", err)
			}
			actual := FormatFingerprint(cert)
			if actual != want {
				return fmt.Errorf("%w: got %s, want %s", ErrFingerprintMismatch, actual, want)
			}
			return nil
		},
	}
	return &http.Client{
		Transport: transport,
	}
}

// FormatFingerprint returns the uppercase colon-delimited hex SHA-256 fingerprint
// of cert (e.g. "AA:BB:CC:...").
func FormatFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	var parts []string
	for _, b := range sum {
		parts = append(parts, fmt.Sprintf("%02X", b))
	}
	return strings.Join(parts, ":")
}

func normalizeFingerprint(fp string) string {
	clean := strings.ToUpper(strings.TrimSpace(fp))
	clean = strings.ReplaceAll(clean, ":", "")
	clean = strings.ReplaceAll(clean, " ", "")
	if clean == "" {
		return ""
	}
	// Reformat as colon-delimited
	var parts []string
	for i := 0; i+2 <= len(clean); i += 2 {
		parts = append(parts, clean[i:i+2])
	}
	return strings.Join(parts, ":")
}
