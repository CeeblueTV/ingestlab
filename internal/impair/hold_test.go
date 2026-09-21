package impair

import (
	"math/rand"
	"testing"
	"time"
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
