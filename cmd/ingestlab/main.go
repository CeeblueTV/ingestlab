// Command ingestlab publishes a media file to a live endpoint, impairing it on the way.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CeeblueTV/ingestlab/internal/impair"
	"github.com/CeeblueTV/ingestlab/internal/inject"
	"github.com/CeeblueTV/ingestlab/internal/media"
	"github.com/CeeblueTV/ingestlab/internal/run"
	"github.com/CeeblueTV/ingestlab/internal/units"
)

func main() {
	err := realMain()
	switch {
	case err == nil:
	case errors.Is(err, impair.ErrCrash):
		fmt.Fprintln(os.Stderr, "ingestlab:", err)
		os.Exit(3)
	default:
		fmt.Fprintln(os.Stderr, "ingestlab:", err)
		os.Exit(1)
	}
}

func realMain() error {
	fs := flag.NewFlagSet("ingestlab", flag.ContinueOnError)
	fs.Usage = func() { usage(); fs.PrintDefaults() }

	var o run.Options
	var (
		// Core.
		verbose = fs.Bool("v", false, "verbose logging")
		holds   units.StringList
		metas   units.StringList
		holdAt  units.DurationList

		outputTS  units.Duration
		jitter    units.Duration
		jitterMin units.Duration
		cadence   units.Duration
		audioOff  units.Duration
		videoOff  units.Duration
		interval  units.Duration
		metaAfter units.Duration
		scteDur   units.Duration
		scteAnn   units.Duration
	)

	fs.StringVar(&o.Format, "format", "", "output container, guessed from the destination when unset")
	fs.BoolVar(&o.MapAll, "map-all", false, "map every input stream instead of the first audio and video")
	fs.BoolVar(&o.MapTimedData, "map-timed-data", false, "also map input subtitle and data streams")
	fs.BoolVar(&o.VOD, "vod", false, "send as fast as the output accepts, without real-time pacing")
	fs.IntVar(&o.Loop, "loop", -1, "number of extra passes over the input, -1 for endless")
	fs.Int64Var(&o.Seed, "seed", 0, "seed for every random draw, 0 picks one and logs it")
	fs.Var(&outputTS, "output-ts-offset", "delay every output timestamp by this much")

	// Delivery impairments.
	fs.Var(&jitter, "jitter", "deliver in back-to-back bursts of up to this much buffered media")
	fs.Var(&jitterMin, "jitter-min", "shortest burst window when --jitter is set")
	fs.Var(&cadence, "cadence-jitter", "wobble each video output timestamp by up to +/- this much")
	fs.Var(&audioOff, "audio-offset", "shift audio output timestamps; pacing is unchanged")
	fs.Var(&videoOff, "video-offset", "shift video output timestamps; pacing is unchanged")
	fs.Var(&holds, "hold-tracks", "comma-separated tracks to freeze: audio,video,subtitle,data")
	fs.Var(&o.HoldDuration, "hold-duration", "length of each hold, 0 holds for good and drops (default 10s)")
	fs.Var(&o.HoldMin, "hold-duration-min", "when set, each hold lasts a random time up to --hold-duration")
	fs.Var(&o.HoldEvery, "hold-every", "start a hold every this much media (default 30s)")
	fs.Var(&holdAt, "hold-at", "comma-separated media times at which to hold, overrides --hold-every")

	// Track lifecycle faults.
	fs.Var(&o.StartAudioAfter, "start-audio-after", "withhold audio until this much media has passed")
	fs.Var(&o.StartVideoAfter, "start-video-after", "withhold video until this much media has passed")
	fs.Var(&o.StopMediaAfter, "stop-media-after", "stop sending audio and video after this much media")
	fs.Var(&o.ErrorMediaAfter, "error-media-after", "corrupt the video payload after this much media")
	fs.Var(&o.CrashAfter, "crash-after", "drop the connection without unpublishing after this much media")

	// Injected metadata.
	fs.Var(&metas, "meta-tracks", "comma-separated tracks to inject: metadata,scte35,id3,subtitles,none (default metadata)")
	fs.Var(&interval, "interval", "emit an injected metadata packet this often, 0 keeps the track empty (default 1s)")
	fs.Var(&metaAfter, "start-meta-after", "start injecting metadata after this much media")
	fs.BoolVar(&o.Meta.OnFI, "onfi", false, "send the timecode as onFI instead of onMetaData")
	fs.StringVar(&o.Meta.ID3Description, "id3-description", "ceeblue.metadata", "TXXX description for the id3 track")
	scteID := fs.Uint64("scte-id", 0, "first SCTE-35 event id, incremented per cue")
	fs.Var(&scteDur, "scte-duration", "length of the SCTE-35 break (default 4s)")
	fs.Var(&scteAnn, "scte-announce", "how far ahead of the break to send the cue (default 4s)")
	fs.BoolVar(&o.Meta.SCTEOnCuePoint, "scte-oncuepoint", false, "send onCuePoint (AWS Elemental) instead of onAdCue")

	// Telemetry.
	fs.StringVar(&o.EventsPath, "events", "", "write an events.jsonl of what the run did")
	fs.StringVar(&o.SummaryPath, "summary", "", "write a summary.json when the run ends")

	source, dest, args, err := positional(os.Args[1:])
	if err != nil {
		fs.Usage()
		return err
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	o.Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	media.RouteLog(o.Log, *verbose)

	o.Source, o.Dest = source, dest
	o.OutputTSOffset = outputTS.Value
	o.Jitter, o.JitterMin = jitter.Value, jitterMin.Value
	o.CadenceJitter = cadence.Value
	o.AudioOffset, o.VideoOffset = audioOff.Value, videoOff.Value
	o.HoldTracks, o.HoldAt = holds.Values, holdAt.Values

	o.Meta.Interval = orElse(interval, time.Second)
	o.Meta.StartAfter = metaAfter.Value
	o.Meta.SCTEDuration = orElse(scteDur, 4*time.Second)
	o.Meta.SCTEAnnounce = orElse(scteAnn, 4*time.Second)
	o.Meta.SCTEID = uint32(*scteID)

	// No --meta-tracks means one AMF metadata track.
	names := metas.Values
	if !metas.Present {
		names = []string{string(inject.Metadata)}
	}
	if o.Meta.Types, err = inject.ParseTypes(names); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return run.Run(ctx, o)
}

// orElse distinguishes "not given" from an explicit zero, which several options need:
// --interval 0 keeps the metadata track and emits nothing.
func orElse(d units.Duration, fallback time.Duration) time.Duration {
	if d.Present {
		return d.Value
	}
	return fallback
}

// positional pulls <source> <destination> off the front. Flags follow them, which is how
// the tool has always been called.
func positional(argv []string) (source, dest string, rest []string, err error) {
	var pos []string
	for len(argv) > 0 && len(pos) < 2 && !strings.HasPrefix(argv[0], "-") {
		pos = append(pos, argv[0])
		argv = argv[1:]
	}
	if len(pos) < 2 {
		return "", "", nil, fmt.Errorf("need a source and a destination")
	}
	return pos[0], pos[1], argv, nil
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: ingestlab <source> <destination> [options]

  ingestlab video.mkv rtmp://host/live/key
  ingestlab video.mkv srt://host:9000 --loop 0
  ingestlab video.mkv rtmp://host/live/key --hold-tracks video --hold-at 20s,60s

Durations take a unit anywhere: 10s, 1500ms, 2m, 1h. A bare number is milliseconds.

options:
`)
}
