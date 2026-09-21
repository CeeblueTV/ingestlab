// Package epoch writes the EpochTime band: a row of 32 black-and-white blocks across the
// top of the picture stating the wall clock at which the frame was stamped. A player reads
// it off the rendered frame, so the number it yields has been through every stage the
// viewer's video goes through.
//
// The layout is fixed by the decoder in webrtc-client: 32 bits MSB first, black = 1,
// day(5) hour(5) minute(6) second(6) millisecond(10) UTC, block size floor(width/32).
package epoch

import "time"

// Blocks is the number of blocks the word is spread over, one bit each.
const Blocks = 32

// Limited-range YUV. The band is drawn in greyscale, so both chroma planes are neutral.
const (
	black   = 16
	white   = 235
	neutral = 128
)

// Word packs a UTC instant the way the decoder unpacks it. The month and year are not
// carried, so the value wraps every month and a reader can only difference within one.
func Word(t time.Time) uint32 {
	t = t.UTC()
	return uint32(t.Day())<<27 |
		uint32(t.Hour())<<22 |
		uint32(t.Minute())<<16 |
		uint32(t.Second())<<10 |
		uint32(t.Nanosecond()/int(time.Millisecond))
}

// BlockSize is the side of one block, and 0 when the picture is too small to carry the
// band. Floor division is what the decoder does, so a width that is not a multiple of 32
// leaves a margin on the right rather than shifting the blocks.
func BlockSize(width int) int { return width / Blocks }

// DrawYUV420P stamps the word over the top-left of a packed YUV420P image, the layout
// av_image_copy_to_buffer produces at align 1: Y at full size, then U and V at half in
// both directions.
//
// It writes only the rows the band covers, so the cost is the band's area, not the frame's.
func DrawYUV420P(buf []byte, width, height int, word uint32) {
	size := BlockSize(width)
	if size == 0 || size > height {
		return
	}

	uvWidth, uvHeight := (width+1)/2, (height+1)/2
	uOff := width * height
	vOff := uOff + uvWidth*uvHeight

	for i := range Blocks {
		x := i * size
		if x+size > width {
			break
		}

		luma, chroma := byte(white), byte(neutral)
		if word>>(31-i)&1 == 1 {
			luma = black
		}

		for row := range size {
			line := buf[row*width+x:]
			for col := range size {
				line[col] = luma
			}
		}
		// Chroma is subsampled 2:1, so the same area is half as wide and half as tall.
		for row := range (size + 1) / 2 {
			at := row*uvWidth + x/2
			for col := range (size + 1) / 2 {
				buf[uOff+at+col] = chroma
				buf[vOff+at+col] = chroma
			}
		}
	}
}
