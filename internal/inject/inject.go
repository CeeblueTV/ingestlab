// Package inject adds our own timed-metadata tracks to the stream: AMF metadata, SCTE-35
// cues, timed ID3 and subtitles. They are driven by the media clock of the packets flowing
// past, so what they carry lines up with what the source is sending.
package inject

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/asticode/go-astiav"

	"github.com/CeeblueTV/ingestlab/internal/events"
	"github.com/CeeblueTV/ingestlab/internal/impair"
)

// Type is a metadata track we can inject.
type Type string

const (
	Metadata  Type = "metadata"
	SCTE35    Type = "scte35"
	ID3       Type = "id3"
	Subtitles Type = "subtitles"
	None      Type = "none"
)

var Types = []Type{Metadata, SCTE35, ID3, Subtitles, None}

// Config is the whole --meta-tracks surface.
type Config struct {
	Types      []Type
	Interval   time.Duration // zero keeps the tracks and emits nothing
	StartAfter time.Duration

	OnFI           bool   // timecode as onFI rather than onMetaData
	ID3Description string // TXXX description
	SCTEID         uint32
	SCTEDuration   time.Duration
	SCTEAnnounce   time.Duration
	SCTEOnCuePoint bool // onCuePoint (AWS Elemental) rather than onAdCue
}

// Validate rejects combinations that cannot mean anything. It runs before any output stream
// is added, so a refused run leaves the muxer untouched.
func (c Config) Validate() error {
	// auto_return closes each break --scte-duration after it opens, so a shorter interval
	// would announce the next break before the previous one has returned. A zero interval
	// is not the same mistake: it keeps the track and emits nothing.
	if slices.Contains(c.Types, SCTE35) && c.Interval > 0 && c.Interval < c.SCTEDuration {
		return fmt.Errorf("--interval %v is shorter than --scte-duration %v: each SCTE-35 break "+
			"would be cut short by the next cue", c.Interval, c.SCTEDuration)
	}
	return nil
}

// Muxer is the slice of the sink an injector needs.
type Muxer interface {
	AddDataStream(codec astiav.CodecID, tb astiav.Rational) (*astiav.Stream, error)
	FormatName() string
}

// ParseTypes turns the CLI list into track types, resolving "none" to nothing.
func ParseTypes(values []string) ([]Type, error) {
	var out []Type
	for _, v := range values {
		t := Type(v)
		switch t {
		case None:
			if len(values) > 1 {
				return nil, fmt.Errorf("%q cannot be combined with other metadata track types", None)
			}
			return nil, nil
		case Metadata, SCTE35, ID3, Subtitles:
			out = append(out, t)
		default:
			return nil, fmt.Errorf("unknown metadata track type %q, expected one of %v", v, Types)
		}
	}
	return out, nil
}

// Injector is the pipeline stage. It is the first one, so what it emits goes through every
// impairment: a held or burst data track is a case worth testing in its own right.
type Injector struct {
	emitters []*emitter
	Log      *slog.Logger
	Events   *events.Log
}

type emitter struct {
	typ   Type
	track *impair.Track
	build func(media time.Duration) ([]byte, events.Fields, error)

	interval   time.Duration
	startAfter time.Duration

	anchored bool
	startAt  time.Duration
	nextAt   time.Duration
}

// New adds an output stream per requested track. It must run before the header is written.
func New(cfg Config, mux Muxer, log *slog.Logger, ev *events.Log) (*Injector, error) {
	if len(cfg.Types) == 0 {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	mpegts := mux.FormatName() == "mpegts"
	if !mpegts && len(cfg.Types) > 1 {
		// One FLV data track is all flvenc tracks; E-RTMP PacketTypeMetadata on FFmpeg 8.1
		// is what would lift this.
		return nil, fmt.Errorf("FLV output carries one metadata track at a time (asked for %v)", cfg.Types)
	}

	in := &Injector{Log: log, Events: ev}
	for _, t := range cfg.Types {
		e, err := newEmitter(t, cfg, mux, mpegts)
		if err != nil {
			return nil, err
		}
		in.emitters = append(in.emitters, e)
		log.Info("injecting metadata track", "type", string(t), "out", e.track.Out.Index(),
			"interval", cfg.Interval, "start-after", cfg.StartAfter)
	}
	if cfg.Interval <= 0 {
		log.Info("metadata interval is zero: the tracks are announced but stay empty")
	}
	return in, nil
}

func newEmitter(t Type, cfg Config, mux Muxer, mpegts bool) (*emitter, error) {
	e := &emitter{typ: t, interval: cfg.Interval, startAfter: cfg.StartAfter}

	codec, tb := astiav.CodecIDNone, astiav.NewRational(1, 1000)
	switch t {
	case ID3:
		if !mpegts {
			return nil, fmt.Errorf("the id3 track needs an MPEG-TS output, not %s", mux.FormatName())
		}
		codec, tb = astiav.CodecIDTimedId3, astiav.NewRational(1, 90000)
	case SCTE35:
		if mpegts {
			codec, tb = astiav.CodecIDScte35, astiav.NewRational(1, 90000)
		}
	}

	out, err := mux.AddDataStream(codec, tb)
	if err != nil {
		return nil, fmt.Errorf("adding the %s track: %w", t, err)
	}
	e.track = &impair.Track{Kind: impair.Data, Out: out, TimeBase: tb}

	switch t {
	case Metadata:
		e.build = metadataPayload(cfg.OnFI)
	case Subtitles:
		e.build = subtitlePayload()
	case ID3:
		e.build = id3Payload(cfg.ID3Description)
	case SCTE35:
		e.build = sctePayload(cfg, mpegts)
	}
	return e, nil
}

// Tracks lists the injected tracks, for logging.
func (i *Injector) Tracks() []*impair.Track {
	if i == nil {
		return nil
	}
	out := make([]*impair.Track, len(i.emitters))
	for n, e := range i.emitters {
		out[n] = e.track
	}
	return out
}

func (i *Injector) Process(p *impair.Packet, next impair.Emit) error {
	// Our own packets must not drive the schedule, or each one would beget another.
	if p.Track.Injected() {
		return next(p)
	}
	for _, e := range i.emitters {
		if !e.due(p.Media) {
			continue
		}
		data, fields, err := e.build(p.Media)
		if err != nil {
			return fmt.Errorf("building %s payload: %w", e.typ, err)
		}
		pkt, err := e.packet(data, p)
		if err != nil {
			return err
		}
		e.nextAt = p.Media + e.interval

		kind := events.MetaSent
		if e.typ == SCTE35 {
			kind = events.SCTE35Cue
		}
		i.Events.Emit(kind, p.Media, merge(fields, "track", string(e.typ)))
		i.Log.Debug("injecting metadata", "type", string(e.typ), "at", p.Media, "size", len(data))

		if err := next(impair.Own(pkt, e.track, p.Media)); err != nil {
			return err
		}
	}
	return next(p)
}

func (i *Injector) Flush(impair.Emit) error { return nil }

// due: the first emission lands one interval after the first packet, and
// --start-meta-after pushes it further out. Every track type honours both.
func (e *emitter) due(media time.Duration) bool {
	if !e.anchored {
		e.anchored = true
		e.startAt = media + e.startAfter
		e.nextAt = media + e.interval
	}
	if e.interval <= 0 {
		return false
	}
	return media >= e.startAt && media >= e.nextAt
}

// packet carries the driving packet's timing into the metadata track's timebase, so the
// cue lands on the media it describes.
func (e *emitter) packet(data []byte, driver *impair.Packet) (*astiav.Packet, error) {
	pkt := astiav.AllocPacket()
	if err := pkt.FromData(data); err != nil {
		pkt.Free()
		return nil, fmt.Errorf("allocating the %s packet: %w", e.typ, err)
	}
	src, dst := driver.Track.TimeBase, e.track.TimeBase
	pkt.SetDts(rescale(driver.Pkt.Dts(), src, dst))
	pkt.SetPts(rescale(driver.Pkt.Pts(), src, dst))
	pkt.SetDuration(astiav.RescaleQ(driver.Pkt.Duration(), src, dst))
	return pkt, nil
}

func rescale(ts int64, src, dst astiav.Rational) int64 {
	if ts == astiav.NoPtsValue {
		return astiav.NoPtsValue
	}
	return astiav.RescaleQ(ts, src, dst)
}

func metadataPayload(onFI bool) func(time.Duration) ([]byte, events.Fields, error) {
	var index int
	return func(media time.Duration) ([]byte, events.Fields, error) {
		now := time.Now().UTC()
		if onFI {
			data, err := amfEncode("onFI", amfObj{
				{"sd", now.Format("02-01-2006")},
				{"st", now.Format("15:04:05.000")},
			})
			return data, events.Fields{"format": "onFI"}, err
		}
		index++
		data, err := amfEncode("onMetaData", amfObj{
			{"timecode", now.UnixMilli()},
			{"index", index},
		})
		return data, events.Fields{"format": "onMetaData", "index": index}, err
	}
}

func subtitlePayload() func(time.Duration) ([]byte, events.Fields, error) {
	return func(media time.Duration) ([]byte, events.Fields, error) {
		text := fmt.Sprintf("subtitle at %v of media, %s", media.Round(time.Millisecond),
			time.Now().UTC().Format(time.RFC3339Nano))
		data, err := amfEncode("onTextData", text)
		return data, nil, err
	}
}

func id3Payload(description string) func(time.Duration) ([]byte, events.Fields, error) {
	var id int
	return func(media time.Duration) ([]byte, events.Fields, error) {
		id++
		body, err := json.Marshal(struct {
			ID  int    `json:"id"`
			DTS int64  `json:"dts"`
			UTC string `json:"utc"`
		}{id, media.Milliseconds(), time.Now().UTC().Format(time.RFC3339Nano)})
		if err != nil {
			return nil, nil, err
		}
		return id3Tag(txxxFrame(description, string(body))), events.Fields{"id": id}, nil
	}
}

func sctePayload(cfg Config, mpegts bool) func(time.Duration) ([]byte, events.Fields, error) {
	id := cfg.SCTEID
	return func(media time.Duration) ([]byte, events.Fields, error) {
		id++
		at := media + cfg.SCTEAnnounce
		cue := SpliceInsert{
			EventID:   id,
			Duration:  cfg.SCTEDuration,
			ProgramID: 0xC000,
			PTS:       -1, // splice_immediate, unless MPEG-TS gives us somewhere to put a PTS
		}
		fields := events.Fields{
			"id": id, "at_seconds": at.Seconds(), "duration_seconds": cfg.SCTEDuration.Seconds(),
		}

		if mpegts {
			cue.PTS = int64(at) * 90000 / int64(time.Second) & 0x1FFFFFFFF
			return cue.Section(), fields, nil
		}
		if cfg.SCTEOnCuePoint {
			data, err := cue.cuePoint(cfg.SCTEAnnounce)
			return data, fields, err
		}
		data, err := cue.adCue(at)
		return data, fields, err
	}
}

func merge(f events.Fields, k string, v any) events.Fields {
	if f == nil {
		f = events.Fields{}
	}
	f[k] = v
	return f
}

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }
