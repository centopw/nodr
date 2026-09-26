package proxmox

import (
	"errors"
	"fmt"
)

// ErrFingerprintMismatch reports that a server's TLS certificate does not
// match the pinned fingerprint configured for the connection.
var ErrFingerprintMismatch = errors.New("proxmox: TLS certificate fingerprint does not match pinned value")

// APIError reports a non-2xx response from the Proxmox VE API. It never
// includes credentials in its error message.
type APIError struct {
	Status  int    // HTTP status code (e.g. 401, 403, 500)
	Message string // Redacted error message from the response body
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("proxmox: API error (status %d)", e.Status)
	}
	return fmt.Sprintf("proxmox: API error %d: %s", e.Status, e.Message)
}
