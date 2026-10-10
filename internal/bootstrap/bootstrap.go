// Package bootstrap provides durable first-run bootstrap primitives.
package bootstrap

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/secrets"
)

// Config configures bootstrap storage and KEK sources.
type Config struct {
	DataDir string
	KEKFile string
	KEKEnv  string
}

// DataRoot returns the configured data directory or its XDG default.
func (c Config) DataRoot() string {
	if c.DataDir != "" {
		return c.DataDir
	}
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "nodr")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "nodr")
}

// State records completed bootstrap state.
type State struct {
	Version   int    `json:"version"`
	Workspace string `json:"workspace"`
}

// LoadState reads and validates bootstrap state.
func LoadState(dataDir string) (State, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "bootstrap.json"))
	if err != nil {
		return State{}, fmt.Errorf("bootstrap: read state: %w", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("bootstrap: parse state: %w", err)
	}
	if state.Version != 1 {
		return State{}, fmt.Errorf("bootstrap: unsupported state version %d", state.Version)
	}
	if !validStateWorkspace(state.Workspace) {
		return State{}, errors.New("bootstrap: state workspace must be a clean relative workspaces child")
	}
	return state, nil
}

func validStateWorkspace(workspace string) bool {
	if filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace || filepath.Dir(workspace) != "workspaces" {
		return false
	}
	return ValidateName(filepath.Base(workspace)) == nil
}

// WriteState atomically persists state with restrictive permissions.
func WriteState(dataDir string, state State) error {
	if !validStateWorkspace(state.Workspace) {
		return errors.New("bootstrap: state workspace must be a clean relative workspaces child")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("bootstrap: create data root: %w", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("bootstrap: encode state: %w", err)
	}
	file, err := os.CreateTemp(dataDir, ".bootstrap-*")
	if err != nil {
		return fmt.Errorf("bootstrap: create state temp: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(dataDir, "bootstrap.json")); err != nil {
		return fmt.Errorf("bootstrap: rename state: %w", err)
	}
	return nil
}

// ValidateName applies the NRM resource name rule to a workspace name.
func ValidateName(name string) error { return nrm.ValidateName(name) }

// WorkspaceTarget validates name and returns a new, non-symlink workspace target.
func WorkspaceTarget(dataDir, name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", fmt.Errorf("bootstrap: invalid workspace name: %w", err)
	}
	parent := filepath.Join(dataDir, "workspaces")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", fmt.Errorf("bootstrap: create workspace parent: %w", err)
	}
	target := filepath.Join(parent, name)
	info, err := os.Lstat(target)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("bootstrap: workspace target is a symlink")
		}
		return "", errors.New("bootstrap: workspace target already exists")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("bootstrap: inspect workspace target: %w", err)
	}
	return target, nil
}

// ResolveKEK resolves a configured KEK or creates and reuses the bootstrap KEK.
func ResolveKEK(cfg Config, warning io.Writer) ([]byte, error) {
	if cfg.KEKFile != "" || cfg.KEKEnv != "" {
		return secrets.LoadKEK(secrets.KEKConfig{FilePath: cfg.KEKFile, EnvVar: cfg.KEKEnv})
	}
	dataDir := cfg.DataRoot()
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("bootstrap: create data root: %w", err)
	}
	path := filepath.Join(dataDir, "kek")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: open KEK lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("bootstrap: lock KEK: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	if key, err := secrets.LoadKEK(secrets.KEKConfig{FilePath: path}); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("bootstrap: generate KEK: %w", err)
	}
	file, err := os.CreateTemp(dataDir, ".kek-*")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: create KEK temp: %w", err)
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, fmt.Errorf("bootstrap: chmod KEK temp: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		file.Close()
		return nil, fmt.Errorf("bootstrap: write KEK: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, fmt.Errorf("bootstrap: sync KEK: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("bootstrap: close KEK: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return nil, fmt.Errorf("bootstrap: rename KEK: %w", err)
	}
	if warning != nil {
		fmt.Fprintf(warning, "generated KEK at %s; back it up before moving or restoring the data directory: losing it makes encrypted credentials unrecoverable\n", path)
	}
	return key, nil
}
