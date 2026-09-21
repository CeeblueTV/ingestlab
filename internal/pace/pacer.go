// Package pace releases packets in real time.
package pace

import (
	"context"
	"time"
)

// Pacer holds each packet until its media time is due, anchored on the first packet.
type Pacer struct {
	origin  time.Time
	base    time.Duration
	started bool
}

// Wait blocks until media is due. The first call anchors the clock and returns immediately.
func (p *Pacer) Wait(ctx context.Context, media time.Duration) error {
	if !p.started {
		p.origin, p.base, p.started = time.Now(), media, true
		return nil
	}

	d := time.Until(p.origin.Add(media - p.base))
	if d <= 0 {
		return nil
	}

	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Behind reports how far the given media time is past its due instant, which is the
// signal that the machine cannot keep up.
func (p *Pacer) Behind(media time.Duration) time.Duration {
	if !p.started {
		return 0
	}
	return time.Since(p.origin.Add(media - p.base))
}
