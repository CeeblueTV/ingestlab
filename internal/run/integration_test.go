package run

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asticode/go-astiav"

	"github.com/CeeblueTV/ingestlab/internal/impair"
	"github.com/CeeblueTV/ingestlab/internal/inject"
	"github.com/CeeblueTV/ingestlab/internal/media"
	"github.com/CeeblueTV/ingestlab/internal/units"
)

// These publish for real and read the result back, which is the only way to tell an
// impairment that works from one that the muxer quietly undid. They need a sample:
// $INGESTLAB_SAMPLE, or ~/samples/bbb_multi_with_time.mkv.
func sample(t *testing.T) string {
	t.Helper()
	path := os.Getenv("INGESTLAB_SAMPLE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory to look for a sample in")
		}
		path = filepath.Join(home, "samples", "bbb_multi_with_time.mkv")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no sample at %s; set INGESTLAB_SAMPLE to run this", path)
	}
	return path
}

// publish runs one scenario to a temporary FLV and returns its path.
func publish(t *testing.T, o Options) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "out.flv")
	o.Source, o.Dest = sample(t), dest
	o.VOD, o.Loop = true, 0
	o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	if o.Meta.Types == nil {
		o.Meta.Interval = time.Second
	}
	if err := Run(context.Background(), o); err != nil && !errors.Is(err, impair.ErrCrash) {
		t.Fatalf("publishing: %v", err)
	}
	return dest
}

type wirePacket struct {
	kind impair.Kind
	dts  time.Duration
}

// readBack returns what arrived, in the order it was written.
func readBack(t *testing.T, path string) []wirePacket {
	t.Helper()
	src, err := media.OpenSource(path)
	if err != nil {
		t.Fatalf("reading back %s: %v", path, err)
	}
	defer src.Close()

	kinds := map[int]impair.Kind{}
	bases := map[int]astiav.Rational{}
	for _, s := range src.Streams() {
		kinds[s.Index()] = impair.KindOf(s.CodecParameters().MediaType())
		bases[s.Index()] = s.TimeBase()
	}

	pkt := astiav.AllocPacket()
	defer pkt.Free()

	var out []wirePacket
	for {
		if err := src.ReadFrame(pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) {
				return out
			}
			t.Fatalf("reading packet: %v", err)
		}
		i := pkt.StreamIndex()
		out = append(out, wirePacket{kinds[i], toDuration(pkt.Dts(), bases[i])})
		pkt.Unref()
	}
}

func after(d time.Duration) units.Duration { return units.Duration{Value: d, Present: true} }

func noMeta() inject.Config { return inject.Config{} }

// A hold must be visible on the wire, not just in our own bookkeeping: the muxer
// interleaves by dts and would happily put the burst back where it came from.
func TestHoldLagsOnTheWire(t *testing.T) {
	path := publish(t, Options{
		Meta:         noMeta(),
		HoldTracks:   []string{"video"},
		HoldAt:       []time.Duration{20 * time.Second},
		HoldDuration: after(5 * time.Second),
	})
	got := readBack(t, path)

	// Find the stretch where video stops arriving while audio keeps going.
	var lastVideo, gapStart, gapEnd int
	for i, p := range got {
		if p.kind != impair.Video {
			continue
		}
		if i-lastVideo > 20 && gapEnd == 0 {
			gapStart, gapEnd = lastVideo, i
		}
		lastVideo = i
	}
	if gapEnd == 0 {
		t.Fatal("video never stopped arriving, the hold did not reach the wire")
	}

	// The audio already on the wire is seconds ahead of the video that follows it.
	var audioAhead time.Duration
	for _, p := range got[:gapEnd] {
		if p.kind == impair.Audio && p.dts > audioAhead {
			audioAhead = p.dts
		}
	}
	lag := audioAhead - got[gapEnd].dts
	if lag < 3*time.Second {
		t.Fatalf("video resumes only %v behind the audio, want at least 3s", lag)
	}

	// Nothing was lost and no timestamp was rewritten: the burst carries its own timing.
	var video []time.Duration
	for _, p := range got {
		if p.kind == impair.Video {
			video = append(video, p.dts)
		}
	}
	if got[gapStart].dts >= got[gapEnd].dts {
		t.Fatalf("the burst starts at %v, after the %v that preceded the gap", got[gapEnd].dts, got[gapStart].dts)
	}
	for i := 1; i < len(video); i++ {
		if video[i] <= video[i-1] {
			t.Fatalf("video timestamps are not monotonic at %d: %v then %v", i, video[i-1], video[i])
		}
	}
}

// A per-track offset has to move the A/V skew on the wire by exactly its own value.
func TestOffsetMovesSkew(t *testing.T) {
	skew := func(o Options) time.Duration {
		var audio, video time.Duration = -1, -1
		for _, p := range readBack(t, publish(t, o)) {
			if p.kind == impair.Audio && audio < 0 {
				audio = p.dts
			}
			if p.kind == impair.Video && video < 0 {
				video = p.dts
			}
			if audio >= 0 && video >= 0 {
				break
			}
		}
		return audio - video
	}

	base := skew(Options{Meta: noMeta(), StopMediaAfter: after(5 * time.Second)})
	moved := skew(Options{Meta: noMeta(), StopMediaAfter: after(5 * time.Second),
		AudioOffset: -250 * time.Millisecond})

	// Rounding to the FLV millisecond grid is the only slack allowed.
	if d := (moved - base) + 250*time.Millisecond; d < -2*time.Millisecond || d > 2*time.Millisecond {
		t.Fatalf("skew moved by %v, want -250ms", moved-base)
	}
}

// --stop-media-after cuts the media and leaves the injected track running.
func TestStopMediaLeavesMetadataRunning(t *testing.T) {
	path := publish(t, Options{
		Meta:           inject.Config{Types: []inject.Type{inject.Metadata}, Interval: time.Second},
		StopMediaAfter: after(6 * time.Second),
	})

	var lastMedia, lastData time.Duration
	for _, p := range readBack(t, path) {
		switch p.kind {
		case impair.Audio, impair.Video:
			lastMedia = max(lastMedia, p.dts)
		default:
			lastData = max(lastData, p.dts)
		}
	}
	if lastMedia > 7*time.Second {
		t.Fatalf("media ran to %v, want it cut at 6s", lastMedia)
	}
	if lastData < 30*time.Second {
		t.Fatalf("the injected track stopped at %v, it should outlive the media", lastData)
	}
}

// --crash-after leaves the stream unfinished, which is the point: no clean unpublish.
func TestCrashStopsEarly(t *testing.T) {
	path := publish(t, Options{Meta: noMeta(), CrashAfter: after(4 * time.Second)})
	var last time.Duration
	for _, p := range readBack(t, path) {
		last = max(last, p.dts)
	}
	if last > 5*time.Second {
		t.Fatalf("the stream ran to %v after a crash at 4s", last)
	}
}
