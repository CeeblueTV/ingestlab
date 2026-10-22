package media

/*
#cgo pkg-config: libavcodec
#include <libavcodec/packet.h>
#include <string.h>

static int add_new_extradata(AVPacket *pkt, const void *data, size_t size)
{
	uint8_t *dst = av_packet_new_side_data(pkt, AV_PKT_DATA_NEW_EXTRADATA, size);
	if (!dst)
		return -1;
	memcpy(dst, data, size);
	return 0;
}
*/
import "C"

import (
	"errors"
	"reflect"
	"unsafe"

	"github.com/asticode/go-astiav"
)

// attachExtradata adds config to pkt as AV_PKT_DATA_NEW_EXTRADATA. The binding has no
// setter for that side data, so this reaches the AVPacket behind it.
func attachExtradata(pkt *astiav.Packet, config []byte) error {
	if len(config) == 0 {
		return nil
	}
	c := reflect.ValueOf(pkt).Elem().FieldByName("c")
	if !c.IsValid() || c.Kind() != reflect.Pointer || c.IsNil() {
		return errors.New("no AVPacket behind the go-astiav packet")
	}
	if C.add_new_extradata((*C.AVPacket)(c.UnsafePointer()), unsafe.Pointer(&config[0]), C.size_t(len(config))) < 0 {
		return errors.New("allocating codec config side data")
	}
	return nil
}
