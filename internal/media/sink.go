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

	deferConfig bool
	config      map[int][]byte // codec config waiting for its stream's first packet
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

// NeedsGlobalHeader reports whether the container carries extradata out of band, which
// an encoder has to be told before it opens.
func (s *Sink) NeedsGlobalHeader() bool {
	return s.fc.OutputFormat().Flags().Has(astiav.IOFormatFlagGlobalheader)
}

// AddEncodedStream creates an output stream described by an encoder rather than by a
// source stream, for a track we re-encode on the way through.
func (s *Sink) AddEncodedStream(cc *astiav.CodecContext) (*astiav.Stream, error) {
	out := s.fc.NewStream(nil)
	if out == nil {
		return nil, errors.New("output stream is nil")
	}
	if err := cc.ToCodecParameters(out.CodecParameters()); err != nil {
		return nil, fmt.Errorf("copying encoder parameters: %w", err)
	}
	out.CodecParameters().SetCodecTag(0)
	out.SetTimeBase(cc.TimeBase())
	return out, nil
}

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

// DeferCodecConfig keeps each stream's codec config (the FLV sequence header) out of the
// container header and sends it with that stream's first packet, the way a live encoder
// that has not produced anything yet would. Call it before Connect.
func (s *Sink) DeferCodecConfig() error {
	if name := s.FormatName(); name != "flv" {
		return fmt.Errorf("deferring codec config needs FLV output, not %s", name)
	}
	s.deferConfig = true
	return nil
}

// Connect opens the output and writes the container header.
func (s *Sink) Connect() error {
	if !s.fc.OutputFormat().Flags().Has(astiav.IOFormatFlagNofile) {
		opts := astiav.NewDictionary()
		defer opts.Free()
		// Caps a write to a file; the network protocols do not read it.
		if err := opts.Set("blocksize", "8192", 0); err != nil {
			return err
		}
		// Nagle is on by default in FFmpeg's TCP, which holds a partial segment back until
		// the previous one is acknowledged — a round trip of delay added to a publisher
		// whose whole purpose is measuring delay. Unknown to the non-TCP protocols, which
		// ignore it.
		if err := opts.Set("tcp_nodelay", "1", 0); err != nil {
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
	write := s.fc.WriteHeader
	if s.deferConfig {
		write = s.writeHeaderWithoutConfig
	}
	if err := write(d); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	s.headerWrote = true
	return nil
}

// writeHeaderWithoutConfig writes the header through a scratch buffer so the sequence
// headers flvenc always puts in it can be dropped, then arms each stream to have flvenc
// write its own in front of its first packet.
func (s *Sink) writeHeaderWithoutConfig(d *astiav.Dictionary) error {
	if s.io == nil {
		return errors.New("deferring codec config needs an output to write to")
	}
	var header []byte
	scratch, err := astiav.AllocIOContext(4096, true, nil, nil, func(b []byte) (int, error) {
		header = append(header, b...)
		return len(b), nil
	})
	if err != nil {
		return fmt.Errorf("allocating header buffer: %w", err)
	}
	defer scratch.Free()

	s.fc.SetPb(scratch)
	err = s.fc.WriteHeader(d)
	scratch.Flush()
	s.fc.SetPb(s.io)
	if err != nil {
		return err
	}

	kept, err := withoutMediaTags(header)
	if err != nil {
		return fmt.Errorf("filtering header: %w", err)
	}
	s.io.Write(kept)
	s.io.Flush()

	s.config = map[int][]byte{}
	for _, st := range s.fc.Streams() {
		p := st.CodecParameters()
		mt := p.MediaType()
		config := p.ExtraData()
		if (mt != astiav.MediaTypeAudio && mt != astiav.MediaTypeVideo) || len(config) == 0 {
			continue
		}
		s.config[st.Index()] = config
		// flvenc writes a codec header only for side data that changes the extradata, and an
		// empty one would make it insert extract_extradata, so anything else will do.
		if err := p.SetExtraData([]byte{0}); err != nil {
			return fmt.Errorf("clearing codec config: %w", err)
		}
	}
	return nil
}

func (s *Sink) Write(pkt *astiav.Packet) error {
	if config, ok := s.config[pkt.StreamIndex()]; ok {
		delete(s.config, pkt.StreamIndex())
		if err := attachExtradata(pkt, config); err != nil {
			return err
		}
	}
	return s.fc.WriteInterleavedFrame(pkt)
}

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
