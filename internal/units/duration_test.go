package units

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	ok := []struct {
		in   string
		want time.Duration
	}{
		{"0", 0},
		{"1500", 1500 * time.Millisecond}, // bare number stays milliseconds
		{"1500ms", 1500 * time.Millisecond},
		{"10s", 10 * time.Second},
		{"2.5m", 150 * time.Second},
		{"1h", time.Hour},
		{" 30S ", 30 * time.Second},
	}
	for _, c := range ok {
		got, err := ParseDuration(c.in)
		if err != nil {
			t.Errorf("ParseDuration(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", c.in, got, c.want)
		}
	}

	for _, in := range []string{"", "s", "10x", "abc", "10 s"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) should have failed", in)
		}
	}
}

// The units the old tool expressed in seconds must not silently change meaning.
func TestBareNumberIsMilliseconds(t *testing.T) {
	got, err := ParseDuration("10000")
	if err != nil {
		t.Fatal(err)
	}
	if got != 10*time.Second {
		t.Fatalf("got %v, want 10s", got)
	}
}
