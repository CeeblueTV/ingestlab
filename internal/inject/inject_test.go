package inject

import (
	"strings"
	"testing"
	"time"
)

// due is the whole injection schedule, so it is worth pinning on its own.
func fire(e *emitter, from, to, step time.Duration) []time.Duration {
	var out []time.Duration
	for at := from; at <= to; at += step {
		if e.due(at) {
			out = append(out, at)
			e.nextAt = at + e.interval
		}
	}
	return out
}

func TestIntervalSchedule(t *testing.T) {
	e := &emitter{interval: time.Second}
	got := fire(e, 0, 5*time.Second, 200*time.Millisecond)
	want := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second}
	equalDurations(t, "emissions", got, want)
}

// --interval 0 keeps the track and emits nothing, for every track type.
func TestZeroIntervalKeepsTrackSilent(t *testing.T) {
	e := &emitter{interval: 0}
	if got := fire(e, 0, 10*time.Second, time.Second); len(got) != 0 {
		t.Fatalf("a zero interval emitted %d packets", len(got))
	}
}

// --start-meta-after pushes the first emission out, and applies to every track type.
func TestStartAfter(t *testing.T) {
	e := &emitter{interval: time.Second, startAfter: 3 * time.Second}
	got := fire(e, 0, 5*time.Second, 500*time.Millisecond)
	want := []time.Duration{3 * time.Second, 4 * time.Second, 5 * time.Second}
	equalDurations(t, "emissions", got, want)
}

// The schedule anchors on the first packet, not on zero, so a source that starts at an
// offset still emits one interval in.
func TestScheduleAnchorsOnFirstPacket(t *testing.T) {
	e := &emitter{interval: time.Second}
	got := fire(e, 10*time.Second, 13*time.Second, 500*time.Millisecond)
	want := []time.Duration{11 * time.Second, 12 * time.Second, 13 * time.Second}
	equalDurations(t, "emissions", got, want)
}

// Breaks return --scte-duration after they open, so cues closer than that would overlap.
func TestSCTEIntervalAgainstDuration(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		wantFail bool
	}{
		{"shorter", Config{Types: []Type{SCTE35}, Interval: time.Second, SCTEDuration: 4 * time.Second}, true},
		{"back to back", Config{Types: []Type{SCTE35}, Interval: 4 * time.Second, SCTEDuration: 4 * time.Second}, false},
		{"longer", Config{Types: []Type{SCTE35}, Interval: 30 * time.Second, SCTEDuration: 10 * time.Second}, false},
		{"silent track", Config{Types: []Type{SCTE35}, Interval: 0, SCTEDuration: 4 * time.Second}, false},
		{"no scte track", Config{Types: []Type{Metadata}, Interval: time.Second, SCTEDuration: 4 * time.Second}, false},
	}
	for _, c := range cases {
		err := c.cfg.Validate()
		if c.wantFail && err == nil {
			t.Errorf("%s: should have been refused", c.name)
		}
		if !c.wantFail && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestParseTypes(t *testing.T) {
	ok := map[string][]Type{
		"metadata":        {Metadata},
		"metadata,scte35": {Metadata, SCTE35},
		"none":            nil,
		"id3,subtitles":   {ID3, Subtitles},
	}
	for in, want := range ok {
		got, err := ParseTypes(split(in))
		if err != nil {
			t.Errorf("ParseTypes(%q): %v", in, err)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("ParseTypes(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("ParseTypes(%q) = %v, want %v", in, got, want)
			}
		}
	}

	for _, in := range []string{"none,metadata", "bogus", "metadata,bogus"} {
		if _, err := ParseTypes(split(in)); err == nil {
			t.Errorf("ParseTypes(%q) should have failed", in)
		}
	}
}

func split(s string) []string { return strings.Split(s, ",") }

func equalDurations(t *testing.T, name string, got, want []time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", name, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
	}
}
