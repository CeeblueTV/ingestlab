// Package media wraps libavformat demuxing and muxing.
package media

import (
	"errors"
	"fmt"

	"github.com/asticode/go-astiav"
)

// Source is a demuxer reading a file or URL.
type Source struct {
	fc *astiav.FormatContext
}

func OpenSource(url string) (*Source, error) {
	fc := astiav.AllocFormatContext()
	if fc == nil {
		return nil, errors.New("allocating input format context failed")
	}
	if err := fc.OpenInput(url, nil, nil); err != nil {
		fc.Free()
		return nil, fmt.Errorf("opening %s: %w", url, err)
	}
	if err := fc.FindStreamInfo(nil); err != nil {
		fc.CloseInput()
		fc.Free()
		return nil, fmt.Errorf("finding stream info: %w", err)
	}
	return &Source{fc: fc}, nil
}

func (s *Source) Streams() []*astiav.Stream { return s.fc.Streams() }

// ReadFrame returns astiav.ErrEof at the end of the input.
func (s *Source) ReadFrame(pkt *astiav.Packet) error { return s.fc.ReadFrame(pkt) }

// Rewind seeks back to the start so the input can be looped.
func (s *Source) Rewind() error {
	return s.fc.SeekFrame(-1, 0, astiav.NewSeekFlags(astiav.SeekFlagBackward))
}

func (s *Source) Close() {
	s.fc.CloseInput()
	s.fc.Free()
}
