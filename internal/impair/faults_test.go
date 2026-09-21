package impair

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/CeeblueTV/ingestlab/internal/units"
)

func at(d time.Duration) units.Duration { return units.Duration{Value: d, Present: true} }

// runFaults walks an audio+video second timeline through the stage.
func runFaults(t *testing.T, f *Faults, until time.Duration) (*collector, error) {
	t.Helper()
	f.Log = discard
	audio, video := newTrack(Audio), newTrack(Video)
	c := &collector{}
	for tick := time.Duration(0); tick <= until; tick += time.Second {
		for _, tr := range []*Track{audio, video} {
			if err := f.Process(packet(t, tr, tick), c.emit); err != nil {
				return c, err
			}
		}
	}
	return c, nil
}

func TestStopMediaAfter(t *testing.T) {
	c, err := runFaults(t, &Faults{StopMedia: at(5 * time.Second)}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{0, time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second}
	equal(t, "audio", c.of(Audio), want)
	equal(t, "video", c.of(Video), want)
}

// A late track is withheld entirely; the other one is untouched.
func TestStartVideoAfter(t *testing.T) {
	c, err := runFaults(t, &Faults{StartVideo: at(3 * time.Second)}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, "video", c.of(Video), []time.Duration{3 * time.Second, 4 * time.Second, 5 * time.Second})
	if got := len(c.of(Audio)); got != 6 {
		t.Fatalf("audio should be untouched, got %d of 6 packets", got)
	}
}

// Corruption replaces the video payload from its instant on, and leaves audio alone.
func TestErrorMediaAfter(t *testing.T) {
	c, err := runFaults(t, &Faults{ErrorMedia: at(2 * time.Second)}, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range c.got {
		corrupted := s.Size == len(garbage)
		want := s.Kind == Video && s.Media >= 2*time.Second
		if corrupted != want {
			t.Fatalf("%v at %v: corrupted=%v, want %v", s.Kind, s.Media, corrupted, want)
		}
	}
}

// The corrupt packet keeps every timestamp, so it still paces and muxes like the one it
// replaced — only the bytes changed.
func TestCorruptKeepsTiming(t *testing.T) {
	video := newTrack(Video)
	p := packet(t, video, 4*time.Second, 0xAA, 0xBB)
	defer p.Release()

	dts, pts := p.Pkt.Dts(), p.Pkt.Pts()
	if err := corrupt(p); err != nil {
		t.Fatal(err)
	}
	if p.Pkt.Dts() != dts || p.Pkt.Pts() != pts {
		t.Fatalf("timestamps moved: dts %d->%d, pts %d->%d", dts, p.Pkt.Dts(), pts, p.Pkt.Pts())
	}
	if !bytes.Equal(p.Pkt.Data(), garbage) {
		t.Fatalf("payload is %x, want %x", p.Pkt.Data(), garbage)
	}
}

func TestCrashAfter(t *testing.T) {
	_, err := runFaults(t, &Faults{Crash: at(3 * time.Second)}, 10*time.Second)
	if !errors.Is(err, ErrCrash) {
		t.Fatalf("got %v, want ErrCrash", err)
	}
}

// Injected metadata keeps flowing after the media stops: that asymmetry is the point of
// --stop-media-after for a track-lifecycle test.
func TestFaultsSpareInjectedTracks(t *testing.T) {
	f := &Faults{StopMedia: at(2 * time.Second), Log: discard}
	meta := &Track{Kind: Data, TimeBase: milli} // no In: an injected track
	c := &collector{}

	for tick := time.Duration(0); tick <= 5*time.Second; tick += time.Second {
		if err := f.Process(packet(t, meta, tick), c.emit); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(c.of(Data)); got != 6 {
		t.Fatalf("got %d metadata packets, want all 6", got)
	}
}
