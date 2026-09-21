// Package units parses the scalars used across the CLI and scenario files.
package units

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDuration accepts "1500", "1500ms", "10s", "2.5m", "1h".
// A bare number is milliseconds.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	num, unit := splitUnit(s)
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}

	scale, ok := map[string]time.Duration{
		"":   time.Millisecond,
		"ms": time.Millisecond,
		"s":  time.Second,
		"m":  time.Minute,
		"h":  time.Hour,
	}[unit]
	if !ok {
		return 0, fmt.Errorf("unknown duration unit %q in %q", unit, s)
	}
	return time.Duration(v * float64(scale)), nil
}

func splitUnit(s string) (num, unit string) {
	i := len(s)
	for i > 0 {
		c := s[i-1]
		if (c >= '0' && c <= '9') || c == '.' {
			break
		}
		i--
	}
	return s[:i], strings.ToLower(s[i:])
}

// Duration adapts ParseDuration to flag.Value. Present reports whether the flag was given,
// which several options need to distinguish from a zero value.
type Duration struct {
	Value   time.Duration
	Present bool
}

func (d *Duration) String() string {
	if d == nil || !d.Present {
		return ""
	}
	return d.Value.String()
}

func (d *Duration) Set(s string) error {
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	d.Value, d.Present = v, true
	return nil
}
