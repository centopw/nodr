package secrets

import "errors"

// ErrNotFound reports that a requested secret reference does not exist in the store.
var ErrNotFound = errors.New("secrets: secret not found")

// ErrDecrypt reports that decryption failed (invalid KEK, corrupted data, or tampering).
var ErrDecrypt = errors.New("secrets: decryption failed")

// ErrInvalidKEK reports that a KEK was missing or did not meet length/permission requirements.
var ErrInvalidKEK = errors.New("secrets: invalid KEK")
