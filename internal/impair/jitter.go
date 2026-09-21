package impair

import (
	"log/slog"
	"math/rand"
	"time"

	"github.com/CeeblueTV/ingestlab/internal/events"
)

// Jitter delivers the stream in back-to-back bursts instead of smoothly: each window
// buffers up to Window of wall time and then flushes at once, with no quiet gap after it.
// A client's adaptive jitter buffer drifts under that, which is how a growing A/V desync
// reproduces.
//
// This one runs on the wall clock: it models the network, not the media.
type Jitter struct {
	Window time.Duration // longest burst window; zero disables
	Min    time.Duration // shortest burst window
	Rand   *rand.Rand
	Now    func() time.Time // overridden in tests

	Log    *slog.Logger
	Events *events.Log

	end time.Time
	buf []*Packet
}

func (j *Jitter) Process(p *Packet, next Emit) error {
	if j.Window <= 0 {
		return next(p)
	}
	now := j.now()

	// Window elapsed: burst it, then open the next one right away so there is no recovery gap.
	if !now.Before(j.end) {
		if err := j.flush(p.Media, next); err != nil {
			return err
		}
		j.end = now.Add(j.Min + time.Duration(j.Rand.Int63n(int64(j.Window-j.Min)+1)))
	}

	if now.Before(j.end) {
		p.Retain()
		j.buf = append(j.buf, p)
		return nil
	}
	return next(p)
}

// Flush empties the window. The run loop calls it before looping and on shutdown.
func (j *Jitter) Flush(next Emit) error {
	j.end = time.Time{}
	return j.flush(0, next)
}

func (j *Jitter) flush(media time.Duration, next Emit) error {
	if len(j.buf) == 0 {
		return nil
	}
	buf := j.buf
	j.buf = nil
	j.Log.Debug("bursting buffered packets", "packets", len(buf), "at", media)
	j.Events.Emit(events.JitterBurst, media, events.Fields{"packets": len(buf)})
	return burst(buf, next)
}

func (j *Jitter) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}
