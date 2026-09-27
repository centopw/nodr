package planapply

import (
	"bytes"
	"errors"
	"io"

	"github.com/centopw/nodr/internal/engine/opentofu"
)

// redactor wraps an io.Writer, replacing occurrences of any secret in
// secrets with "<redacted>" before forwarding to w. It preserves standard
// io.Writer semantics: Write returns len(p) on complete success, so callers
// do not receive unexpected short or oversized write counts due to secret
// replacement length differences.
type redactor struct {
	w       io.Writer
	secrets [][]byte
}

func newRedactor(w io.Writer, secrets [][]byte) io.Writer {
	if w == nil || len(secrets) == 0 {
		return w
	}
	return &redactor{w: w, secrets: secrets}
}

func (r *redactor) Write(p []byte) (int, error) {
	out := p
	for _, secret := range r.secrets {
		if len(secret) == 0 {
			continue
		}
		out = bytes.ReplaceAll(out, secret, []byte("<redacted>"))
	}
	if _, err := r.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// scrubCommandError returns err with any *opentofu.CommandError's Stderr
// field scrubbed of every secret in secrets, in place.
func scrubCommandError(err error, secrets [][]byte) error {
	var cmdErr *opentofu.CommandError
	if !errors.As(err, &cmdErr) {
		return err
	}
	scrubbed := []byte(cmdErr.Stderr)
	for _, secret := range secrets {
		if len(secret) == 0 {
			continue
		}
		scrubbed = bytes.ReplaceAll(scrubbed, secret, []byte("<redacted>"))
	}
	cmdErr.Stderr = string(scrubbed)
	return err
}
