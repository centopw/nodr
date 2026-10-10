package githubapp_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/centopw/nodr/internal/githubapp"
)

// mustSignatureService builds a Service for webhook verification tests.
// The fake API base URL is never reached because only signature
// verification is exercised.
func mustSignatureService(t *testing.T) *githubapp.Service {
	t.Helper()
	keyPEM, _ := testKey(t)
	svc, err := githubapp.NewService(fakeAppID, keyPEM, webhookTestSecret)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestVerifyWebhookSignature(t *testing.T) {
	svc := mustSignatureService(t)
	payload := []byte(`{"action":"push","installation":6789}`)

	mac := hmac.New(sha256.New, []byte(webhookTestSecret))
	mac.Write(payload)
	validDigest := hex.EncodeToString(mac.Sum(nil))

	otherMAC := hmac.New(sha256.New, []byte("wrong-secret"))
	otherMAC.Write(payload)

	tests := []struct {
		name      string
		signature string
		wantValid bool
	}{
		{name: "valid", signature: "sha256=" + validDigest, wantValid: true},
		{name: "tampered digest", signature: "sha256=" + validDigest[:len(validDigest)-4] + "dead", wantValid: false},
		{name: "different secret", signature: "sha256=" + hex.EncodeToString(otherMAC.Sum(nil)), wantValid: false},
		{name: "wrong digest over same payload", signature: "sha256=" + hex.EncodeToString(payload), wantValid: false},
		{name: "missing header", signature: "", wantValid: false},
		{name: "wrong algorithm prefix", signature: "sha1=" + validDigest, wantValid: false},
		{name: "non-hex digest", signature: "sha256=zzz-not-hex", wantValid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.VerifyWebhookSignature(payload, tt.signature)
			if !tt.wantValid {
				if !errors.Is(err, githubapp.ErrWebhookSignature) {
					t.Fatalf("err = %v, want ErrWebhookSignature", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyWebhookSignature: %v", err)
			}
		})
	}
}
