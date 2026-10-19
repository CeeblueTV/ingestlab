package impair

import (
	"log/slog"
	"time"

	"github.com/asticode/go-astiav"

	"github.com/CeeblueTV/ingestlab/internal/events"
)

// Writer is the end of the chain and the only place a timestamp leaves our rational domain:
// it rescales to the output timebase, adds the accumulated shift, and hands the packet to
// the muxer.
type Writer struct {
	Write  func(*astiav.Packet) error
	Log    *slog.Logger
	Events *events.Log

	// Base is the media time the current loop pass starts at, so looping continues the
	// output timeline instead of rewinding it. The run loop sets it before each pass.
	Base time.Duration

	Packets int64
	Bytes   int64

	started bool
}

func (w *Writer) Process(p *Packet, next Emit) error {
	t := p.Track
	tb := t.Out.TimeBase()

	p.Pkt.SetStreamIndex(t.Out.Index())
	p.Pkt.RescaleTs(t.TimeBase, tb)
	p.Pkt.SetPos(-1)

	// A B-frame stream has no dts on its first packets after each seek, and the muxer would
	// fill one in from the previous pass. Start it at the pass start; the clamp lifts it.
	guessed := p.Pkt.Dts() == astiav.NoPtsValue
	if guessed {
		p.Pkt.SetDts(0)
	}

	if shift := ticks(w.Base+p.Shift, tb); shift != 0 {
		bump(p.Pkt, shift)
	}

	// The muxer rejects a dts that does not advance, and cadence jitter can produce one.
	// Clamping here keeps the impairment visible on the wire without failing the run.
	if dts := p.Pkt.Dts(); dts != astiav.NoPtsValue {
		if t.written && dts <= t.lastOut {
			fix := t.lastOut + 1 - dts
			w.Log.Debug("clamping dts to stay monotonic", "track", string(t.Kind), "dts", dts, "by", fix)
			if guessed {
				p.Pkt.SetDts(dts + fix) // only the dts is ours to move
			} else {
				bump(p.Pkt, fix)
			}
			dts = p.Pkt.Dts()
		}
		t.lastOut, t.written = dts, true
	}
	if pts := p.Pkt.Pts(); pts != astiav.NoPtsValue && pts < p.Pkt.Dts() {
		p.Pkt.SetPts(p.Pkt.Dts())
	}

	if !w.started {
		w.started = true
		w.Events.Emit(events.StreamStart, p.Media, events.Fields{"track": string(t.Kind)})
	}
	w.Packets++
	w.Bytes += int64(p.Pkt.Size())

	if err := w.Write(p.Pkt); err != nil {
		return err
	}
	return next(p)
}

func (w *Writer) Flush(Emit) error { return nil }

func bump(pkt *astiav.Packet, by int64) {
	if dts := pkt.Dts(); dts != astiav.NoPtsValue {
		pkt.SetDts(dts + by)
	}
	if pts := pkt.Pts(); pts != astiav.NoPtsValue {
		pkt.SetPts(pts + by)
	}
}
