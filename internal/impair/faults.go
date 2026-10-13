package impair

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/asticode/go-astiav"

	"github.com/CeeblueTV/ingestlab/internal/events"
	"github.com/CeeblueTV/ingestlab/internal/units"
)

// garbage is the payload --error-media-after writes over a video packet: an access unit
// delimiter followed by a truncated NAL. The exact bytes matter — decoders differ in what
// they salvage — so treat them as fixed.
var garbage = []byte{0x00, 0x00, 0x00, 0x01, 0x09, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x04}

// Faults are the track lifecycle impairments: a track that starts late, stops early, turns
// to garbage, or a publisher that dies. All of them run on the media clock.
//
// Injected tracks are left alone, so metadata keeps flowing after the media stops — that
// asymmetry is what --stop-media-after exists to test.
type Faults struct {
	StartAudio units.Duration
	StartVideo units.Duration
	StopMedia  units.Duration
	ErrorMedia units.Duration
	Crash      units.Duration

	Log    *slog.Logger
	Events *events.Log

	stopped, corrupting bool
	late                map[Kind]bool
}

func (f *Faults) Process(p *Packet, next Emit) error {
	if f.Crash.Present && p.Media >= f.Crash.Value {
		f.Events.Emit(events.FaultInjected, p.Media, events.Fields{"fault": "crash"})
		p.Release()
		return fmt.Errorf("%w at %v of media", ErrCrash, p.Media)
	}

	if p.Track.Injected() {
		return next(p)
	}
	kind := p.Track.Kind

	if f.StopMedia.Present && p.Media >= f.StopMedia.Value {
		if !f.stopped {
			f.stopped = true
			f.Log.Info("stopping media", "at", p.Media)
			f.Events.Emit(events.FaultInjected, p.Media, events.Fields{"fault": "stop-media"})
		}
		p.Release()
		return nil
	}

	if start, ok := f.startAfter(kind); ok && p.Media < start {
		if !f.late[kind] { // reading a nil map is fine; only the write below needs one
			if f.late == nil {
				f.late = map[Kind]bool{}
			}
			f.late[kind] = true
			f.Log.Info("withholding track until its start time", "track", string(kind), "until", start)
			f.Events.Emit(events.FaultInjected, p.Media, events.Fields{
				"fault": "start-after", "track": string(kind), "until": start.Seconds()})
		}
		p.Release()
		return nil
	}

	if kind == Video && f.ErrorMedia.Present && p.Media >= f.ErrorMedia.Value {
		if !f.corrupting {
			f.corrupting = true
			f.Log.Info("corrupting video payload", "at", p.Media)
			f.Events.Emit(events.FaultInjected, p.Media, events.Fields{"fault": "error-media"})
		}
		if err := corrupt(p); err != nil {
			p.Release()
			return err
		}
	}

	return next(p)
}

func (f *Faults) Flush(Emit) error { return nil }

// startAfter is the media time before which this track is withheld entirely. The first
// packet delivered is whatever the source had there, keyframe or not, which is the part
// worth reproducing.
func (f *Faults) startAfter(k Kind) (time.Duration, bool) {
	switch k {
	case Audio:
		return f.StartAudio.Value, f.StartAudio.Present
	case Video:
		return f.StartVideo.Value, f.StartVideo.Present
	}
	return 0, false
}

// corrupt replaces the payload while keeping every timestamp and flag, so only the bytes
// change and the packet still paces and muxes like the one it replaced.
func corrupt(p *Packet) error {
	pkt := astiav.AllocPacket()
	if err := pkt.FromData(garbage); err != nil {
		pkt.Free()
		return fmt.Errorf("building corrupt payload: %w", err)
	}
	if err := pkt.CopyProperties(p.Pkt); err != nil {
		pkt.Free()
		return fmt.Errorf("copying packet properties: %w", err)
	}
	p.Release()
	p.Pkt, p.owned = pkt, true
	return nil
}
