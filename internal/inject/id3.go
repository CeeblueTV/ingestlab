package inject

// ID3v2.4 with a single TXXX frame, which is what MPEG-TS timed metadata carries.

func synchsafe(v int) []byte {
	return []byte{byte(v>>21) & 0x7F, byte(v>>14) & 0x7F, byte(v>>7) & 0x7F, byte(v) & 0x7F}
}

func txxxFrame(description, value string) []byte {
	payload := []byte{0x03} // UTF-8
	payload = append(payload, description...)
	payload = append(payload, 0x00)
	payload = append(payload, value...)

	frame := make([]byte, 0, 10+len(payload))
	frame = append(frame, "TXXX"...)
	frame = append(frame, synchsafe(len(payload))...)
	frame = append(frame, 0x00, 0x00) // frame flags
	return append(frame, payload...)
}

func id3Tag(frames ...[]byte) []byte {
	var body []byte
	for _, f := range frames {
		body = append(body, f...)
	}
	tag := make([]byte, 0, 10+len(body))
	tag = append(tag, "ID3"...)
	tag = append(tag, 0x04, 0x00, 0x00) // v2.4, revision 0, no flags
	tag = append(tag, synchsafe(len(body))...)
	return append(tag, body...)
}
