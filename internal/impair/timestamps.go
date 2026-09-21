package impair

import (
	"math/rand"
	"time"
)

// Timestamps shifts what is written on the wire without touching what the pacer used, so
// an A/V desync or an unstable encoder cadence shows up in the stream while the packets
// still leave at their real-time instants.
type Timestamps struct {
	Output  time.Duration // --output-ts-offset, every track
	Audio   time.Duration // --audio-offset
	Video   time.Duration // --video-offset
	Cadence time.Duration // --cadence-jitter, video only: a real encoder's audio stays steady
	Rand    *rand.Rand
}

// Active reports whether anything would actually move.
func (t *Timestamps) Active() bool {
	return t.Output != 0 || t.Audio != 0 || t.Video != 0 || t.Cadence > 0
}

func (t *Timestamps) Process(p *Packet, next Emit) error {
	p.Shift += t.Output
	switch p.Track.Kind {
	case Audio:
		p.Shift += t.Audio
	case Video:
		p.Shift += t.Video
		if t.Cadence > 0 {
			p.Shift += time.Duration((t.Rand.Float64()*2 - 1) * float64(t.Cadence))
		}
	}
	return next(p)
}

func (t *Timestamps) Flush(Emit) error { return nil }
