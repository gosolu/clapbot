package feishu

import (
	"bytes"
	"testing"
)

func TestPBFrameEncodeDecode(t *testing.T) {
	orig := pbFrame{
		SeqID:           42,
		LogID:           99,
		Service:         1,
		Method:          1,
		Headers:         []pbHeader{{Key: "type", Value: "event"}},
		PayloadEncoding: "json",
		PayloadType:     "application/json",
		Payload:         []byte(`{"header":{"event_type":"im.message.receive_v1"}}`),
	}
	encoded := encodePBFrame(orig)
	decoded, err := decodePBFrame(encoded)
	if err != nil {
		t.Fatalf("decodePBFrame: %v", err)
	}
	if decoded.SeqID != orig.SeqID || decoded.Method != orig.Method || !bytes.Equal(decoded.Payload, orig.Payload) {
		t.Fatalf("decoded frame mismatch: %+v vs %+v", decoded, orig)
	}
	if len(decoded.Headers) != 1 || decoded.Headers[0].Key != "type" || decoded.Headers[0].Value != "event" {
		t.Fatalf("decoded headers mismatch: %+v", decoded.Headers)
	}
}
