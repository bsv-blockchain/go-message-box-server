package livesocket_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/auth"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"
	engine "github.com/zishang520/socket.io/clients/engine/v3"
	client "github.com/zishang520/socket.io/clients/socket/v3"
	"github.com/zishang520/socket.io/servers/socket/v3"
	"github.com/zishang520/socket.io/v3/pkg/types"

	"github.com/bsv-blockchain/go-message-box-server/pkg/livesocket"
)

func newWallet(t *testing.T) (wallet.Interface, *ec.PublicKey) {
	t.Helper()
	k, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	w, err := wallet.NewCompletedProtoWallet(k)
	if err != nil {
		t.Fatal(err)
	}
	return w, k.PubKey()
}

// newAuthServer serves AuthSockets whose "ping" handler emits "pong" with the
// same data. Each accepted AuthSocket is sent on the returned channel.
func newAuthServer(t *testing.T) (*httptest.Server, <-chan *livesocket.AuthSocket) {
	t.Helper()
	serverWallet, _ := newWallet(t)
	accepted := make(chan *livesocket.AuthSocket, 1)
	io := livesocket.NewServer(livesocket.Options{})
	t.Cleanup(func() { io.Close(nil) })
	_ = io.On("connection", func(args ...any) {
		a, err := livesocket.NewAuthSocket(args[0].(*socket.Socket), serverWallet, nil)
		if err != nil {
			t.Error(err)
			return
		}
		a.On("ping", func(data any) {
			if err := a.Emit(context.Background(), "pong", data); err != nil {
				t.Error(err)
			}
		})
		accepted <- a
	})
	mux := http.NewServeMux()
	mux.Handle(livesocket.Path, io.ServeHandler(nil))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, accepted
}

func dial(t *testing.T, url string) *client.Socket {
	t.Helper()
	opts := client.DefaultOptions()
	opts.SetReconnection(false)
	// WebSocket only: the zishang520 Go client's polling transport loses its
	// connect or its first packets in roughly one dial in five. The server's
	// polling is covered by TestEchoOverPolling and by the TS probes.
	opts.SetTransports(types.NewSet[engine.TransportCtor](&engine.WebSocketBuilder{}))
	c, err := client.Connect(url, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Disconnect() })
	return c
}

func encodeEvent(t *testing.T, name string, data any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"eventName": name, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAuthenticatedRoundTrip runs a real go-sdk Peer on each end: the client's
// first ToPeer drives the BRC-103 handshake over "authMessage", then a signed
// general message carries {eventName, data} the way @bsv/authsocket-client does.
func TestAuthenticatedRoundTrip(t *testing.T) {
	ts, accepted := newAuthServer(t)
	c := dial(t, ts.URL)

	clientWallet, clientKey := newWallet(t)
	tr := livesocket.NewTransport(c.Emit, func(err error) { t.Error(err) })
	if err := c.On(livesocket.AuthMessageEvent, tr.Receive); err != nil {
		t.Fatal(err)
	}
	peer := auth.NewPeer(&auth.PeerOptions{Wallet: clientWallet, Transport: tr})

	got := make(chan []byte, 1)
	peer.ListenForGeneralMessages(func(_ context.Context, _ *ec.PublicKey, payload []byte) error {
		got <- payload
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := peer.ToPeer(ctx, encodeEvent(t, "ping", map[string]any{"n": 1.0, "bytes": []int{0, 255}}), nil, 10_000); err != nil {
		t.Fatal(err)
	}

	select {
	case payload := <-got:
		var env struct {
			EventName string         `json:"eventName"`
			Data      map[string]any `json:"data"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			t.Fatalf("pong payload %q: %v", payload, err)
		}
		if env.EventName != "pong" || env.Data["n"] != 1.0 {
			t.Fatalf("got %s, want pong with n=1", payload)
		}
	case <-ctx.Done():
		t.Fatal("no pong")
	}

	a := <-accepted
	if k := a.IdentityKey(); k == nil || !k.IsEqual(clientKey) {
		t.Fatalf("server bound identity %v, want client key %v", k, clientKey)
	}
}

// TestMalformedAuthMessageDisconnects checks that a frame that is not an
// AuthMessage drops the socket, as @bsv/authsocket's transport does, and that
// the client is told (io server disconnect): on a bare transport close
// socket.io reconnects, and the server drops it again, in a loop.
func TestMalformedAuthMessageDisconnects(t *testing.T) {
	ts, _ := newAuthServer(t)
	c := dial(t, ts.URL)

	connected, gone := make(chan struct{}), make(chan string, 1)
	_ = c.On("connect", func(...any) { close(connected) })
	_ = c.On("disconnect", func(args ...any) {
		select {
		case gone <- fmt.Sprint(args[0]):
		default:
		}
	})
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("no connect")
	}
	// Emitted outside the connect listener: the zishang520 client does not
	// flush an Emit made from inside it.
	if err := c.Emit(livesocket.AuthMessageEvent, map[string]any{"identityKey": "not-a-key"}); err != nil {
		t.Fatal(err)
	}

	select {
	case reason := <-gone:
		if reason != "io server disconnect" {
			t.Fatalf("client saw %q, want io server disconnect", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server kept a socket that sent a malformed authMessage")
	}
}
