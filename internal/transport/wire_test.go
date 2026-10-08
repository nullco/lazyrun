package transport

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestWireRoundTripAndInvalidFrames(t *testing.T) {
	var b bytes.Buffer
	req := Request{Version: ProtocolVersion, ID: "request", Operation: ListState, Payload: []byte(`{}`)}
	if err := writeFrame(&b, req); err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := readFrame(&b, &got); err != nil || got.ID != req.ID || got.Operation != req.Operation {
		t.Fatal(got, err)
	}
	for _, size := range []uint32{0, MaxFrameBytes + 1} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		if err := readFrame(bytes.NewReader(header[:]), &got); err == nil {
			t.Fatal("accepted frame length", size)
		}
	}
	if err := Decode([]byte(`{"version":1,"unknown":true}`), &got); err == nil {
		t.Fatal("accepted unknown envelope field")
	}
	if err := Decode([]byte(`{} {}`), &got); err == nil {
		t.Fatal("accepted multiple JSON values")
	}
	if err := writeFrame(&b, strings.Repeat("x", MaxFrameBytes+1)); err == nil {
		t.Fatal("wrote oversized frame")
	}
}
