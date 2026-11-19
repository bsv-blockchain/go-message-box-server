package livesocket_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"testing"

	"github.com/bsv-blockchain/go-sdk/auth"

	"github.com/bsv-blockchain/go-message-box-server/pkg/livesocket"
)

// testdata/ts-authmessages.json holds a BRC-103 handshake and one general
// message between two @bsv/sdk 3.2.1 Peers, each frame as JSON.stringify
// leaves the AuthMessage that @bsv/authsocket hands to socket.emit. The
// general message's payload is a MessageBox-style {eventName, data} envelope.
type tsFrames struct {
	Client string           `json:"client"`
	Server string           `json:"server"`
	Frames []map[string]any `json:"frames"`
}

func loadTSFrames(t *testing.T) tsFrames {
	t.Helper()
	b, err := os.ReadFile("testdata/ts-authmessages.json")
	if err != nil {
		t.Fatal(err)
	}
	var f tsFrames
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Frames) != 3 {
		t.Fatalf("want 3 frames, got %d", len(f.Frames))
	}
	return f
}

// TestTransportMatchesTSFrames pins the wire shape against the TypeScript
// SDK rather than against the Go Transport itself: every TS frame decodes,
// and the Go Transport sends it back out field for field as TS sent it.
func TestTransportMatchesTSFrames(t *testing.T) {
	f := loadTSFrames(t)
	for _, frame := range f.Frames {
		t.Run(frame["messageType"].(string), func(t *testing.T) {
			var sent any
			tr := livesocket.NewTransport(func(event string, args ...any) error {
				if event != livesocket.AuthMessageEvent || len(args) != 1 {
					t.Fatalf("emit %s with %d args", event, len(args))
				}
				sent = args[0]
				return nil
			}, func(err error) { t.Fatal(err) })

			var got *auth.AuthMessage
			if err := tr.OnData(func(_ context.Context, m *auth.AuthMessage) error { got = m; return nil }); err != nil {
				t.Fatal(err)
			}
			tr.Receive(frame)
			if got == nil {
				t.Fatal("frame not delivered")
			}

			wantKey := f.Client
			if got.MessageType == auth.MessageTypeInitialResponse {
				wantKey = f.Server
			}
			if got.IdentityKey == nil || got.IdentityKey.ToDERHex() != wantKey {
				t.Fatalf("identityKey %v, want %s", got.IdentityKey, wantKey)
			}
			if got.MessageType == auth.MessageTypeGeneral {
				var env struct {
					EventName string `json:"eventName"`
					Data      struct {
						Body string `json:"body"`
					} `json:"data"`
				}
				if err := json.Unmarshal(got.Payload, &env); err != nil || env.EventName != "sendMessage" || env.Data.Body != "héllo <&> 🙂" {
					t.Fatalf("payload %q decoded to %+v, %v", got.Payload, env, err)
				}
			}

			if err := tr.Send(context.Background(), got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sent, frame) {
				gotJSON, _ := json.Marshal(sent)
				wantJSON, _ := json.Marshal(frame)
				t.Fatalf("Go sent\n%s\nTS sent\n%s", gotJSON, wantJSON)
			}
		})
	}
}

// TestGoPeerAnswersTSHandshake feeds the TS initialRequest to a Go Peer and
// checks it answers with an initialResponse to that nonce.
func TestGoPeerAnswersTSHandshake(t *testing.T) {
	f := loadTSFrames(t)
	w, _ := newWallet(t)
	var reply map[string]any
	tr := livesocket.NewTransport(func(_ string, args ...any) error {
		reply, _ = args[0].(map[string]any)
		return nil
	}, func(err error) { t.Fatal(err) })
	auth.NewPeer(&auth.PeerOptions{Wallet: w, Transport: tr})

	tr.Receive(f.Frames[0])
	if reply["messageType"] != "initialResponse" || reply["yourNonce"] != f.Frames[0]["initialNonce"] {
		t.Fatalf("reply %v", reply)
	}
}

// TestHandshakeCap checks that a connection starting handshake after
// handshake is cut off: each costs the server a signature and a session.
func TestHandshakeCap(t *testing.T) {
	f := loadTSFrames(t)
	w, _ := newWallet(t)
	var failed error
	tr := livesocket.NewTransport(func(string, ...any) error { return nil }, func(err error) { failed = err })
	auth.NewPeer(&auth.PeerOptions{Wallet: w, Transport: tr})

	// Each a fresh initialRequest: a replayed nonce is refused by the Peer.
	request := func() map[string]any {
		nonce := make([]byte, 32)
		_, _ = rand.Read(nonce)
		m := maps.Clone(f.Frames[0])
		m["initialNonce"] = base64.StdEncoding.EncodeToString(nonce)
		return m
	}
	for i := 0; i < 8; i++ {
		tr.Receive(request())
		if failed != nil {
			t.Fatalf("handshake %d refused: %v", i+1, failed)
		}
	}
	tr.Receive(request())
	if failed == nil {
		t.Fatal("ninth handshake on one connection accepted")
	}
}
