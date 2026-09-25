// Package buildinfo reports version information about the running binary.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Version is the release version of nodr. Release builds set it with
// -ldflags "-X github.com/centopw/nodr/internal/buildinfo.Version=<version>".
var Version = "dev"

// Info describes the running binary.
type Info struct {
	Version   string
	Commit    string
	Modified  bool
	GoVersion string
	Platform  string
}

// Read returns information about the running binary. The commit comes from
// the version control data that the Go toolchain embeds at build time.
func Read() Info {
	info := Info{
		Version:   Version,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Commit = s.Value
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}
	return info
}

// String formats the information on one line, for example
// "nodr dev (commit 1a2b3c4d, go1.24.7, linux/amd64)".
func (i Info) String() string {
	commit := "unknown"
	if i.Commit != "" {
		commit = i.Commit
		if len(commit) > 12 {
			commit = commit[:12]
		}
		if i.Modified {
			commit += "-dirty"
		}
	}
	return fmt.Sprintf("nodr %s (commit %s, %s, %s)", i.Version, commit, i.GoVersion, i.Platform)
}
