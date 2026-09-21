package units

import (
	"strings"
	"time"
)

// StringList is a comma-separated list flag, lowercased and trimmed.
type StringList struct {
	Values  []string
	Present bool
}

func (l *StringList) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(l.Values, ",")
}

func (l *StringList) Set(s string) error {
	l.Present = true
	for _, item := range strings.Split(s, ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			l.Values = append(l.Values, item)
		}
	}
	return nil
}

func (l *StringList) Has(v string) bool {
	for _, item := range l.Values {
		if item == v {
			return true
		}
	}
	return false
}

// DurationList is a comma-separated list of durations, e.g. "20s,60s,2m".
type DurationList struct {
	Values  []time.Duration
	Present bool
}

func (l *DurationList) String() string {
	if l == nil {
		return ""
	}
	parts := make([]string, len(l.Values))
	for i, v := range l.Values {
		parts[i] = v.String()
	}
	return strings.Join(parts, ",")
}

func (l *DurationList) Set(s string) error {
	l.Present = true
	for _, item := range strings.Split(s, ",") {
		if strings.TrimSpace(item) == "" {
			continue
		}
		v, err := ParseDuration(item)
		if err != nil {
			return err
		}
		l.Values = append(l.Values, v)
	}
	return nil
}
