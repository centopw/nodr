package proxmox_test

import (
	"errors"
	"testing"

	"github.com/centopw/nodr/internal/proxmox"
)

func TestAPIError_Format(t *testing.T) {
	err := &proxmox.APIError{Status: 404, Message: "not found"}
	if got := err.Error(); got != "proxmox: API error 404: not found" {
		t.Errorf("Error() = %q, want %q", got, "proxmox: API error 404: not found")
	}

	errNoMsg := &proxmox.APIError{Status: 500}
	if got := errNoMsg.Error(); got != "proxmox: API error (status 500)" {
		t.Errorf("Error() = %q, want %q", got, "proxmox: API error (status 500)")
	}
}

func TestErrFingerprintMismatch(t *testing.T) {
	if !errors.Is(proxmox.ErrFingerprintMismatch, proxmox.ErrFingerprintMismatch) {
		t.Fatal("ErrFingerprintMismatch should match itself")
	}
}
