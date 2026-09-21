package media

import (
	"errors"
	"fmt"
	"strings"

	"github.com/asticode/go-astiav"
)

// Sink is a muxer publishing to a URL or writing a file.
type Sink struct {
	fc          *astiav.FormatContext
	io          *astiav.IOContext
	url         string
	headerWrote bool
	closed      bool
}

// OpenSink allocates the muxer. format overrides the container guessed from the URL.
func OpenSink(url, format string) (*Sink, error) {
	if format == "" {
		format = FormatForURL(url)
	}
	fc, err := astiav.AllocOutputFormatContext(nil, format, url)
	if err != nil {
		return nil, fmt.Errorf("allocating output format context: %w", err)
	}
	if fc == nil {
		return nil, errors.New("output format context is nil")
	}
	return &Sink{fc: fc, url: url}, nil
}

// FormatForURL maps a scheme to its container, since libavformat can only guess from
// a file extension and a publish URL has none.
func FormatForURL(url string) string {
	scheme, _, ok := strings.Cut(url, "://")
	if !ok {
		return "" // a path: let libavformat guess from the extension
	}
	switch strings.ToLower(scheme) {
	case "rtmp", "rtmps", "rtmpt", "rtmpts":
		return "flv"
	case "srt", "udp", "rist":
		return "mpegts"
	default:
		return ""
	}
}

func (s *Sink) FormatName() string { return s.fc.OutputFormat().Name() }

// AddStream copies a source stream's parameters into a new output stream.
func (s *Sink) AddStream(src *astiav.Stream) (*astiav.Stream, error) {
	out := s.fc.NewStream(nil)
	if out == nil {
		return nil, errors.New("output stream is nil")
	}
	if err := src.CodecParameters().Copy(out.CodecParameters()); err != nil {
		return nil, fmt.Errorf("copying codec parameters: %w", err)
	}
	// The source tag is meaningless in another container and libavformat rejects it.
	out.CodecParameters().SetCodecTag(0)
	out.SetTimeBase(src.TimeBase())
	return out, nil
}

// AddDataStream creates an output stream for a metadata track we inject ourselves.
// FLV wants AV_CODEC_ID_NONE, which makes flvenc write our payload into a META tag as is;
// MPEG-TS wants the real codec so it gets its own PID and stream type.
func (s *Sink) AddDataStream(codec astiav.CodecID, tb astiav.Rational) (*astiav.Stream, error) {
	out := s.fc.NewStream(nil)
	if out == nil {
		return nil, errors.New("output stream is nil")
	}
	p := out.CodecParameters()
	p.SetMediaType(astiav.MediaTypeData)
	p.SetCodecID(codec)
	p.SetCodecTag(0)
	out.SetTimeBase(tb)
	return out, nil
}

// Connect opens the output and writes the container header.
func (s *Sink) Connect() error {
	if !s.fc.OutputFormat().Flags().Has(astiav.IOFormatFlagNofile) {
		opts := astiav.NewDictionary()
		defer opts.Free()
		if err := opts.Set("blocksize", "8192", 0); err != nil {
			return err
		}
		io, err := astiav.OpenIOContext(s.url, astiav.NewIOContextFlags(astiav.IOContextFlagWrite), nil, opts)
		if err != nil {
			return fmt.Errorf("opening io context: %w", err)
		}
		s.io = io
		s.fc.SetPb(io)
	}

	d := astiav.NewDictionary()
	defer d.Free()
	// Flush every packet: a live publisher must not sit on data waiting for a buffer to fill.
	if err := d.Set("fflags", "+flush_packets+nobuffer", 0); err != nil {
		return err
	}
	// How long the muxer holds media waiting for a starved track. The 10 s default would
	// stall everything behind a sparse metadata track, and every millisecond of it is delay
	// we add and then measure. It is also contamination: interleave buffering blunts the
	// delivery impairments this tool exists to inject, so keep it as low as the container
	// tolerates.
	if err := d.Set("max_interleave_delta", "100000", 0); err != nil {
		return err
	}
	if err := s.fc.WriteHeader(d); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	s.headerWrote = true
	return nil
}

func (s *Sink) Write(pkt *astiav.Packet) error { return s.fc.WriteInterleavedFrame(pkt) }

// Close writes the trailer and releases the muxer, so the session unpublishes cleanly.
func (s *Sink) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true

	var err error
	if s.headerWrote {
		err = s.fc.WriteTrailer()
	}
	if s.io != nil {
		if cerr := s.io.Close(); err == nil {
			err = cerr
		}
	}
	s.fc.Free()
	return err
}

// Abort drops the output without a trailer and without unpublishing, which is what
// --crash-after has to look like from the server's side.
func (s *Sink) Abort() {
	if s.closed {
		return
	}
	s.closed = true
	if s.io != nil {
		s.io.Close()
	}
	s.fc.Free()
}
