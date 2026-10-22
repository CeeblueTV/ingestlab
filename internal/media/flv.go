package media

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// FLV tag types.
const (
	FLVAudio  = 8
	FLVVideo  = 9
	FLVScript = 18
)

// FLVTag is one tag of an FLV byte stream.
type FLVTag struct {
	Type      byte
	Timestamp uint32 // milliseconds
	Body      []byte
	Raw       []byte // the whole tag, its previous-tag-size included
}

// SplitFLV cuts an FLV byte stream into its file header and its tags.
func SplitFLV(b []byte) (header []byte, tags []FLVTag, err error) {
	if len(b) < 9 || string(b[:3]) != "FLV" {
		return nil, nil, errors.New("not an FLV stream")
	}
	n := int(binary.BigEndian.Uint32(b[5:9])) + 4 // the first previous-tag-size belongs to it
	if n < 13 || len(b) < n {
		return nil, nil, fmt.Errorf("FLV header of %d bytes in a %d-byte stream", n, len(b))
	}
	header, b = b[:n], b[n:]

	for len(b) > 0 {
		if len(b) < 11 {
			return nil, nil, fmt.Errorf("truncated FLV tag header of %d bytes", len(b))
		}
		size := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
		end := 11 + size + 4
		if len(b) < end {
			return nil, nil, fmt.Errorf("truncated FLV tag: %d of %d bytes", len(b), end)
		}
		tags = append(tags, FLVTag{
			Type:      b[0] & 0x1f, // the top bits are the filter flag and reserved
			Timestamp: uint32(b[7])<<24 | uint32(b[4])<<16 | uint32(b[5])<<8 | uint32(b[6]),
			Body:      b[11 : 11+size],
			Raw:       b[:end],
		})
		b = b[end:]
	}
	return header, tags, nil
}

// withoutMediaTags drops the audio and video tags from a header flvenc wrote: at that point
// they can only be codec config.
func withoutMediaTags(b []byte) ([]byte, error) {
	header, tags, err := SplitFLV(b)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), header...)
	for _, t := range tags {
		if t.Type != FLVAudio && t.Type != FLVVideo {
			out = append(out, t.Raw...)
		}
	}
	return out, nil
}
