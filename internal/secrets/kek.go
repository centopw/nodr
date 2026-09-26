package secrets

import (
	"encoding/base64"
	"fmt"
	"os"
)

// KEKConfig specifies how to locate and load the 32-byte Key Encryption Key.
type KEKConfig struct {
	FilePath string // Path to file containing 32 raw bytes on disk
	EnvVar   string // Name of environment variable containing base64-encoded 32 bytes
}

// LoadKEK loads a 32-byte KEK from FilePath if set, or EnvVar if set.
// If FilePath is used, the file mode must not grant read/write/execute to group or others
// (mode & 0077 != 0 is rejected).
func LoadKEK(cfg KEKConfig) ([]byte, error) {
	if cfg.FilePath != "" {
		info, err := os.Stat(cfg.FilePath)
		if err != nil {
			return nil, fmt.Errorf("secrets: stat KEK file: %w", err)
		}
		// Enforce strict file permissions: reject group/other permissions
		if info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("secrets: KEK file %q has insecure permissions %04o (must be 0600 or stricter): %w",
				cfg.FilePath, info.Mode().Perm(), ErrInvalidKEK)
		}
		data, err := os.ReadFile(cfg.FilePath)
		if err != nil {
			return nil, fmt.Errorf("secrets: read KEK file: %w", err)
		}
		if len(data) != 32 {
			return nil, fmt.Errorf("secrets: KEK file %q contains %d bytes (want 32): %w", cfg.FilePath, len(data), ErrInvalidKEK)
		}
		return data, nil
	}

	if cfg.EnvVar != "" {
		val := os.Getenv(cfg.EnvVar)
		if val == "" {
			return nil, fmt.Errorf("secrets: environment variable %s is empty or unset: %w", cfg.EnvVar, ErrInvalidKEK)
		}
		decoded, err := base64.StdEncoding.DecodeString(val)
		if err != nil {
			return nil, fmt.Errorf("secrets: decode KEK from env %s: %w", cfg.EnvVar, err)
		}
		if len(decoded) != 32 {
			return nil, fmt.Errorf("secrets: KEK from env %s is %d bytes (want 32): %w", cfg.EnvVar, len(decoded), ErrInvalidKEK)
		}
		return decoded, nil
	}

	return nil, fmt.Errorf("secrets: no KEK source specified in config: %w", ErrInvalidKEK)
}
