package epoch

import (
	"testing"
	"time"
)

// decode reads the band back the way a player does: sample the centre of each block and
// call anything dark a 1.
func decode(buf []byte, width int) uint32 {
	size := BlockSize(width)
	var word uint32
	for i := range Blocks {
		x := i*size + size/2
		y := size / 2
		word <<= 1
		if buf[y*width+x] < 128 {
			word |= 1
		}
	}
	return word
}

func newPicture(width, height int, fill byte) []byte {
	buf := make([]byte, width*height+2*((width+1)/2)*((height+1)/2))
	for i := range buf {
		buf[i] = fill
	}
	return buf
}

// The packing is the decoder's, not ours: it is the one thing here that cannot change
// without shipping a new webrtc-client in lockstep. The value is the first frame of
// ~/samples/bbb_1080p-epoch.mkv, which the filter we are replacing stamped.
const reference = uint32(0x13b26bb4)

func TestWordPacking(t *testing.T) {
	at := time.Date(2026, 9, 2, 14, 50, 26, 948*int(time.Millisecond), time.UTC)
	if got := Word(at); got != reference {
		t.Fatalf("Word = %#x, want %#x", got, reference)
	}
}

// A local time must be stamped as UTC, or every reader is out by the offset.
func TestWordIsUTC(t *testing.T) {
	zone := time.FixedZone("UTC+2", 2*60*60)
	at := time.Date(2026, 9, 2, 16, 50, 26, 948*int(time.Millisecond), zone)
	if got := Word(at); got != reference {
		t.Fatalf("Word = %#x, want %#x — the zone was not converted", got, reference)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, w := range []int{1920, 1280, 640} {
		h := w * 9 / 16
		at := time.Date(2026, 9, 2, 23, 59, 59, 999*int(time.Millisecond), time.UTC)
		buf := newPicture(w, h, 90)
		DrawYUV420P(buf, w, h, Word(at))
		if got, want := decode(buf, w), Word(at); got != want {
			t.Errorf("%dx%d: decoded %#x, want %#x", w, h, got, want)
		}
	}
}

// Floor division: 1918/32 is 59, so the band covers 1888 pixels and leaves a margin
// rather than stretching to fit.
func TestNonMultipleWidthKeepsBlockGrid(t *testing.T) {
	w, h := 1918, 1080
	if got := BlockSize(w); got != 59 {
		t.Fatalf("BlockSize(%d) = %d, want 59", w, got)
	}
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	buf := newPicture(w, h, 90)
	DrawYUV420P(buf, w, h, Word(at))
	if got, want := decode(buf, w), Word(at); got != want {
		t.Fatalf("decoded %#x, want %#x", got, want)
	}
	if buf[w-1] != 90 {
		t.Errorf("the margin past the last block was painted")
	}
}

func TestDrawLeavesTheRestOfThePictureAlone(t *testing.T) {
	w, h := 640, 360
	buf := newPicture(w, h, 90)
	DrawYUV420P(buf, w, h, 0xFFFFFFFF)

	size := BlockSize(w)
	for y := size; y < h; y++ {
		for x := range w {
			if buf[y*w+x] != 90 {
				t.Fatalf("luma at %d,%d was overwritten below the band", x, y)
			}
		}
	}

	// Inside the band the chroma is neutral, so the blocks are grey rather than tinted.
	uOff := w * h
	if buf[uOff] != neutral {
		t.Errorf("chroma in the band is %d, want %d", buf[uOff], neutral)
	}
	// And untouched below it.
	if got := buf[uOff+((size+1)/2)*((w+1)/2)]; got != 90 {
		t.Errorf("chroma below the band is %d, want it untouched", got)
	}
}

// A picture too short for one block carries no band at all rather than a clipped one.
func TestPictureTooSmallIsLeftAlone(t *testing.T) {
	w, h := 640, 8
	buf := newPicture(w, h, 90)
	DrawYUV420P(buf, w, h, 0xFFFFFFFF)
	for i, b := range buf {
		if b != 90 {
			t.Fatalf("byte %d was written on a picture too small for the band", i)
		}
	}
}
