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

	"github.com/CeeblueTV/ingestlab/internal/epoch"
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
	return sampleAt(t, "INGESTLAB_SAMPLE", "bbb_multi_with_time.mkv")
}

// sampleAt looks in $env, then for name in ~/samples.
func sampleAt(t *testing.T, env, name string) string {
	t.Helper()
	path := os.Getenv(env)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory to look for a sample in")
		}
		path = filepath.Join(home, "samples", name)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no sample at %s; set INGESTLAB_SAMPLE to run this", path)
	}
	return path
}

// publish runs one scenario to a temporary FLV, unpaced, and returns its path.
func publish(t *testing.T, o Options) string {
	t.Helper()
	o.VOD = true
	return publishTo(t, o, "out.flv")
}

// publishTo runs one scenario to a temporary file whose extension picks the container.
func publishTo(t *testing.T, o Options, name string) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), name)
	if o.Source == "" {
		o.Source = sample(t)
	}
	o.Dest = dest
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

// The only test that runs in real time, and the only one on MPEG-TS: the others publish
// FLV as fast as the sink accepts, so nothing else covers the pacer or the container that
// carries every injected track. One paced run pays for all three.
func TestPacedTSCarriesHoldAndID3(t *testing.T) {
	if testing.Short() {
		t.Skip("this one publishes in real time")
	}

	start := time.Now()
	path := publishTo(t, Options{
		Meta:         inject.Config{Types: []inject.Type{inject.ID3}, Interval: time.Second},
		HoldTracks:   []string{"video"},
		HoldAt:       []time.Duration{4 * time.Second},
		HoldDuration: after(3 * time.Second),
		CrashAfter:   after(10 * time.Second),
	}, "out.ts")
	elapsed := time.Since(start)

	// Paced means the wall clock follows the media clock. Unpaced, this takes well under
	// a second.
	if elapsed < 7*time.Second {
		t.Fatalf("10s of media published in %v, the pacer did not run", elapsed)
	}

	var audioAt, ahead time.Duration
	var id3 int
	for _, p := range readBack(t, path) {
		switch p.kind {
		case impair.Audio:
			audioAt = max(audioAt, p.dts)
		case impair.Video:
			ahead = max(ahead, audioAt-p.dts)
		default:
			id3++
		}
	}

	// How far audio had run by the time each video packet was written. The muxer
	// interleaves within 100 ms, so seconds of it can only be the hold.
	if ahead < 2*time.Second {
		t.Fatalf("audio ran at most %v ahead of video, the 3s hold did not reach the wire", ahead)
	}
	if id3 < 5 {
		t.Fatalf("%d ID3 packets arrived, want one a second", id3)
	}
}

// The band is only worth its re-encode if it survives the whole pipeline, so read the
// clock back off the picture that arrived rather than trusting that we drew it.
func TestEpochBandArrivesOnTheWire(t *testing.T) {
	if testing.Short() {
		t.Skip("this one re-encodes the video")
	}

	before := time.Now().UTC()
	path := publishTo(t, Options{
		Meta:       noMeta(),
		VOD:        true,
		Epoch:      true,
		CrashAfter: after(3 * time.Second),
	}, "out.ts")

	word := firstBandWord(t, path)
	stamp := time.Date(before.Year(), before.Month(), int(word>>27&0x1f), int(word>>22&0x1f),
		int(word>>16&0x3f), int(word>>10&0x3f), int(word&0x3ff)*int(time.Millisecond), time.UTC)

	// The month and the year are not in the word, so this can only be judged inside one.
	if d := stamp.Sub(before); d < -time.Minute || d > time.Minute {
		t.Fatalf("the band says %s, the run started at %s", stamp, before)
	}
}

// firstBandWord decodes the first video frame that arrived and samples the centre of each
// block, the way a player reads it off the rendered picture.
func firstBandWord(t *testing.T, path string) uint32 {
	t.Helper()

	src, err := media.OpenSource(path)
	if err != nil {
		t.Fatalf("reading back %s: %v", path, err)
	}
	defer src.Close()

	var vs *astiav.Stream
	for _, s := range src.Streams() {
		if s.CodecParameters().MediaType() == astiav.MediaTypeVideo {
			vs = s
			break
		}
	}
	if vs == nil {
		t.Fatal("no video stream arrived")
	}

	codec := astiav.FindDecoder(vs.CodecParameters().CodecID())
	cc := astiav.AllocCodecContext(codec)
	defer cc.Free()
	if err := vs.CodecParameters().ToCodecContext(cc); err != nil {
		t.Fatalf("configuring the decoder: %v", err)
	}
	if err := cc.Open(codec, nil); err != nil {
		t.Fatalf("opening the decoder: %v", err)
	}

	pkt, frame := astiav.AllocPacket(), astiav.AllocFrame()
	defer pkt.Free()
	defer frame.Free()

	for {
		if err := src.ReadFrame(pkt); err != nil {
			t.Fatalf("no decodable video frame arrived: %v", err)
		}
		if pkt.StreamIndex() != vs.Index() {
			pkt.Unref()
			continue
		}
		err := cc.SendPacket(pkt)
		pkt.Unref()
		if err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if err := cc.ReceiveFrame(frame); err != nil {
			if errors.Is(err, astiav.ErrEagain) {
				continue
			}
			t.Fatalf("receiving a frame: %v", err)
		}
		break
	}

	buf, err := frame.Data().Bytes(1)
	if err != nil {
		t.Fatalf("reading the frame: %v", err)
	}
	width, size := frame.Width(), epoch.BlockSize(frame.Width())
	var word uint32
	for i := range epoch.Blocks {
		word <<= 1
		if buf[(size/2)*width+i*size+size/2] < 128 {
			word |= 1
		}
	}
	return word
}

// A B-frame source has no dts on its first packets, after every seek: looping it must
// keep the timeline going rather than hand the muxer a dts from the pass before.
func TestLoopKeepsBFramesMonotonic(t *testing.T) {
	if testing.Short() {
		t.Skip("this one reads a long sample twice")
	}

	path := publish(t, Options{
		Source: sampleAt(t, "INGESTLAB_BFRAMES_SAMPLE", "tears_of_steel_1080p_gop15.mkv"),
		Meta:   noMeta(),
		Loop:   1,
	})

	var video []time.Duration
	for _, p := range readBack(t, path) {
		if p.kind == impair.Video {
			video = append(video, p.dts)
		}
	}
	for i := 1; i < len(video); i++ {
		if video[i] <= video[i-1] {
			t.Fatalf("video timestamps are not monotonic at %d: %v then %v", i, video[i-1], video[i])
		}
	}
	// Two passes of a 12-minute sample.
	if last := video[len(video)-1]; last < 24*time.Minute {
		t.Fatalf("video ends at %v, the second pass did not arrive", last)
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
