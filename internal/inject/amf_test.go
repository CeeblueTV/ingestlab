package inject

import (
	"bytes"
	"strings"
	"testing"
)

func TestAMFEncode(t *testing.T) {
	got, err := amfEncode("onMetaData", amfObj{{"index", 1}, {"live", true}})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x02, 0x00, 0x0A, 'o', 'n', 'M', 'e', 't', 'a', 'D', 'a', 't', 'a',
		0x03,
		0x00, 0x05, 'i', 'n', 'd', 'e', 'x', 0x00, 0x3F, 0xF0, 0, 0, 0, 0, 0, 0,
		0x00, 0x04, 'l', 'i', 'v', 'e', 0x01, 0x01,
		0x00, 0x00, 0x09,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got  %x\nwant %x", got, want)
	}
}

// Object members keep their order: AMF0 is a list of pairs and consumers rely on it, which
// is why this cannot be a map.
func TestAMFKeepsOrder(t *testing.T) {
	got, err := amfEncode(amfObj{{"zebra", 1}, {"apple", 2}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Index(got, []byte("zebra")) > bytes.Index(got, []byte("apple")) {
		t.Fatal("members were reordered")
	}
}

// A payload larger than any sane fixed buffer must still come out whole.
func TestAMFGrowsWithThePayload(t *testing.T) {
	long := strings.Repeat("x", 4000)
	got, err := amfEncode("onTextData", long)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 4000 {
		t.Fatalf("payload is %d bytes, the string alone is %d", len(got), len(long))
	}
}

// A string longer than AMF0's 16-bit length must be refused, not truncated.
func TestAMFRejectsOverlongString(t *testing.T) {
	if _, err := amfEncode(strings.Repeat("x", 70000)); err == nil {
		t.Fatal("a 70000-byte string was accepted")
	}
}
