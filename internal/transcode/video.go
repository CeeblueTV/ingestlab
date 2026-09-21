// Package transcode is the optional shaping stage in front of the remuxer: decode the
// video, stamp the epoch band on each frame at the last instant before encoding, and
// encode again. Nothing in here is allocated unless it is switched on.
package transcode

import (
	"errors"
	"fmt"
	"time"

	"github.com/asticode/go-astiav"

	"github.com/CeeblueTV/ingestlab/internal/epoch"
	"github.com/CeeblueTV/ingestlab/internal/impair"
)

// Defaults match the tool being replaced, so the same case measured against both is
// measuring the tools and not two different encoders.
const (
	DefaultGOP     = time.Second
	DefaultBitrate = 4_000_000
)

type Config struct {
	GOP     time.Duration
	Bitrate int64
	// GlobalHeader is for the containers that carry extradata out of band, FLV among them.
	GlobalHeader bool
	Now          func() time.Time // the clock, swapped in tests
}

// Video decodes, stamps and re-encodes one video track. Every other track walks past
// untouched.
//
// It sits at the head of the chain, after the pacer: the band is read from the clock at
// the moment the frame is due to leave, and every impairment then acts on the stamped
// picture. That ordering is the whole point — a hold applied before the stamp would hide
// itself from the measurement.
type Video struct {
	cfg Config

	dec, enc *astiav.CodecContext
	frame    *astiav.Frame
	out      *astiav.Packet
	buf      []byte // the picture, out and back, reused every frame

	in    *astiav.Stream
	track *impair.Track
	base  time.Duration // loop offset, taken from the packets we are handed
}

// NewVideo builds the decoder and the encoder for one input stream. The caller adds the
// output stream from Encoder() and then calls Bind.
func NewVideo(in *astiav.Stream, cfg Config) (*Video, error) {
	if cfg.GOP == 0 {
		cfg.GOP = DefaultGOP
	}
	if cfg.Bitrate == 0 {
		cfg.Bitrate = DefaultBitrate
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	v := &Video{cfg: cfg, in: in, frame: astiav.AllocFrame(), out: astiav.AllocPacket()}
	if err := v.openDecoder(in); err != nil {
		v.Close()
		return nil, err
	}
	if err := v.openEncoder(in); err != nil {
		v.Close()
		return nil, err
	}
	return v, nil
}

func (v *Video) openDecoder(in *astiav.Stream) error {
	codec := astiav.FindDecoder(in.CodecParameters().CodecID())
	if codec == nil {
		return fmt.Errorf("no decoder for %s", in.CodecParameters().CodecID().Name())
	}
	v.dec = astiav.AllocCodecContext(codec)
	if v.dec == nil {
		return errors.New("allocating the decoder failed")
	}
	if err := in.CodecParameters().ToCodecContext(v.dec); err != nil {
		return fmt.Errorf("configuring the decoder: %w", err)
	}
	v.dec.SetTimeBase(in.TimeBase())

	// Slices, not frames: frame threading holds one frame per thread, which is latency
	// added to the number the band exists to measure.
	v.dec.SetThreadCount(0)
	v.dec.SetThreadType(astiav.ThreadTypeSlice)
	if err := v.dec.Open(codec, nil); err != nil {
		return fmt.Errorf("opening the decoder: %w", err)
	}
	return nil
}

func (v *Video) openEncoder(in *astiav.Stream) error {
	codec := astiav.FindEncoderByName("libx264")
	if codec == nil {
		return errors.New("this FFmpeg has no libx264, which the band needs to re-encode")
	}
	v.enc = astiav.AllocCodecContext(codec)
	if v.enc == nil {
		return errors.New("allocating the encoder failed")
	}

	p := in.CodecParameters()
	v.enc.SetWidth(p.Width())
	v.enc.SetHeight(p.Height())
	v.enc.SetPixelFormat(astiav.PixelFormatYuv420P)
	v.enc.SetTimeBase(in.TimeBase())

	fps := in.AvgFrameRate()
	if fps.Num() > 0 && fps.Den() > 0 {
		v.enc.SetFramerate(fps)
		v.enc.SetGopSize(int(v.cfg.GOP.Seconds() * float64(fps.Num()) / float64(fps.Den())))
	}

	// Constant bitrate, because the band is only a fixed number of milliseconds ahead of
	// the wire if the number of bytes ahead of it is fixed too.
	v.enc.SetBitRate(v.cfg.Bitrate)
	v.enc.SetRateControlMinRate(v.cfg.Bitrate)
	v.enc.SetRateControlMaxRate(v.cfg.Bitrate)
	v.enc.SetRateControlBufferSize(int(v.cfg.Bitrate))

	// No frame may be held back: Flush cannot reset a codec through this binding, so
	// anything still inside the encoder at a loop boundary would be stranded there.
	v.enc.SetMaxBFrames(0)

	// One thread on purpose: at ultrafast the slices are cheap enough that synchronising
	// them costs more than it saves, and it keeps frames from being held.
	v.enc.SetThreadCount(1)

	if v.cfg.GlobalHeader {
		v.enc.SetFlags(v.enc.Flags().Add(astiav.CodecContextFlagGlobalHeader))
	}

	opts := astiav.NewDictionary()
	defer opts.Free()
	for k, val := range map[string]string{
		"preset":   "ultrafast",
		"tune":     "zerolatency",
		"x264opts": "nal-hrd=cbr:force-cfr=1:no-scenecut:bframes=0:ref=1",
	} {
		if err := opts.Set(k, val, 0); err != nil {
			return fmt.Errorf("setting %s: %w", k, err)
		}
	}
	if err := v.enc.Open(codec, opts); err != nil {
		return fmt.Errorf("opening libx264: %w", err)
	}
	return nil
}

// Encoder exposes the context the output stream is built from.
func (v *Video) Encoder() *astiav.CodecContext { return v.enc }

// StreamIndex is the input stream this stage transcodes. It is the index and not the
// stream, because each call to FormatContext.Streams hands back new wrappers and two of
// them for the same stream are not equal.
func (v *Video) StreamIndex() int { return v.in.Index() }

// Bind attaches the track once the caller has mapped it.
func (v *Video) Bind(t *impair.Track) { v.track = t }

func (v *Video) Process(p *impair.Packet, next impair.Emit) error {
	if p.Track != v.track {
		return next(p)
	}

	// The chain carries raw stream timestamps and the loop offset separately, so read the
	// offset back off the packet rather than tracking the loop ourselves.
	if dts := p.Pkt.Dts(); dts != astiav.NoPtsValue {
		v.base = p.Media - toDuration(dts, v.track.TimeBase)
	}

	if err := v.dec.SendPacket(p.Pkt); err != nil && !errors.Is(err, astiav.ErrEagain) {
		return fmt.Errorf("decoding: %w", err)
	}
	p.Release()
	return v.drain(next)
}

// Flush drains what the codecs will give up without ending them: zerolatency and no
// B-frames mean nothing is in flight, and this binding has no avcodec_flush_buffers, so
// ending them here would leave nothing to continue the next loop pass with.
func (v *Video) Flush(next impair.Emit) error {
	if v.track == nil {
		return nil
	}
	return v.drain(next)
}

func (v *Video) drain(next impair.Emit) error {
	for {
		err := v.dec.ReceiveFrame(v.frame)
		if err != nil {
			if errors.Is(err, astiav.ErrEagain) || errors.Is(err, astiav.ErrEof) {
				return nil
			}
			return fmt.Errorf("receiving a frame: %w", err)
		}
		err = v.stampAndEncode(next)
		v.frame.Unref()
		if err != nil {
			return err
		}
	}
}

func (v *Video) stampAndEncode(next impair.Emit) error {
	if err := v.stamp(v.frame); err != nil {
		return err
	}
	// Let the encoder pick its own frame types rather than inheriting the source's.
	v.frame.SetPictureType(astiav.PictureTypeNone)

	if err := v.enc.SendFrame(v.frame); err != nil {
		return fmt.Errorf("encoding: %w", err)
	}
	return v.emit(next)
}

// stamp reads the clock as late as it can and writes the band over the top of the picture.
func (v *Video) stamp(f *astiav.Frame) error {
	if f.PixelFormat() != astiav.PixelFormatYuv420P {
		return fmt.Errorf("the band needs yuv420p, this frame is %s", f.PixelFormat().Name())
	}
	if err := f.MakeWritable(); err != nil {
		return fmt.Errorf("making the frame writable: %w", err)
	}
	// The picture goes out to a buffer and comes back patched. Reused, because a fresh one
	// per frame is 3 MB of garbage at 1080p.
	if v.buf == nil {
		size, err := f.ImageBufferSize(1)
		if err != nil {
			return fmt.Errorf("sizing the frame: %w", err)
		}
		v.buf = make([]byte, size)
	}
	if _, err := f.ImageCopyToBuffer(v.buf, 1); err != nil {
		return fmt.Errorf("reading the frame: %w", err)
	}

	epoch.DrawYUV420P(v.buf, f.Width(), f.Height(), epoch.Word(v.cfg.Now()))

	if err := f.Data().SetBytes(v.buf, 1); err != nil {
		return fmt.Errorf("writing the band: %w", err)
	}
	return nil
}

func (v *Video) emit(next impair.Emit) error {
	for {
		v.out.Unref()
		err := v.enc.ReceivePacket(v.out)
		if err != nil {
			if errors.Is(err, astiav.ErrEagain) || errors.Is(err, astiav.ErrEof) {
				return nil
			}
			return fmt.Errorf("receiving a packet: %w", err)
		}
		media := v.base + toDuration(v.out.Dts(), v.track.TimeBase)
		if err := next(impair.Own(v.out.Clone(), v.track, media)); err != nil {
			return err
		}
	}
}

func (v *Video) Close() {
	if v.enc != nil {
		v.enc.Free()
	}
	if v.dec != nil {
		v.dec.Free()
	}
	if v.frame != nil {
		v.frame.Free()
	}
	if v.out != nil {
		v.out.Free()
	}
}

func toDuration(ts int64, tb astiav.Rational) time.Duration {
	if ts == astiav.NoPtsValue || tb.Num() == 0 {
		return 0
	}
	return time.Duration(astiav.RescaleQ(ts, tb, astiav.NewRational(1, int(time.Second))))
}
