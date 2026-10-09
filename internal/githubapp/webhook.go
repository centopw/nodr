package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// signatureHeader is the GitHub webhook header carrying the HMAC-SHA256
// digest of the raw request body.
const signatureHeader = "X-Hub-Signature-256"

// ErrWebhookSignature reports a missing, malformed, or non-matching webhook
// signature. Callers must reject the delivery before parsing the body.
var ErrWebhookSignature = errors.New("githubapp: invalid webhook signature")

// VerifyWebhookSignature checks the raw webhook payload against the
// sha256=<hex> digest sent in X-Hub-Signature-256, computed with the app's
// webhook secret. The comparison is constant time.
func (s *Service) VerifyWebhookSignature(payload []byte, signature string) error {
	if signature == "" {
		return fmt.Errorf("githubapp: missing %s header: %w", signatureHeader, ErrWebhookSignature)
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return fmt.Errorf("%w: %s must be %q-prefixed hex, got %q", ErrWebhookSignature, signatureHeader, prefix, signature)
	}
	given, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return fmt.Errorf("%w: %s digest is not hex: %q", ErrWebhookSignature, signatureHeader, signature)
	}
	mac := hmac.New(sha256.New, s.webhookSecret)
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), given) {
		return fmt.Errorf("%w: %s digest mismatch", ErrWebhookSignature, signatureHeader)
	}
	return nil
}
