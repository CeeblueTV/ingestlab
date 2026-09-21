package inject

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"
)

// Golden sections, decoded with threefive 3.0.95 and checked field by field: splice_insert,
// out_of_network, auto_return, the break duration, the event id, and pts_time on the second.
// Regenerate only against an independent parser, never against our own output.
func TestSectionGolden(t *testing.T) {
	cases := []struct {
		name string
		cue  SpliceInsert
		want string
	}{
		{"immediate, 4s break", SpliceInsert{EventID: 1, Duration: 4 * time.Second, ProgramID: 0xC000, PTS: -1},
			"/DAgAAAAAAAAAP/wDwUAAAABf//+AAV+QMAAAAAAAOl7NY8="},
		{"splice_time at 8s, 30s break", SpliceInsert{EventID: 99, Duration: 30 * time.Second, ProgramID: 0xC000, PTS: 8 * 90000},
			"/DAlAAAAAAAAAP/wFAUAAABjf+/+AAr8gP4AKTLgwAAAAAAAksvbqA=="},
	}
	for _, c := range cases {
		if got := base64.StdEncoding.EncodeToString(c.cue.Section()); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestSectionCRCTable(t *testing.T) {
	// The first non-zero entry is the polynomial itself, which pins the table's direction.
	if crcTable[1] != 0x04C11DB7 {
		t.Fatalf("crcTable[1] = %#08x, want 0x04C11DB7", crcTable[1])
	}
}

// Running the CRC over a section including its own CRC leaves zero; that is the property a
// decoder checks, so it is the one worth asserting.
func TestSectionCRCValidates(t *testing.T) {
	for _, pts := range []int64{-1, 90000} {
		s := SpliceInsert{EventID: 42, Duration: 4 * time.Second, ProgramID: 0xC000, PTS: pts}
		if got := sectionCRC(s.Section()); got != 0 {
			t.Fatalf("pts=%d: CRC over the whole section is %#x, want 0", pts, got)
		}
	}
}

func TestSpliceInsertLayout(t *testing.T) {
	cases := []struct {
		name   string
		pts    int64
		size   int
		cmdLen byte
		flags  byte
	}{
		{"immediate", -1, 35, 0x0F, 0xFF},
		{"with splice_time", 90000, 40, 0x14, 0xEF},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := SpliceInsert{EventID: 7, Duration: 10 * time.Second, ProgramID: 0xC000, PTS: c.pts}.Section()

			if len(b) != c.size {
				t.Fatalf("section is %d bytes, want %d", len(b), c.size)
			}
			if b[0] != 0xFC {
				t.Errorf("table_id is %#x, want 0xFC", b[0])
			}
			if got := int(binary.BigEndian.Uint16(b[1:3]) & 0x0FFF); got != len(b)-3 {
				t.Errorf("section_length is %d, want %d", got, len(b)-3)
			}
			if b[12] != c.cmdLen {
				t.Errorf("splice_command_length is %#x, want %#x", b[12], c.cmdLen)
			}
			if b[13] != 0x05 {
				t.Errorf("splice_command_type is %#x, want 0x05 (splice_insert)", b[13])
			}
			if got := binary.BigEndian.Uint32(b[14:18]); got != 7 {
				t.Errorf("splice_event_id is %d, want 7", got)
			}
			if b[18] != 0x7F {
				t.Errorf("cancel indicator byte is %#x, want 0x7F", b[18])
			}
			if b[19] != c.flags {
				t.Errorf("splice_insert flags are %#x, want %#x", b[19], c.flags)
			}

			// break_duration sits right after the optional splice_time.
			off := 20
			if c.pts >= 0 {
				if b[off]&0x80 == 0 {
					t.Errorf("time_specified_flag is not set")
				}
				if got := binary.BigEndian.Uint32(b[off+1 : off+5]); int64(got) != c.pts {
					t.Errorf("splice_time is %d, want %d", got, c.pts)
				}
				off += 5
			}
			if b[off]&0x80 == 0 {
				t.Errorf("auto_return is not set")
			}
			if got := binary.BigEndian.Uint32(b[off+1 : off+5]); got != 10*90000 {
				t.Errorf("break_duration is %d ticks, want %d", got, 10*90000)
			}
			if got := binary.BigEndian.Uint16(b[off+5 : off+7]); got != 0xC000 {
				t.Errorf("unique_program_id is %#x, want 0xC000", got)
			}
			if got := binary.BigEndian.Uint16(b[off+9 : off+11]); got != 0 {
				t.Errorf("descriptor_loop_length is %d, want 0", got)
			}
		})
	}
}

// break_duration is 33 bits: the top one rides in the flags byte, not in the 32-bit word.
func TestBreakDurationTopBit(t *testing.T) {
	// 90000 ticks per second, so the 33rd bit turns over just under 47722 s.
	long := SpliceInsert{Duration: 50000 * time.Second, PTS: -1}.Section()
	if long[20]&0x01 != 1 {
		t.Error("the 33rd duration bit should be set for a 50000 s break")
	}

	// A break whose tick count is odd must not set it. 11112 ns is one 90 kHz tick.
	odd := SpliceInsert{Duration: 11112 * time.Nanosecond, PTS: -1}.Section()
	if odd[20]&0x01 != 0 {
		t.Error("an odd tick count leaked into the 33rd bit")
	}
	if got := binary.BigEndian.Uint32(odd[21:25]); got != 1 {
		t.Errorf("break_duration is %d ticks, want 1", got)
	}
}
