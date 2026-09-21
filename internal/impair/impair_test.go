package impair

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/asticode/go-astiav"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// ms keeps the test tables readable.
func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// sent is what a stage handed downstream, flattened so tests can compare sequences.
type sent struct {
	Kind  Kind
	Media time.Duration
	Shift time.Duration
	Dts   int64
	Size  int
}

type collector struct{ got []sent }

func (c *collector) emit(p *Packet) error {
	c.got = append(c.got, sent{p.Track.Kind, p.Media, p.Shift, p.Pkt.Dts(), p.Pkt.Size()})
	p.Release()
	return nil
}

func (c *collector) medias() []time.Duration {
	out := make([]time.Duration, len(c.got))
	for i, s := range c.got {
		out[i] = s.Media
	}
	return out
}

func (c *collector) of(k Kind) []time.Duration {
	var out []time.Duration
	for _, s := range c.got {
		if s.Kind == k {
			out = append(out, s.Media)
		}
	}
	return out
}

// milli is the timebase the tests use, so a dts reads as its media time in milliseconds.
var milli = astiav.NewRational(1, 1000)

func newTrack(k Kind) *Track { return &Track{Kind: k, TimeBase: milli, In: &astiav.Stream{}} }

// packet builds an owned packet so the chain frees it, keeping the tests leak-free.
func packet(t *testing.T, tr *Track, media time.Duration, payload ...byte) *Packet {
	t.Helper()
	if len(payload) == 0 {
		payload = []byte{0x01, 0x02, 0x03, 0x04}
	}
	pkt := astiav.AllocPacket()
	if err := pkt.FromData(payload); err != nil {
		t.Fatalf("building packet: %v", err)
	}
	pkt.SetDts(media.Milliseconds())
	pkt.SetPts(media.Milliseconds())
	return Own(pkt, tr, media)
}

func equal(t *testing.T, name string, got, want []time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d entries %v, want %d %v", name, len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
	}
}
