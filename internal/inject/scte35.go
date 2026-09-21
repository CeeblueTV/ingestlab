package inject

import (
	"encoding/base64"
	"encoding/binary"
	"time"
)

// crcTable is the MPEG-2 section CRC (ITU-T V.42, polynomial 0x04C11DB7, MSB first).
// hash/crc32 cannot be used: it reflects input and output.
var crcTable = func() (t [256]uint32) {
	for i := range t {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return
}()

func sectionCRC(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, x := range b {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^x]
	}
	return crc
}

// SpliceInsert is the one SCTE-35 command we emit today: an ad break, out of network, with
// an automatic return after Duration. time_signal and segmentation descriptors are not built.
type SpliceInsert struct {
	EventID   uint32
	Duration  time.Duration
	ProgramID uint16
	// PTS is the 33-bit 90 kHz splice_time. A negative value sets splice_immediate instead,
	// which is what the FLV path uses because it carries no PTS of its own.
	PTS int64
}

// Section builds the splice_info_section, CRC included.
func (s SpliceInsert) Section() []byte {
	immediate := s.PTS < 0
	size, cmdLen := 35, 0x0F
	if !immediate {
		size, cmdLen = 40, 0x14
	}

	b := make([]byte, 0, size)
	b = append(b, 0xFC)                                 // table_id
	b = append(b, 0x30|byte((size-3)>>8), byte(size-3)) // sap_type=3, section_length
	b = append(b, 0x00)                                 // protocol_version
	b = append(b, 0x00, 0x00, 0x00, 0x00, 0x00)         // encryption flags + 33-bit pts_adjustment
	b = append(b, 0x00)                                 // cw_index
	b = append(b, 0xFF, 0xF0, byte(cmdLen))             // tier=0xFFF, splice_command_length
	b = append(b, 0x05)                                 // splice_insert
	b = be32(b, s.EventID)
	b = append(b, 0x7F) // splice_event_cancel_indicator=0

	flags := byte(0xEF) // out_of_network=1, program_splice=1, duration_flag=1
	if immediate {
		flags |= 0x10
	}
	b = append(b, flags)
	if !immediate {
		b = append(b, 0xFE|byte((s.PTS>>32)&1)) // time_specified_flag=1, 33rd bit
		b = be32(b, uint32(s.PTS))
	}

	// break_duration(), auto_return=1. The duration is 33 bits: the top one goes in the
	// flags byte, the rest in the word that follows.
	ticks := int64(s.Duration) * 90000 / int64(time.Second)
	b = append(b, 0xFE|byte((ticks>>32)&1))
	b = be32(b, uint32(ticks))

	b = be16(b, s.ProgramID)
	b = append(b, 0x00, 0x00) // avail_num, avails_expected
	b = be16(b, 0)            // descriptor_loop_length
	return be32(b, sectionCRC(b))
}

// adCue is the FLV carriage of a cue: the section in base64 on an onAdCue data message.
func (s SpliceInsert) adCue(at time.Duration) ([]byte, error) {
	return amfEncode("onAdCue", amfObj{
		{"cue", base64.StdEncoding.EncodeToString(s.Section())},
		{"duration", s.Duration.Seconds()},
		{"id", "id:" + itoa(s.EventID)},
		{"time", at.Seconds()},
		{"type", "scte35"},
	})
}

// cuePoint is the AWS Elemental shape, which describes the break in fields instead of
// carrying the section.
func (s SpliceInsert) cuePoint(announce time.Duration) ([]byte, error) {
	return amfEncode("onCuePoint", amfObj{
		{"type", "event"},
		{"parameters", amfObj{
			{"avail_expected", 2},
			{"avail_num", 0},
			{"break_duration", s.Duration.Milliseconds()},
			{"messageType", "com.elementaltechnologies.scte35.1"},
			{"out_of_network_indicator", true},
			{"pre_roll_time", announce.Milliseconds()},
			{"splice_command_type", 5},
			{"splice_event_cancel_indicator", false},
			{"splice_event_id", uint32(0xC0000000)},
			{"splice_immediate", false},
			{"unique_program_id", s.EventID},
		}},
	})
}

func be16(b []byte, v uint16) []byte { return binary.BigEndian.AppendUint16(b, v) }
func be32(b []byte, v uint32) []byte { return binary.BigEndian.AppendUint32(b, v) }
