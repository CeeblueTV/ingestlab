package impair

import (
	"math/rand"
	"testing"
	"time"

	"github.com/asticode/go-astiav"

	"github.com/CeeblueTV/ingestlab/internal/events"
)

// An offset is a wire-only shift: it never moves the media time the pacer runs on.
func TestOffsetsAreWireOnly(t *testing.T) {
	ts := &Timestamps{Audio: -250 * time.Millisecond, Video: 100 * time.Millisecond}
	c := &collector{}
	for _, k := range []Kind{Audio, Video, Data} {
		if err := ts.Process(packet(t, newTrack(k), time.Second), c.emit); err != nil {
			t.Fatal(err)
		}
	}
	want := []time.Duration{-250 * time.Millisecond, 100 * time.Millisecond, 0}
	for i, s := range c.got {
		if s.Shift != want[i] {
			t.Fatalf("%v: shift %v, want %v", s.Kind, s.Shift, want[i])
		}
		if s.Media != time.Second {
			t.Fatalf("%v: media moved to %v", s.Kind, s.Media)
		}
	}
}

// Cadence jitter wobbles video only, inside its window: a real unstable encoder still
// emits audio on a steady clock.
func TestCadenceJitter(t *testing.T) {
	ts := &Timestamps{Cadence: 60 * time.Millisecond, Rand: rand.New(rand.NewSource(3))}
	c := &collector{}
	audio, video := newTrack(Audio), newTrack(Video)
	for i := range 200 {
		if err := ts.Process(packet(t, video, ms(i*40)), c.emit); err != nil {
			t.Fatal(err)
		}
		if err := ts.Process(packet(t, audio, ms(i*40)), c.emit); err != nil {
			t.Fatal(err)
		}
	}
	var moved int
	for _, s := range c.got {
		switch s.Kind {
		case Audio:
			if s.Shift != 0 {
				t.Fatalf("audio was wobbled by %v", s.Shift)
			}
		case Video:
			if s.Shift < -ts.Cadence || s.Shift > ts.Cadence {
				t.Fatalf("wobble %v is outside +/-%v", s.Shift, ts.Cadence)
			}
			if s.Shift != 0 {
				moved++
			}
		}
	}
	if moved == 0 {
		t.Fatal("no video timestamp moved at all")
	}
}

// --output-ts-offset shifts every track, injected ones included.
func TestOutputOffsetAppliesEverywhere(t *testing.T) {
	ts := &Timestamps{Output: 2 * time.Second, Audio: time.Second}
	c := &collector{}
	for _, tr := range []*Track{newTrack(Audio), newTrack(Video), {Kind: Data, TimeBase: milli}} {
		if err := ts.Process(packet(t, tr, 0), c.emit); err != nil {
			t.Fatal(err)
		}
	}
	equal(t, "shifts", []time.Duration{c.got[0].Shift, c.got[1].Shift, c.got[2].Shift},
		[]time.Duration{3 * time.Second, 2 * time.Second, 2 * time.Second})
}

// newWriter builds a real output stream, which is the only thing the writer needs from a muxer.
func newWriter(t *testing.T, tb astiav.Rational) (*Writer, *Track, *[]int64) {
	t.Helper()
	fc, err := astiav.AllocOutputFormatContext(nil, "mpegts", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fc.Free)
	out := fc.NewStream(nil)
	out.SetTimeBase(tb)

	var written []int64
	w := &Writer{Log: discard, Events: (*events.Log)(nil),
		Write: func(p *astiav.Packet) error { written = append(written, p.Dts()); return nil }}
	return w, &Track{Kind: Video, In: &astiav.Stream{}, Out: out, TimeBase: milli}, &written
}

// The writer is the one conversion to the output timebase, and it folds in the loop offset.
func TestWriterRescalesAndShifts(t *testing.T) {
	w, track, written := newWriter(t, astiav.NewRational(1, 90000))
	w.Base = 10 * time.Second

	p := packet(t, track, time.Second)
	p.Shift = 500 * time.Millisecond
	if err := w.Process(p, func(p *Packet) error { p.Release(); return nil }); err != nil {
		t.Fatal(err)
	}
	// 1s of media, plus a 10s loop offset and a 500 ms shift, at 90 kHz.
	if want := int64(11500 * 90); (*written)[0] != want {
		t.Fatalf("wrote dts %d, want %d", (*written)[0], want)
	}
}

// A wobble can push a dts backwards; the muxer would reject that, so it is clamped.
func TestWriterClampsMonotonic(t *testing.T) {
	w, track, written := newWriter(t, milli)
	tail := func(p *Packet) error { p.Release(); return nil }

	for _, shift := range []time.Duration{0, -500 * time.Millisecond} {
		p := packet(t, track, time.Second)
		p.Shift = shift
		if err := w.Process(p, tail); err != nil {
			t.Fatal(err)
		}
	}
	if got := *written; got[1] <= got[0] {
		t.Fatalf("dts went backwards: %d then %d", got[0], got[1])
	}
}
