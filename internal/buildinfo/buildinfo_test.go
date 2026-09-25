package buildinfo

import (
	"runtime"
	"strings"
	"testing"
)

func TestRead(t *testing.T) {
	info := Read()
	if info.Version != Version {
		t.Errorf("Version = %q, want %q", info.Version, Version)
	}
	if info.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", info.GoVersion, runtime.Version())
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; info.Platform != want {
		t.Errorf("Platform = %q, want %q", info.Platform, want)
	}
}

func TestInfoString(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want string
	}{
		{
			name: "unknown commit",
			info: Info{Version: "dev", GoVersion: "go1.24.7", Platform: "linux/amd64"},
			want: "nodr dev (commit unknown, go1.24.7, linux/amd64)",
		},
		{
			name: "long commit is shortened",
			info: Info{Version: "v0.1.0", Commit: "0123456789abcdef0123", GoVersion: "go1.24.7", Platform: "linux/arm64"},
			want: "nodr v0.1.0 (commit 0123456789ab, go1.24.7, linux/arm64)",
		},
		{
			name: "modified tree",
			info: Info{Version: "dev", Commit: "abc123", Modified: true, GoVersion: "go1.24.7", Platform: "linux/amd64"},
			want: "nodr dev (commit abc123-dirty, go1.24.7, linux/amd64)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
			if !strings.HasPrefix(tt.info.String(), "nodr ") {
				t.Errorf("String() should start with the program name")
			}
		})
	}
}
