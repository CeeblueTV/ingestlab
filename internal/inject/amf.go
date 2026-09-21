package inject

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

// AMF0 markers, only the ones an FLV data track needs.
const (
	amfNumber  = 0x00
	amfBoolean = 0x01
	amfString  = 0x02
	amfObject  = 0x03
	amfNull    = 0x05
	amfObjEnd  = 0x09
)

// amfPair is one object member. AMF0 objects are ordered and consumers key off that order,
// so this cannot be a map.
type amfPair struct {
	Key string
	Val any
}

type amfObj []amfPair

// amfEncode writes the values back to back, which is the shape of an FLV data message:
// the method name, then its argument.
func amfEncode(values ...any) ([]byte, error) {
	var b bytes.Buffer
	for _, v := range values {
		if err := amfWrite(&b, v); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func amfWrite(b *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case nil:
		b.WriteByte(amfNull)
	case string:
		b.WriteByte(amfString)
		return amfWriteString(b, v)
	case bool:
		b.WriteByte(amfBoolean)
		if v {
			b.WriteByte(1)
		} else {
			b.WriteByte(0)
		}
	case float64:
		b.WriteByte(amfNumber)
		return binary.Write(b, binary.BigEndian, math.Float64bits(v))
	case int:
		return amfWrite(b, float64(v))
	case int64:
		return amfWrite(b, float64(v))
	case uint32:
		return amfWrite(b, float64(v))
	case amfObj:
		b.WriteByte(amfObject)
		for _, p := range v {
			if err := amfWriteString(b, p.Key); err != nil {
				return err
			}
			if err := amfWrite(b, p.Val); err != nil {
				return err
			}
		}
		if err := amfWriteString(b, ""); err != nil {
			return err
		}
		b.WriteByte(amfObjEnd)
	default:
		return fmt.Errorf("no AMF0 encoding for %T", v)
	}
	return nil
}

func amfWriteString(b *bytes.Buffer, s string) error {
	if len(s) > math.MaxUint16 {
		return fmt.Errorf("AMF0 string of %d bytes is too long", len(s))
	}
	if err := binary.Write(b, binary.BigEndian, uint16(len(s))); err != nil {
		return err
	}
	_, err := b.WriteString(s)
	return err
}
