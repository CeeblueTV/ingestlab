// Package impair is the chain a packet walks between the pacer and the muxer: metadata
// injection, lifecycle faults, per-track holds, burst delivery, timestamp shifts, and the
// single place timestamps are converted to the output timebase.
package impair

import (
	"errors"
	"time"

	"github.com/asticode/go-astiav"
)

// ErrCrash is returned when --crash-after fires. The run drops the connection without
// writing a trailer, which is the point: the server sees a publisher that died.
var ErrCrash = errors.New("crashed on purpose")

// Kind is a track's media type, spelled the way the CLI spells it.
type Kind string

const (
	Video    Kind = "video"
	Audio    Kind = "audio"
	Subtitle Kind = "subtitle"
	Data     Kind = "data"
)

func KindOf(mt astiav.MediaType) Kind {
	switch mt {
	case astiav.MediaTypeVideo:
		return Video
	case astiav.MediaTypeAudio:
		return Audio
	case astiav.MediaTypeSubtitle:
		return Subtitle
	default:
		return Data
	}
}

// Track is the per-stream side table. Nothing of ours is ever attached to a libav object.
type Track struct {
	Kind     Kind
	In       *astiav.Stream // nil for a track we inject ourselves
	Out      *astiav.Stream
	TimeBase astiav.Rational // timebase the packets carry while in the chain

	End time.Duration // media time just past the last packet read, anchors the next loop

	lastOut int64 // last dts written, in Out's timebase
	written bool
}

func (t *Track) Injected() bool { return t.In == nil }

// Packet is one unit flowing through the chain.
//
// Timestamps stay in Track.TimeBase all the way down; Media and Shift carry the exact
// durations the stages reason about, so nothing is ever rounded to milliseconds before the
// Writer converts once at the end.
type Packet struct {
	Pkt   *astiav.Packet
	Track *Track
	Media time.Duration // media time of the dts, continuous across loops
	Shift time.Duration // wire-only timestamp shift, applied by the Writer

	owned bool // the chain, not the read loop, owns Pkt
}

// Borrow wraps the read loop's reusable packet. The chain may read it but must Retain
// before keeping it.
func Borrow(pkt *astiav.Packet, t *Track, media time.Duration) *Packet {
	return &Packet{Pkt: pkt, Track: t, Media: media}
}

// Own wraps a packet the chain allocated, such as an injected metadata packet.
func Own(pkt *astiav.Packet, t *Track, media time.Duration) *Packet {
	return &Packet{Pkt: pkt, Track: t, Media: media, owned: true}
}

// Retain makes the packet safe to hold past the current read. It is a reference, not a copy
// of the payload.
func (p *Packet) Retain() {
	if !p.owned {
		p.Pkt = p.Pkt.Clone()
		p.owned = true
	}
}

// Release frees the packet if the chain owns it. Every stage that swallows a packet must
// call it; the chain's tail does it for everything that reaches the end.
func (p *Packet) Release() {
	if p.owned {
		p.Pkt.Free()
		p.Pkt, p.owned = nil, false
	}
}

// Emit hands a packet to the next stage.
type Emit func(*Packet) error

// Stage transforms the flow. A stage either forwards a packet, swallows it (and releases
// it), or holds it for a later burst.
type Stage interface {
	Process(p *Packet, next Emit) error
	// Flush releases whatever is buffered. Called at loop boundaries and on shutdown, so a
	// held packet is never delivered after the packet that should follow it.
	Flush(next Emit) error
}

// Chain runs the stages in order.
type Chain struct {
	stages []Stage
	emits  []Emit // emits[i] enters stage i; the last one ends the chain
}

func NewChain(stages ...Stage) *Chain {
	c := &Chain{stages: stages, emits: make([]Emit, len(stages)+1)}
	c.emits[len(stages)] = func(p *Packet) error { p.Release(); return nil }
	for i := len(stages) - 1; i >= 0; i-- {
		stage, next := stages[i], c.emits[i+1]
		c.emits[i] = func(p *Packet) error { return stage.Process(p, next) }
	}
	return c
}

func (c *Chain) Process(p *Packet) error { return c.emits[0](p) }

// Flush drains every stage, each into the stages below it.
func (c *Chain) Flush() error {
	for i, s := range c.stages {
		if err := s.Flush(c.emits[i+1]); err != nil {
			return err
		}
	}
	return nil
}

// burst sends buffered packets downstream in order and empties the buffer.
func burst(buf []*Packet, next Emit) error {
	for i, p := range buf {
		if err := next(p); err != nil {
			for _, rest := range buf[i+1:] {
				rest.Release()
			}
			return err
		}
	}
	return nil
}

// ticks converts a duration to a timebase, rounding once.
func ticks(d time.Duration, tb astiav.Rational) int64 {
	if tb.Num() == 0 {
		return 0
	}
	return astiav.RescaleQ(int64(d), astiav.NewRational(1, int(time.Second)), tb)
}
