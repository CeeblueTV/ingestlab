package media

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/asticode/go-astiav"
)

var (
	avcC = []byte{0x01, 0x64, 0x00, 0x1f, 0xff, 0xe1, 0x00, 0x04, 0x67, 0x64, 0x00, 0x1f,
		0x01, 0x00, 0x02, 0x68, 0xee}
	asc = []byte{0x11, 0x90} // AAC-LC, 48 kHz, stereo
)

// writeFLV muxes an H.264 and an AAC stream to a file, audio starting at 1s and video at
// 2.5s, and returns its tags.
func writeFLV(t *testing.T, deferConfig bool) []FLVTag {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.flv")
	s, err := OpenSink(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	video := s.fc.NewStream(nil)
	vp := video.CodecParameters()
	vp.SetMediaType(astiav.MediaTypeVideo)
	vp.SetCodecID(astiav.CodecIDH264)
	vp.SetWidth(64)
	vp.SetHeight(64)
	audio := s.fc.NewStream(nil)
	ap := audio.CodecParameters()
	ap.SetMediaType(astiav.MediaTypeAudio)
	ap.SetCodecID(astiav.CodecIDAac)
	ap.SetSampleRate(48000)
	ap.SetChannelLayout(astiav.ChannelLayoutStereo)
	for _, c := range []struct {
		p    *astiav.CodecParameters
		data []byte
	}{{vp, avcC}, {ap, asc}} {
		if err := c.p.SetExtraData(c.data); err != nil {
			t.Fatal(err)
		}
	}

	if deferConfig {
		if err := s.DeferCodecConfig(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Connect(); err != nil {
		t.Fatal(err)
	}

	write := func(st *astiav.Stream, ms int64) {
		pkt := astiav.AllocPacket()
		defer pkt.Free()
		if err := pkt.FromData([]byte{0x00, 0x00, 0x00, 0x02, 0x09, 0xf0}); err != nil {
			t.Fatal(err)
		}
		pkt.SetStreamIndex(st.Index())
		pkt.SetDts(astiav.RescaleQ(ms, astiav.NewRational(1, 1000), st.TimeBase()))
		pkt.SetPts(pkt.Dts())
		pkt.SetFlags(astiav.NewPacketFlags(astiav.PacketFlagKey))
		if err := s.Write(pkt); err != nil {
			t.Fatal(err)
		}
	}
	for ms := int64(1000); ms <= 4000; ms += 500 {
		write(audio, ms)
		if ms >= 2500 {
			write(video, ms)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, tags, err := SplitFLV(b)
	if err != nil {
		t.Fatal(err)
	}
	return tags
}

// isConfig reports an AVC or AAC sequence header.
func isConfig(tag FLVTag) bool {
	switch tag.Type {
	case FLVVideo:
		return len(tag.Body) > 1 && tag.Body[0]&0x0f == 7 && tag.Body[1] == 0
	case FLVAudio:
		return len(tag.Body) > 1 && tag.Body[0]>>4 == 10 && tag.Body[1] == 0
	}
	return false
}

// Without the option, flvenc sends both sequence headers at connect.
func TestCodecConfigAtConnect(t *testing.T) {
	tags := writeFLV(t, false)
	if len(tags) < 3 || tags[0].Type != FLVScript || !isConfig(tags[1]) || !isConfig(tags[2]) {
		t.Fatalf("want onMetaData then both sequence headers, got %v", describe(tags))
	}
	if tags[1].Timestamp != 0 || tags[2].Timestamp != 0 {
		t.Fatalf("sequence headers at %d and %d ms, want 0", tags[1].Timestamp, tags[2].Timestamp)
	}
}

// With it, nothing but onMetaData goes out until a track's first packet, and that packet
// comes right behind its own sequence header, sent once and with the real config.
func TestDeferCodecConfig(t *testing.T) {
	tags := writeFLV(t, true)
	if tags[0].Type != FLVScript {
		t.Fatalf("first tag is %d, want onMetaData", tags[0].Type)
	}

	for _, c := range []struct {
		typ    byte
		at     uint32
		config []byte
		skip   int // flag and packet type bytes before the config
	}{{FLVVideo, 2500, avcC, 5}, {FLVAudio, 1000, asc, 2}} {
		first := -1
		configs := 0
		for i, tag := range tags {
			if tag.Type != c.typ {
				continue
			}
			if first < 0 {
				first = i
			}
			if isConfig(tag) {
				configs++
			}
		}
		if first < 0 || configs != 1 {
			t.Fatalf("type %d: first tag %d, %d sequence headers, want one: %v", c.typ, first, configs, describe(tags))
		}
		seq := tags[first]
		if !isConfig(seq) || seq.Timestamp != c.at {
			t.Fatalf("type %d: first tag at %d ms, sequence header %v, want one at %d ms",
				c.typ, seq.Timestamp, isConfig(seq), c.at)
		}
		if !bytes.Equal(seq.Body[c.skip:], c.config) {
			t.Fatalf("type %d: config %x, want %x", c.typ, seq.Body[c.skip:], c.config)
		}
		next := tags[first+1]
		if next.Type != c.typ || isConfig(next) || next.Timestamp != c.at {
			t.Fatalf("type %d: sequence header not followed by the first packet: %v", c.typ, describe(tags))
		}
	}
}

func TestDeferCodecConfigNeedsFLV(t *testing.T) {
	s, err := OpenSink(filepath.Join(t.TempDir(), "out.ts"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DeferCodecConfig(); err == nil {
		t.Fatal("deferring codec config on MPEG-TS should fail")
	}
}

func describe(tags []FLVTag) []string {
	var out []string
	for _, tag := range tags {
		kind := map[byte]string{FLVAudio: "audio", FLVVideo: "video", FLVScript: "script"}[tag.Type]
		if isConfig(tag) {
			kind += "-config"
		}
		out = append(out, fmt.Sprintf("%s@%d", kind, tag.Timestamp))
	}
	return out
}
