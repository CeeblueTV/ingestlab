package impair

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CeeblueTV/ingestlab/internal/events"
)

// feed walks a second-by-second audio+video timeline through the hold and returns what
// came out the other side.
func feed(t *testing.T, h *Hold, until time.Duration) *collector {
	t.Helper()
	audio, video := newTrack(Audio), newTrack(Video)
	c := &collector{}
	for at := time.Duration(0); at <= until; at += time.Second {
		for _, tr := range []*Track{audio, video} {
			if err := h.Process(packet(t, tr, at), c.emit); err != nil {
				t.Fatalf("processing at %v: %v", at, err)
			}
		}
	}
	return c
}

func newHold(tracks ...Kind) *Hold {
	set := map[Kind]bool{}
	for _, k := range tracks {
		set[k] = true
	}
	return &Hold{Tracks: set, Rand: rand.New(rand.NewSource(1)), Log: discard}
}

// A periodic hold freezes the chosen track for its duration and bursts what piled up,
// while everything else keeps flowing.
func TestHoldEvery(t *testing.T) {
	h := newHold(Video)
	h.Duration, h.Every = 10*time.Second, 30*time.Second
	c := feed(t, h, 45*time.Second)

	// Audio is untouched.
	var wantAudio []time.Duration
	for at := time.Duration(0); at <= 45*time.Second; at += time.Second {
		wantAudio = append(wantAudio, at)
	}
	equal(t, "audio", c.of(Audio), wantAudio)

	// Video is complete too, but late: 30s..39s arrive together once the hold ends.
	equal(t, "video", c.of(Video), wantAudio)

	// The burst goes out ahead of the packet that closed the hold, in order and back to back.
	var burstAt int
	for i, s := range c.got {
		if s.Kind == Video && s.Media == 30*time.Second {
			burstAt = i
			break
		}
	}
	if burstAt == 0 {
		t.Fatal("no video packet at 30s came out")
	}
	if prev := c.got[burstAt-1]; prev.Kind != Audio || prev.Media != 39*time.Second {
		t.Fatalf("burst should follow audio at 39s, follows %v at %v", prev.Kind, prev.Media)
	}
	for i := range 10 {
		got := c.got[burstAt+i]
		if got.Kind != Video || got.Media != time.Duration(30+i)*time.Second {
			t.Fatalf("burst entry %d is %v at %v", i, got.Kind, got.Media)
		}
	}
	if after := c.got[burstAt+10]; after.Kind != Audio || after.Media != 40*time.Second {
		t.Fatalf("audio at 40s should follow the burst, got %v at %v", after.Kind, after.Media)
	}
}

// Held packets keep their timestamps: the track lags, it does not shift.
func TestHoldKeepsTimestamps(t *testing.T) {
	h := newHold(Video)
	h.Duration, h.Every = 5*time.Second, 10*time.Second
	c := feed(t, h, 20*time.Second)

	for _, s := range c.got {
		if s.Shift != 0 {
			t.Fatalf("%v at %v carries a shift of %v", s.Kind, s.Media, s.Shift)
		}
		if s.Dts != s.Media.Milliseconds() {
			t.Fatalf("%v at %v was written with dts %d", s.Kind, s.Media, s.Dts)
		}
	}
}

// --hold-at replaces the periodic schedule and is not replayed afterwards.
func TestHoldAt(t *testing.T) {
	h := newHold(Video)
	h.Duration = 5 * time.Second
	h.At = []time.Duration{20 * time.Second, 8 * time.Second} // deliberately out of order
	c := feed(t, h, 40*time.Second)

	// Both holds happened, and nothing was lost.
	if got := len(c.of(Video)); got != 41 {
		t.Fatalf("got %d video packets, want 41", got)
	}
	// After the last entry the schedule is exhausted: 30s onwards is delivered in step.
	for i, s := range c.got {
		if s.Kind == Video && s.Media >= 30*time.Second {
			if prev := c.got[i-1]; prev.Kind != Audio || prev.Media != s.Media {
				t.Fatalf("video at %v is still being held (follows %v at %v)", s.Media, prev.Kind, prev.Media)
			}
		}
	}
}

// --hold-duration 0 freezes for good and drops, rather than buffering without bound.
func TestHoldForever(t *testing.T) {
	h := newHold(Video)
	h.Duration, h.Every = 0, 10*time.Second
	if !h.Forever() {
		t.Fatal("a zero duration should hold for good")
	}
	c := feed(t, h, 30*time.Second)

	var wantVideo []time.Duration
	for at := time.Duration(0); at < 10*time.Second; at += time.Second {
		wantVideo = append(wantVideo, at)
	}
	equal(t, "video", c.of(Video), wantVideo)
	if got := len(c.of(Audio)); got != 31 {
		t.Fatalf("audio should keep flowing, got %d of 31 packets", got)
	}
}

// A sparse track must not keep a hold open past its end just because it sent nothing.
func TestHoldEndsOnAnyTrack(t *testing.T) {
	h := newHold(Data)
	h.Duration, h.Every = 5*time.Second, 10*time.Second
	audio, data := newTrack(Audio), newTrack(Data)
	c := &collector{}

	// One data packet at 10s starts the hold; audio alone runs through its end.
	if err := h.Process(packet(t, data, 10*time.Second), c.emit); err != nil {
		t.Fatal(err)
	}
	for at := 10 * time.Second; at <= 20*time.Second; at += time.Second {
		if err := h.Process(packet(t, audio, at), c.emit); err != nil {
			t.Fatal(err)
		}
	}
	if h.holding {
		t.Fatal("the hold is still open after its end passed on another track")
	}
	equal(t, "data", c.of(Data), []time.Duration{10 * time.Second})
}

// Flush delivers what is held, so looping never puts a stale packet after a fresh one.
func TestHoldFlush(t *testing.T) {
	h := newHold(Video)
	h.Duration, h.Every = 30*time.Second, 5*time.Second
	c := feed(t, h, 10*time.Second)
	if got := len(c.of(Video)); got != 5 {
		t.Fatalf("got %d video packets before the flush, want 5", got)
	}
	if err := h.Flush(c.emit); err != nil {
		t.Fatal(err)
	}
	if got := len(c.of(Video)); got != 11 {
		t.Fatalf("got %d video packets after the flush, want 11", got)
	}
}

// A random hold length stays inside its window and is reproducible from the seed.
func TestHoldRandomDuration(t *testing.T) {
	lengths := map[time.Duration]bool{}
	for range 20 {
		h := newHold(Video)
		h.Duration, h.Min = 10*time.Second, 2*time.Second
		d := h.duration()
		if d < h.Min || d >= h.Duration {
			t.Fatalf("hold length %v is outside [%v, %v)", d, h.Min, h.Duration)
		}
		lengths[d] = true
	}
	if len(lengths) != 1 {
		t.Fatalf("the same seed drew %d different lengths", len(lengths))
	}
}

func newInterrupt(tracks ...Kind) *Hold {
	h := newHold(tracks...)
	h.Drop = true
	return h
}

// An interrupt cuts the chosen track for its window and lets it through again afterwards.
// Nothing is buffered: the track comes back where the media clock has got to, leaving a
// hole in its timeline, while everything else keeps flowing in step.
func TestInterruptDropsTheWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	ev, err := events.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := newInterrupt(Audio)
	h.Duration, h.At, h.Events = 20*time.Second, []time.Duration{20 * time.Second}, ev
	c := feed(t, h, 60*time.Second)

	var wantAudio, wantVideo []time.Duration
	for at := time.Duration(0); at <= 60*time.Second; at += time.Second {
		if at < 20*time.Second || at >= 40*time.Second {
			wantAudio = append(wantAudio, at)
		}
		wantVideo = append(wantVideo, at)
	}
	equal(t, "audio", c.of(Audio), wantAudio)
	equal(t, "video", c.of(Video), wantVideo)

	if len(h.buf) != 0 || h.holding {
		t.Fatalf("the interrupt kept %d packets, holding=%v", len(h.buf), h.holding)
	}
	// Delivered in step, and with the timestamps it was read with.
	for i, s := range c.got {
		if i > 0 && s.Media < c.got[i-1].Media {
			t.Fatalf("%v at %v follows %v at %v", s.Kind, s.Media, c.got[i-1].Kind, c.got[i-1].Media)
		}
		if s.Shift != 0 || s.Dts != s.Media.Milliseconds() {
			t.Fatalf("%v at %v was rewritten: shift %v, dts %d", s.Kind, s.Media, s.Shift, s.Dts)
		}
	}

	// The event log states what was cut, for whoever reads the stream back.
	if err := ev.Close(); err != nil {
		t.Fatal(err)
	}
	logged := readEvents(t, path)
	if len(logged) != 2 || logged[0]["kind"] != events.InterruptStart || logged[1]["kind"] != events.InterruptEnd {
		t.Fatalf("want one interrupt.start and one interrupt.end, got %v", logged)
	}
	if got := logged[0]["t_media"]; got != 20.0 {
		t.Fatalf("the interrupt started at %v, want 20", got)
	}
	end := logged[1]
	if end["t_media"] != 40.0 || end["dropped"] != 20.0 || end["interrupted_seconds"] != 20.0 {
		t.Fatalf("interrupt.end is %v, want 20 packets dropped over 20s ending at 40", end)
	}
}

// A loop flushes the chain, but an interrupt has nothing to deliver: its window runs on
// across the loop, as the media clock does, and ends where it was scheduled to.
func TestInterruptRunsAcrossFlush(t *testing.T) {
	h := newInterrupt(Audio)
	h.Duration, h.At = 10*time.Second, []time.Duration{5 * time.Second}
	audio, video := newTrack(Audio), newTrack(Video)
	c := &collector{}
	run := func(from, until time.Duration) {
		for at := from; at <= until; at += time.Second {
			for _, tr := range []*Track{audio, video} {
				if err := h.Process(packet(t, tr, at), c.emit); err != nil {
					t.Fatalf("processing at %v: %v", at, err)
				}
			}
		}
	}

	run(0, 8*time.Second)
	before := len(c.got)
	if err := h.Flush(c.emit); err != nil {
		t.Fatal(err)
	}
	if len(c.got) != before || !h.holding {
		t.Fatalf("the flush ended the interrupt or delivered %d packets", len(c.got)-before)
	}
	run(9*time.Second, 20*time.Second)

	var want []time.Duration
	for at := time.Duration(0); at <= 20*time.Second; at += time.Second {
		if at < 5*time.Second || at >= 15*time.Second {
			want = append(want, at)
		}
	}
	equal(t, "audio", c.of(Audio), want)
	if got := len(c.of(Video)); got != 21 {
		t.Fatalf("video should keep flowing, got %d of 21 packets", got)
	}
}

// --interrupt-duration 0 cuts for good, like --hold-duration 0, without logging every drop.
func TestInterruptForever(t *testing.T) {
	ev, err := events.Open("")
	if err != nil {
		t.Fatal(err)
	}
	h := newInterrupt(Audio)
	h.Duration, h.At, h.Events = 0, []time.Duration{10 * time.Second}, ev
	c := feed(t, h, 30*time.Second)

	var want []time.Duration
	for at := time.Duration(0); at < 10*time.Second; at += time.Second {
		want = append(want, at)
	}
	equal(t, "audio", c.of(Audio), want)
	if got := len(c.of(Video)); got != 31 {
		t.Fatalf("video should keep flowing, got %d of 31 packets", got)
	}
	counts := ev.Summarise().Events
	if counts[events.InterruptStart] != 1 || counts[events.HoldDrop] != 0 || counts[events.InterruptEnd] != 0 {
		t.Fatalf("unexpected events %v", counts)
	}
}

// readEvents parses an events.jsonl back into one map per line.
func readEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parsing %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}
