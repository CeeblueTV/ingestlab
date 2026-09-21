package impair

import (
	"math/rand"
	"testing"
	"time"
)

// clock is a wall clock the test moves by hand, so a jitter window is exact instead of racy.
type clock struct{ at time.Time }

func (c *clock) now() time.Time          { return c.at }
func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

// Packets inside a window are buffered and leave together at the end of it, with no quiet
// gap: the next window opens immediately.
func TestJitterBursts(t *testing.T) {
	clk := &clock{at: time.Unix(0, 0)}
	j := &Jitter{Window: 200 * time.Millisecond, Rand: rand.New(rand.NewSource(1)),
		Now: clk.now, Log: discard}
	video := newTrack(Video)
	c := &collector{}

	// Eight packets 40 ms apart span more than one 200 ms window.
	for i := range 8 {
		at := time.Duration(i) * 40 * time.Millisecond
		clk.at = time.Unix(0, 0).Add(at)
		if err := j.Process(packet(t, video, at), c.emit); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.got) == 0 {
		t.Fatal("nothing was delivered across two windows")
	}
	if len(c.got) == 8 {
		t.Fatal("every packet went straight out, nothing was buffered")
	}
	// Whatever came out came out in order.
	for i := 1; i < len(c.got); i++ {
		if c.got[i].Media < c.got[i-1].Media {
			t.Fatalf("burst is out of order at %d: %v after %v", i, c.got[i].Media, c.got[i-1].Media)
		}
	}
	// Flush accounts for the rest: nothing is lost.
	if err := j.Flush(c.emit); err != nil {
		t.Fatal(err)
	}
	if len(c.got) != 8 {
		t.Fatalf("got %d packets after the flush, want 8", len(c.got))
	}
}

// A zero window is a straight passthrough, allocating nothing.
func TestJitterDisabled(t *testing.T) {
	j := &Jitter{Log: discard}
	c := &collector{}
	video := newTrack(Video)
	for i := range 4 {
		if err := j.Process(packet(t, video, ms(i*40)), c.emit); err != nil {
			t.Fatal(err)
		}
	}
	equal(t, "video", c.medias(), []time.Duration{0, ms(40), ms(80), ms(120)})
}

// The window is drawn from [min, max], and from the seed, so a run reproduces.
func TestJitterWindowRange(t *testing.T) {
	clk := &clock{at: time.Unix(0, 0)}
	j := &Jitter{Window: 800 * time.Millisecond, Min: 100 * time.Millisecond,
		Rand: rand.New(rand.NewSource(7)), Now: clk.now, Log: discard}
	video := newTrack(Video)
	c := &collector{}

	for range 20 {
		start := clk.at
		if err := j.Process(packet(t, video, 0), c.emit); err != nil {
			t.Fatal(err)
		}
		if window := j.end.Sub(start); window < j.Min || window > j.Window {
			t.Fatalf("window %v is outside [%v, %v]", window, j.Min, j.Window)
		}
		clk.advance(time.Second) // past the window, so the next call opens a new one
	}
}
