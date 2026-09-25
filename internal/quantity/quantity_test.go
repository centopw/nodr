package quantity

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"pgregory.net/rapid"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in    string
		bytes int64
	}{
		{"0", 0},
		{"1024", 1024},
		{"1Ki", KiB},
		{"512Mi", 512 * MiB},
		{"4Gi", 4 * GiB},
		{"1.5Gi", 1536 * MiB},
		{"2Ti", 2 * TiB},
		{"1Pi", PiB},
		{"2k", 2000},
		{"2G", 2_000_000_000},
		{"0.5k", 500},
		{"1.25Ki", 1280},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			q, err := Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.in, err)
			}
			if q.Bytes() != tt.bytes {
				t.Errorf("Parse(%q).Bytes() = %d, want %d", tt.in, q.Bytes(), tt.bytes)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		in      string
		inexact bool
	}{
		{"", false},
		{"Gi", false},
		{"-1Gi", false},
		{"4 Gi", false},
		{"4gi", false},
		{"4K", false},
		{"1.Gi", false},
		{".5Gi", false},
		{"4Gib", false},
		{"0.1", true},
		{"0.3Ki", true},
		{"9000Pi", false},
		{"100000000000000000000", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, err := Parse(tt.in)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want error", tt.in)
			}
			if got := errors.Is(err, ErrInexact); got != tt.inexact {
				t.Errorf("errors.Is(err, ErrInexact) = %v, want %v (err: %v)", got, tt.inexact, err)
			}
		})
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{1023, "1023"},
		{KiB, "1Ki"},
		{1536 * MiB, "1536Mi"},
		{4 * GiB, "4Gi"},
		{3 * TiB, "3Ti"},
		{2 * PiB, "2Pi"},
		{2_000_000_000, "1953125Ki"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := (Quantity{bytes: tt.bytes}).String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIn(t *testing.T) {
	q := MustParse("4Gi")
	if got, err := q.In(MiB); err != nil || got != 4096 {
		t.Errorf("In(MiB) = %d, %v; want 4096, nil", got, err)
	}
	if got, err := q.In(GiB); err != nil || got != 4 {
		t.Errorf("In(GiB) = %d, %v; want 4, nil", got, err)
	}
	if _, err := MustParse("1536Mi").In(GiB); !errors.Is(err, ErrInexact) {
		t.Errorf("In(GiB) of 1536Mi: err = %v, want ErrInexact", err)
	}
	if _, err := q.In(0); err == nil {
		t.Error("In(0) succeeded, want error")
	}
}

func TestFromUnits(t *testing.T) {
	q, err := FromUnits(8192, MiB)
	if err != nil || q.String() != "8Gi" {
		t.Errorf("FromUnits(8192, MiB) = %v, %v; want 8Gi", q, err)
	}
	if _, err := FromUnits(-1, MiB); err == nil {
		t.Error("FromUnits(-1, MiB) succeeded, want error")
	}
	if _, err := FromUnits(math.MaxInt64, KiB); err == nil {
		t.Error("FromUnits overflow succeeded, want error")
	}
}

func TestTextRoundTrip(t *testing.T) {
	type doc struct {
		Size Quantity `json:"size"`
	}
	var d doc
	if err := json.Unmarshal([]byte(`{"size":"1.5Gi"}`), &d); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"size":"1536Mi"}` {
		t.Errorf("Marshal = %s", out)
	}
	if err := json.Unmarshal([]byte(`{"size":"lots"}`), &d); err == nil {
		t.Error("Unmarshal of invalid quantity succeeded")
	}
}

func TestStringParseRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q := Quantity{bytes: rapid.Int64Range(0, math.MaxInt64).Draw(t, "bytes")}
		parsed, err := Parse(q.String())
		if err != nil {
			t.Fatalf("Parse(%q): %v", q.String(), err)
		}
		if parsed != q {
			t.Fatalf("Parse(String()) = %d bytes, want %d", parsed.Bytes(), q.Bytes())
		}
	})
}
