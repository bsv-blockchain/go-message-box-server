package livesocket_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/auth"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"

	"github.com/bsv-blockchain/go-message-box-server/pkg/livesocket"
)

// TestSessionResumesOnNewConnection is socket.io reconnecting a client after
// an outage the server survived: the client keeps its BRC-103 session and its
// next message arrives on a new connection, whose Peer must accept it (as
// @bsv/authsocket's shared SessionManager does) instead of dropping it.
func TestSessionResumesOnNewConnection(t *testing.T) {
	store := livesocket.NewSessionStore(time.Minute)
	serverWallet, _ := newWallet(t)
	clientWallet, _ := newWallet(t)

	var server atomic.Pointer[livesocket.Transport]
	failed := make(chan error, 4)
	client := livesocket.NewTransport(func(_ string, args ...any) error {
		go server.Load().Receive(args...)
		return nil
	}, func(err error) { t.Error(err) })
	conn := func() (*livesocket.ConnSessions, <-chan []byte) {
		sessions := store.Conn()
		tr := livesocket.NewTransport(func(_ string, args ...any) error {
			go client.Receive(args...)
			return nil
		}, func(err error) { failed <- err })
		p := auth.NewPeer(&auth.PeerOptions{Wallet: serverWallet, Transport: tr, SessionManager: sessions})
		got := make(chan []byte, 1)
		p.ListenForGeneralMessages(func(_ context.Context, _ *ec.PublicKey, payload []byte) error {
			got <- payload
			return nil
		})
		server.Store(tr)
		return sessions, got
	}
	peer := auth.NewPeer(&auth.PeerOptions{Wallet: clientWallet, Transport: client})
	send := func(got <-chan []byte, body string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := peer.ToPeer(ctx, []byte(body), nil, 5_000); err != nil {
			t.Fatal(err)
		}
		select {
		case b := <-got:
			if string(b) != body {
				t.Fatalf("got %q, want %q", b, body)
			}
		case err := <-failed:
			t.Fatalf("server refused %q: %v", body, err)
		case <-ctx.Done():
			t.Fatalf("%q not received", body)
		}
	}

	first, gotFirst := conn()
	send(gotFirst, "before the outage")
	first.Release()

	_, gotSecond := conn()
	send(gotSecond, "after the reconnect")
}
