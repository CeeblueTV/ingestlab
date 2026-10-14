// Package run wires a source to a sink through the impairment chain.
package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/asticode/go-astiav"

	"github.com/CeeblueTV/ingestlab/internal/epoch"
	"github.com/CeeblueTV/ingestlab/internal/events"
	"github.com/CeeblueTV/ingestlab/internal/impair"
	"github.com/CeeblueTV/ingestlab/internal/inject"
	"github.com/CeeblueTV/ingestlab/internal/media"
	"github.com/CeeblueTV/ingestlab/internal/pace"
	"github.com/CeeblueTV/ingestlab/internal/transcode"
	"github.com/CeeblueTV/ingestlab/internal/units"
)

// Options is the whole CLI surface, already parsed.
type Options struct {
	Source       string
	Dest         string
	Format       string // container override; empty means guess from the destination
	MapAll       bool   // map every input stream, not just the first audio and video
	MapTimedData bool   // also map source subtitle and data streams
	VOD          bool   // send as fast as the output accepts, no real-time pacing
	Loop         int    // -1 loops forever, 0 plays once
	Seed         int64

	OutputTSOffset time.Duration

	// Delivery impairments.
	Jitter        time.Duration
	JitterMin     time.Duration
	CadenceJitter time.Duration
	AudioOffset   time.Duration
	VideoOffset   time.Duration
	HoldTracks    []string
	HoldDuration  units.Duration
	HoldMin       units.Duration
	HoldEvery     units.Duration
	HoldAt        []time.Duration

	// Track lifecycle faults.
	StartAudioAfter units.Duration
	StartVideoAfter units.Duration
	StopMediaAfter  units.Duration
	ErrorMediaAfter units.Duration
	CrashAfter      units.Duration

	Meta inject.Config

	// Epoch burns the wall clock into the picture, which costs a decode and a re-encode.
	Epoch        bool
	EpochGOP     units.Duration
	EpochBitrate int64

	EventsPath  string
	SummaryPath string

	Log *slog.Logger
}

func Run(ctx context.Context, o Options) (err error) {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	if o.Seed == 0 {
		o.Seed = time.Now().UnixNano()
	}
	log.Info("seed", "value", o.Seed)
	rng := rand.New(rand.NewSource(o.Seed))

	ev, err := events.Open(o.EventsPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := ev.Close(); err == nil {
			err = cerr
		}
	}()

	src, err := media.OpenSource(o.Source)
	if err != nil {
		return err
	}
	defer src.Close()

	sink, err := media.OpenSink(o.Dest, o.Format)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := sink.Close(); cerr != nil {
			log.Warn("closing sink", "err", cerr)
		}
	}()

	shaper, err := newShaper(src, sink, o, log)
	if err != nil {
		return err
	}
	if shaper != nil {
		defer shaper.Close()
	}

	tracks, err := mapTracks(src, sink, o, log, shaper)
	if err != nil {
		return err
	}
	if len(tracks) == 0 {
		return errors.New("no stream mapped")
	}

	injector, err := inject.New(o.Meta, sink, log, ev)
	if err != nil {
		return err
	}

	if err := sink.Connect(); err != nil {
		return err
	}

	writer := &impair.Writer{Write: sink.Write, Log: log, Events: ev}
	chain, err := buildChain(o, shaper, injector, writer, rng, log, ev)
	if err != nil {
		return err
	}

	log.Info("publishing", "source", o.Source, "dest", o.Dest, "format", sink.FormatName(),
		"tracks", len(tracks)+len(injector.Tracks()))

	err = pump(ctx, src, chain, writer, tracks, o, log, ev)

	// A crash must look like a publisher that vanished, so the trailer is never written.
	if errors.Is(err, impair.ErrCrash) {
		log.Error("crashing on purpose", "err", err)
		sink.Abort()
	}

	if o.SummaryPath != "" {
		s := ev.Summarise()
		s.Source, s.Dest, s.Format, s.Seed = o.Source, o.Dest, sink.FormatName(), o.Seed
		s.Packets, s.Bytes = writer.Packets, writer.Bytes
		s.Media = mediaEnd(tracks).Seconds()
		s.Outcome = "ok"
		if err != nil {
			s.Outcome, s.ExitNote = "error", err.Error()
		}
		if werr := events.WriteSummary(o.SummaryPath, s); werr != nil {
			log.Warn("writing summary", "err", werr)
		}
	}
	return err
}

func buildChain(o Options, shaper *transcode.Video, injector *inject.Injector,
	writer *impair.Writer, rng *rand.Rand, log *slog.Logger, ev *events.Log) (*impair.Chain, error) {

	var stages []impair.Stage
	// First, so the clock is read after pacing and every impairment below acts on a
	// picture that already states when it was sent.
	if shaper != nil {
		stages = append(stages, shaper)
	}
	if injector != nil {
		stages = append(stages, injector)
	}

	stages = append(stages, &impair.Faults{
		StartAudio: o.StartAudioAfter, StartVideo: o.StartVideoAfter,
		StopMedia: o.StopMediaAfter, ErrorMedia: o.ErrorMediaAfter, Crash: o.CrashAfter,
		Log: log, Events: ev,
	})

	if len(o.HoldTracks) > 0 {
		hold, err := newHold(o, rng, log, ev)
		if err != nil {
			return nil, err
		}
		stages = append(stages, hold)
	}

	// Burst delivery models the network, so it means nothing without real-time pacing.
	if o.Jitter > 0 && !o.VOD {
		if o.JitterMin > o.Jitter {
			log.Warn("jitter-min is above jitter, using a fixed window", "min", o.JitterMin, "max", o.Jitter)
			o.JitterMin = 0
		}
		log.Info("bursting delivery", "window", o.Jitter, "min", o.JitterMin)
		stages = append(stages, &impair.Jitter{
			Window: o.Jitter, Min: o.JitterMin, Rand: rng, Log: log, Events: ev,
		})
	}

	ts := &impair.Timestamps{
		Output: o.OutputTSOffset, Audio: o.AudioOffset, Video: o.VideoOffset,
		Cadence: o.CadenceJitter, Rand: rng,
	}
	if ts.Active() {
		log.Info("shifting output timestamps", "output", ts.Output, "audio", ts.Audio,
			"video", ts.Video, "cadence", ts.Cadence)
		stages = append(stages, ts)
	}

	return impair.NewChain(append(stages, writer)...), nil
}

func newHold(o Options, rng *rand.Rand, log *slog.Logger, ev *events.Log) (*impair.Hold, error) {
	kinds := map[impair.Kind]bool{}
	for _, name := range o.HoldTracks {
		k := impair.Kind(name)
		switch k {
		case impair.Audio, impair.Video, impair.Subtitle, impair.Data:
			kinds[k] = true
		default:
			return nil, fmt.Errorf("unknown hold track type %q, expected audio, video, subtitle or data", name)
		}
	}

	h := &impair.Hold{
		Tracks: kinds, Duration: impair.DefaultHoldDuration, Every: impair.DefaultHoldEvery,
		At: o.HoldAt, Rand: rng, Log: log, Events: ev,
	}
	if o.HoldDuration.Present {
		h.Duration = o.HoldDuration.Value
	}
	if o.HoldEvery.Present {
		h.Every = o.HoldEvery.Value
	}
	if o.HoldMin.Present && !h.Forever() {
		if o.HoldMin.Value > h.Duration {
			log.Warn("hold-duration-min is above hold-duration, using a fixed hold",
				"min", o.HoldMin.Value, "max", h.Duration)
		} else {
			h.Min = o.HoldMin.Value
		}
	}

	switch {
	case h.Forever():
		log.Info("holding tracks for good", "tracks", o.HoldTracks, "dropping", true)
	case len(h.At) > 0:
		log.Info("holding tracks", "tracks", o.HoldTracks, "at", h.At, "for", h.Duration)
	default:
		if h.Every <= h.Duration {
			log.Warn("hold-every is not above the hold duration, holds will follow each other",
				"every", h.Every, "duration", h.Duration)
		}
		log.Info("holding tracks", "tracks", o.HoldTracks, "every", h.Every, "for", h.Duration)
	}
	return h, nil
}

// newShaper builds the transcode stage when --epoch asks for it, over the video stream
// that mapTracks would keep. Nothing is allocated when it is off, which is the point of
// the option existing at all.
func newShaper(src *media.Source, sink *media.Sink, o Options, log *slog.Logger) (*transcode.Video, error) {
	if !o.Epoch {
		return nil, nil
	}

	var in *astiav.Stream
	for _, s := range src.Streams() {
		if impair.KindOf(s.CodecParameters().MediaType()) == impair.Video {
			in = s
			break
		}
	}
	if in == nil {
		return nil, errors.New("--epoch needs a video stream to draw the band on")
	}

	cfg := transcode.Config{
		Bitrate:      o.EpochBitrate,
		GlobalHeader: sink.NeedsGlobalHeader(),
	}
	if o.EpochGOP.Present {
		cfg.GOP = o.EpochGOP.Value
	}

	v, err := transcode.NewVideo(in, cfg)
	if err != nil {
		return nil, fmt.Errorf("--epoch: %w", err)
	}
	log.Info("burning the epoch band", "stream", in.Index(),
		"size", fmt.Sprintf("%dx%d", in.CodecParameters().Width(), in.CodecParameters().Height()),
		"block", epoch.BlockSize(in.CodecParameters().Width()))
	if in.CodecParameters().Width()%epoch.Blocks != 0 {
		log.Warn("the width is not a multiple of 32, so the band leaves a margin and a "+
			"scaled delivery will not line up with it", "width", in.CodecParameters().Width())
	}
	return v, nil
}

func mapTracks(src *media.Source, sink *media.Sink, o Options, log *slog.Logger,
	shaper *transcode.Video) (map[int]*impair.Track, error) {
	tracks := map[int]*impair.Track{}
	var haveAudio, haveVideo bool

	for _, in := range src.Streams() {
		kind := impair.KindOf(in.CodecParameters().MediaType())
		keep := o.MapAll
		switch {
		case keep:
		case kind == impair.Video && !haveVideo:
			keep, haveVideo = true, true
		case kind == impair.Audio && !haveAudio:
			keep, haveAudio = true, true
		case o.MapTimedData && (kind == impair.Subtitle || kind == impair.Data):
			keep = true
		}
		if !keep {
			log.Debug("skipping stream", "index", in.Index(), "type", string(kind))
			continue
		}

		var (
			out      *astiav.Stream
			err      error
			shaped   = shaper != nil && shaper.StreamIndex() == in.Index()
			timebase = in.TimeBase()
			codec    = in.CodecParameters().CodecID().Name()
		)
		if shaped {
			// The output describes the encoder, not the source: it is a different bitstream.
			out, err = sink.AddEncodedStream(shaper.Encoder())
			timebase, codec = shaper.Encoder().TimeBase(), "h264 (re-encoded for the band)"
		} else {
			out, err = sink.AddStream(in)
		}
		if err != nil {
			return nil, fmt.Errorf("mapping stream %d: %w", in.Index(), err)
		}

		t := &impair.Track{Kind: kind, In: in, Out: out, TimeBase: timebase}
		tracks[in.Index()] = t
		if shaped {
			shaper.Bind(t)
		}
		log.Info("mapped stream", "in", in.Index(), "out", out.Index(), "type", string(kind),
			"codec", codec)
	}
	return tracks, nil
}

func pump(ctx context.Context, src *media.Source, chain *impair.Chain, writer *impair.Writer,
	tracks map[int]*impair.Track, o Options, log *slog.Logger, ev *events.Log) error {

	pkt := astiav.AllocPacket()
	defer pkt.Free()

	var pacer pace.Pacer
	var lastLog time.Duration
	// The worst lateness since the last progress line: the line's own `behind` is one
	// instant every 5s, and a packet held up between two lines would never show.
	var behindMax time.Duration
	loops := o.Loop

	for {
		if err := ctx.Err(); err != nil {
			log.Info("stopping", "reason", err)
			break
		}

		if err := src.ReadFrame(pkt); err != nil {
			if !errors.Is(err, astiav.ErrEof) {
				return fmt.Errorf("reading frame: %w", err)
			}
			// Everything buffered belongs to the pass that just ended: deliver it before
			// the loop anchor moves, or a pre-loop packet would arrive after a post-loop one.
			if err := chain.Flush(); err != nil {
				return err
			}
			if loops == 0 {
				log.Info("end of input")
				break
			}
			if loops > 0 {
				loops--
			}
			writer.Base = mediaEnd(tracks)
			if err := src.Rewind(); err != nil {
				return fmt.Errorf("rewinding: %w", err)
			}
			log.Info("looping", "offset", writer.Base)
			ev.Emit(events.Loop, writer.Base, events.Fields{"remaining": loops})
			continue
		}

		// An unmapped stream's packet is dropped at media 0, which is not lateness.
		_, mapped := tracks[pkt.StreamIndex()]
		media, err := forward(ctx, pkt, chain, writer.Base, tracks, &pacer, o.VOD)
		pkt.Unref()
		if err != nil {
			return err
		}

		behind := pacer.Behind(media)
		if mapped {
			behindMax = max(behindMax, behind)
		}
		if media > lastLog+5*time.Second {
			lastLog = media
			log.Info("progress", "media", media.Round(time.Millisecond),
				"packets", writer.Packets, "behind", behind.Round(time.Millisecond),
				"behind_max", behindMax.Round(time.Millisecond))
			behindMax = 0
		}
	}

	if err := chain.Flush(); err != nil {
		return err
	}
	ev.Emit(events.StreamEnd, mediaEnd(tracks), nil)
	return nil
}

// forward paces the packet and hands it to the chain, returning the media time it sat at.
func forward(ctx context.Context, pkt *astiav.Packet, chain *impair.Chain, base time.Duration,
	tracks map[int]*impair.Track, pacer *pace.Pacer, vod bool) (time.Duration, error) {

	t, ok := tracks[pkt.StreamIndex()]
	if !ok {
		return 0, nil
	}

	media := base + toDuration(pkt.Dts(), t.TimeBase)
	if !vod {
		if err := pacer.Wait(ctx, media); err != nil {
			return media, nil // cancelled
		}
	}
	t.End = media + toDuration(pkt.Duration(), t.TimeBase)

	return media, chain.Process(impair.Borrow(pkt, t, media))
}

// mediaEnd is where the output timeline has reached, so looping continues it rather than
// rewinding the timestamps the server sees. Only the real-time tracks count: a sparse data
// track ends wherever its last cue happened to fall.
func mediaEnd(tracks map[int]*impair.Track) time.Duration {
	var max time.Duration
	for _, t := range tracks {
		if t.Kind != impair.Audio && t.Kind != impair.Video {
			continue
		}
		if t.End > max {
			max = t.End
		}
	}
	return max
}

func toDuration(ts int64, tb astiav.Rational) time.Duration {
	if ts == astiav.NoPtsValue || tb.Den() == 0 {
		return 0
	}
	return time.Duration(astiav.RescaleQ(ts, tb, astiav.NewRational(1, int(time.Second))))
}
