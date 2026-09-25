// Package quantity parses, formats and converts byte sizes such as "4Gi" or
// "1536Mi". Conversions are exact: a value that cannot be represented in the
// requested unit is an error, never silently rounded (design §3.8).
package quantity

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
)

// Binary units.
const (
	KiB int64 = 1 << (10 * (iota + 1))
	MiB
	GiB
	TiB
	PiB
)

// ErrInexact reports that a value is not a whole number of the requested
// unit.
var ErrInexact = errors.New("not a whole number")

var (
	syntax = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(Ki|Mi|Gi|Ti|Pi|k|M|G|T|P)?$`)

	multipliers = map[string]int64{
		"":   1,
		"Ki": KiB,
		"Mi": MiB,
		"Gi": GiB,
		"Ti": TiB,
		"Pi": PiB,
		"k":  1e3,
		"M":  1e6,
		"G":  1e9,
		"T":  1e12,
		"P":  1e15,
	}

	// binaryUnits lists the suffixes used for canonical formatting, largest
	// first.
	binaryUnits = []struct {
		suffix string
		size   int64
	}{{"Pi", PiB}, {"Ti", TiB}, {"Gi", GiB}, {"Mi", MiB}, {"Ki", KiB}}
)

// Quantity is an exact, non-negative number of bytes. The zero value is
// zero bytes.
type Quantity struct {
	bytes int64
}

// Parse parses a quantity such as "4Gi", "1.5Gi", "512Mi", "2G" or "1024".
// Binary suffixes (Ki, Mi, Gi, Ti, Pi) are powers of 1024; decimal suffixes
// (k, M, G, T, P) are powers of 1000. The value must be a whole number of
// bytes.
func Parse(s string) (Quantity, error) {
	m := syntax.FindStringSubmatch(s)
	if m == nil {
		return Quantity{}, fmt.Errorf("invalid quantity %q: want a number with an optional suffix such as Mi, Gi or G", s)
	}
	r, ok := new(big.Rat).SetString(m[1])
	if !ok {
		return Quantity{}, fmt.Errorf("invalid quantity %q", s)
	}
	r.Mul(r, new(big.Rat).SetInt64(multipliers[m[2]]))
	if !r.IsInt() {
		return Quantity{}, fmt.Errorf("invalid quantity %q: %w of bytes", s, ErrInexact)
	}
	if !r.Num().IsInt64() {
		return Quantity{}, fmt.Errorf("invalid quantity %q: too large", s)
	}
	return Quantity{bytes: r.Num().Int64()}, nil
}

// MustParse is like Parse but panics on error. It is intended for constants
// and tests.
func MustParse(s string) Quantity {
	q, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return q
}

// FromUnits returns the quantity of n units of the given size in bytes, for
// example FromUnits(4096, MiB).
func FromUnits(n, unit int64) (Quantity, error) {
	if n < 0 || unit <= 0 {
		return Quantity{}, fmt.Errorf("invalid quantity: %d units of %d bytes", n, unit)
	}
	if n > math.MaxInt64/unit {
		return Quantity{}, fmt.Errorf("invalid quantity: %d units of %d bytes is too large", n, unit)
	}
	return Quantity{bytes: n * unit}, nil
}

// Bytes returns the quantity in bytes.
func (q Quantity) Bytes() int64 { return q.bytes }

// IsZero reports whether the quantity is zero bytes.
func (q Quantity) IsZero() bool { return q.bytes == 0 }

// In converts the quantity to a whole number of units, for example In(MiB).
// It returns an error wrapping ErrInexact if the quantity is not a whole
// number of units.
func (q Quantity) In(unit int64) (int64, error) {
	if unit <= 0 {
		return 0, fmt.Errorf("invalid unit size %d", unit)
	}
	if q.bytes%unit != 0 {
		return 0, fmt.Errorf("%s is %w of %s", q, ErrInexact, Quantity{bytes: unit})
	}
	return q.bytes / unit, nil
}

// String returns the canonical form: the largest binary unit that
// represents the value exactly, for example "4Gi" or "1536Mi", or a plain
// number of bytes.
func (q Quantity) String() string {
	if q.bytes == 0 {
		return "0"
	}
	for _, u := range binaryUnits {
		if q.bytes%u.size == 0 {
			return strconv.FormatInt(q.bytes/u.size, 10) + u.suffix
		}
	}
	return strconv.FormatInt(q.bytes, 10)
}

// MarshalText implements encoding.TextMarshaler using the canonical form.
func (q Quantity) MarshalText() ([]byte, error) {
	return []byte(q.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (q *Quantity) UnmarshalText(text []byte) error {
	parsed, err := Parse(string(text))
	if err != nil {
		return err
	}
	*q = parsed
	return nil
}
