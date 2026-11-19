// Package livesockettest dials live socket servers with the zishang520
// Socket.IO client, working around the ways that client loses a connection's
// first events. It must be imported only from _test.go files: it imports
// testing, which registers the test flags on the default FlagSet when linked
// into a binary.
package livesockettest

import (
	"testing"
	"time"

	engine "github.com/zishang520/socket.io/clients/engine/v3"
	client "github.com/zishang520/socket.io/clients/socket/v3"
	"github.com/zishang520/socket.io/v3/pkg/types"
)

// openWait bounds one dial's engine.io handshake, which takes milliseconds
// against a local server.
const openWait = 2 * time.Second

// dials is how many times Connect dials before it fails the test.
const dials = 3

// connectWait bounds the wait for the server's socket.io CONNECT.
const connectWait = 5 * time.Second

// NewClient returns a client for url that has not started connecting. The
// client emits each event once, to the handlers attached at that moment, so
// attach them before calling Connect.
//
// The client does not reconnect, does not share its Manager with other
// clients, and is disconnected when the test ends. It uses WebSocket only:
// the polling transport loses its connect or its first packets in roughly
// one dial in five.
func NewClient(t testing.TB, url string) *client.Socket {
	t.Helper()
	opts := client.DefaultOptions()
	opts.SetAutoConnect(false)
	opts.SetReconnection(false)
	opts.SetForceNew(true) // the client otherwise shares one Manager per URL
	opts.SetTransports(types.NewSet[engine.TransportCtor](&engine.WebSocketBuilder{}))
	c, err := client.Connect(url, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Disconnect() })
	return c
}

// Connect starts c's connection and returns once the client has read the
// server's engine.io handshake, redialing if the client misses it. It does
// not wait for the server's socket.io CONNECT, so it also suits a socket the
// server drops on connect; WaitConnected waits for that.
//
// zishang520's Manager.Open dials, which starts the WebSocket reader, and only
// then subscribes to the engine's open event. An OPEN packet the reader
// handles in between is dropped, so the client never sends its CONNECT and
// the dial hangs. Once the Manager is open, the reader goroutine runs the rest
// of the handshake in order, so later packets are not lost this way.
func Connect(t testing.TB, c *client.Socket) {
	t.Helper()
	opened := make(chan struct{}, 1)
	_ = c.Io().On("open", func(...any) {
		select {
		case opened <- struct{}{}:
		default:
		}
	})
	for dial := 1; ; dial++ {
		c.Connect()
		select {
		case <-opened:
			return
		case <-time.After(openWait):
		}
		if dial == dials {
			t.Fatalf("client missed the engine.io handshake on %d dials", dials)
		}
		t.Logf("dial %d missed the engine.io handshake; redialing", dial)
		// A socket that never connected disconnects without a disconnect
		// event. Closing the engine stops it from opening the Manager late.
		c.Disconnect()
		select {
		case <-opened:
		default:
		}
	}
}

// WaitConnected fails t unless c receives the server's CONNECT. The client
// can lose an Emit made before then.
func WaitConnected(t testing.TB, c *client.Socket) {
	t.Helper()
	for deadline := time.Now().Add(connectWait); !c.Connected(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no connect")
		}
	}
}
