package impair

import (
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"time"

	"github.com/CeeblueTV/ingestlab/internal/events"
)

// Default hold schedule.
const (
	DefaultHoldDuration = 10 * time.Second
	DefaultHoldEvery    = 30 * time.Second
)

// Hold freezes the delivery of the selected tracks for a while and then bursts everything
// that piled up, while the other tracks keep flowing. The server sees one track seconds
// behind the others with its timestamps untouched — which is the whole point, and why
// nothing here rewrites a timestamp.
//
// Holds are scheduled on the media clock so a run reproduces.
type Hold struct {
	Tracks   map[Kind]bool
	Duration time.Duration   // zero or less holds for good, dropping instead of buffering
	Min      time.Duration   // when set, each hold lasts a random Min..Duration
	Every    time.Duration   // periodic schedule, used when At is empty
	At       []time.Duration // explicit start times, offsets from the first packet
	Rand     *rand.Rand

	Log    *slog.Logger
	Events *events.Log

	anchor   time.Duration
	anchored bool
	next     time.Duration
	hasNext  bool
	start    time.Duration
	end      time.Duration
	holding  bool
	index    int
	buf      []*Packet
}

// Forever reports the --hold-duration 0 mode: the held tracks never come back and their
// packets are dropped, because buffering them would grow without bound.
func (h *Hold) Forever() bool { return h.Duration <= 0 }

func (h *Hold) Process(p *Packet, next Emit) error {
	if len(h.Tracks) == 0 {
		return next(p)
	}
	if !h.anchored {
		sort.Slice(h.At, func(i, j int) bool { return h.At[i] < h.At[j] })
		h.anchor, h.anchored = p.Media, true
		h.schedule(p.Media)
	}

	// A finished hold is closed by whatever packet arrives, not by one of the held tracks:
	// a sparse track would otherwise stay frozen until its own next packet.
	if h.holding {
		if p.Media < h.end {
			if h.Tracks[p.Track.Kind] {
				return h.keep(p)
			}
			return next(p)
		}
		if err := h.release(p.Media, next); err != nil {
			return err
		}
	}

	if !h.hasNext || p.Media < h.next || !h.Tracks[p.Track.Kind] {
		return next(p)
	}

	h.start, h.holding = p.Media, true
	if h.Forever() {
		h.end = math.MaxInt64
		h.Log.Info("freezing tracks for good", "tracks", h.trackNames(), "at", p.Media)
	} else {
		h.end = p.Media + h.duration()
		h.Log.Info("freezing tracks", "tracks", h.trackNames(), "from", p.Media, "to", h.end)
	}
	h.Events.Emit(events.HoldStart, p.Media, events.Fields{
		"tracks": h.trackNames(), "forever": h.Forever(), "until": h.end.Seconds()})
	return h.keep(p)
}

// Flush ends an open hold and delivers what it buffered. The run loop calls it before
// looping so no pre-loop packet is sent after a post-loop one.
func (h *Hold) Flush(next Emit) error {
	if !h.holding || h.Forever() {
		return nil
	}
	return h.release(h.end, next)
}

// keep buffers the packet, or drops it when the hold is for good.
func (h *Hold) keep(p *Packet) error {
	if h.Forever() {
		h.Events.Emit(events.HoldDrop, p.Media, events.Fields{"track": string(p.Track.Kind)})
		p.Release()
		return nil
	}
	p.Retain()
	h.buf = append(h.buf, p)
	return nil
}

// release closes the current hold and bursts everything it piled up.
func (h *Hold) release(at time.Duration, next Emit) error {
	held, buf := at-h.start, h.buf
	h.buf, h.holding = nil, false
	h.schedule(at)

	h.Log.Info("bursting held packets", "packets", len(buf), "held", held, "at", at)
	h.Events.Emit(events.HoldBurst, at, events.Fields{
		"packets": len(buf), "held_seconds": held.Seconds(), "tracks": h.trackNames()})
	return burst(buf, next)
}

// schedule picks the next hold start, skipping the times already gone by.
func (h *Hold) schedule(from time.Duration) {
	if len(h.At) > 0 {
		for h.index < len(h.At) && h.anchor+h.At[h.index] < from {
			h.index++
		}
		h.hasNext = h.index < len(h.At)
		if h.hasNext {
			h.next = h.anchor + h.At[h.index]
			h.index++
		}
		return
	}
	if h.Every <= 0 {
		h.hasNext = false
		return
	}
	for {
		h.index++
		h.next = h.anchor + time.Duration(h.index)*h.Every
		if h.next >= from {
			break
		}
	}
	h.hasNext = true
}

func (h *Hold) duration() time.Duration {
	if h.Min <= 0 || h.Min >= h.Duration {
		return h.Duration
	}
	return h.Min + time.Duration(h.Rand.Int63n(int64(h.Duration-h.Min)))
}

func (h *Hold) trackNames() []string {
	names := make([]string, 0, len(h.Tracks))
	for _, k := range []Kind{Audio, Video, Subtitle, Data} {
		if h.Tracks[k] {
			names = append(names, string(k))
		}
	}
	return names
}
