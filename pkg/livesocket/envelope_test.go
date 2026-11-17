package livesocket

import (
	"strings"
	"testing"
)

// TestDecodeEnvelope mirrors the rejections of @bsv/authsocket's
// parseAuthSocketEventPayload.
func TestDecodeEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		ok      bool
	}{
		{"event with data", `{"eventName":"joinRoom","data":"r"}`, true},
		{"event without data", `{"eventName":"authenticated"}`, true},
		{"256-byte name", `{"eventName":"` + strings.Repeat("a", 256) + `"}`, true},
		{"257-byte name", `{"eventName":"` + strings.Repeat("a", 257) + `"}`, false},
		{"reserved disconnect", `{"eventName":"disconnect"}`, false},
		{"reserved newListener", `{"eventName":"newListener"}`, false},
		{"empty name", `{"eventName":""}`, false},
		{"numeric name", `{"eventName":1}`, false},
		{"control character", `{"eventName":"a\u0007"}`, false},
		{"C1 control", `{"eventName":"a\u0085"}`, false},
		{"bidi override", `{"eventName":"a\u202e"}`, false},
		{"bidi isolate", `{"eventName":"a\u2066"}`, false},
		{"extra field", `{"eventName":"x","extra":1}`, false},
		{"not an object", `["x"]`, false},
		{"invalid UTF-8", "{\"eventName\":\"x\",\"data\":\"\xff\"}", false},
		{"over 1 MiB", `{"eventName":"x","data":"` + strings.Repeat("a", 1<<20) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeEnvelope([]byte(tc.payload))
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v, err=%v", tc.ok, err)
			}
		})
	}
}
