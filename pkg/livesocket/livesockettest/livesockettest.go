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

// Connect opens c's connection and sends its socket.io CONNECT once,
// redialing if the client misses the server's engine.io handshake. It does
// not wait for the server's reply, so it also suits a socket the server
// drops on connect; WaitConnected waits for that.
//
// It works around two races in the zishang520 client, both of which happen
// when the WebSocket reader goroutine handles the server's OPEN packet early:
//
//   - Manager.Open dials, which starts the reader, and only then subscribes
//     to the engine's open event. An OPEN handled in between is dropped, the
//     client never sends CONNECT, and the dial hangs.
//   - Socket.Connect subscribes to the Manager's open event, opens the
//     Manager, then sends CONNECT itself if the Manager is already open. If
//     the reader opens it in between, CONNECT goes out twice, and the server
//     closes the connection on the second ("invalid state").
//
// So Connect opens the Manager itself and waits for it, redialing if it
// stays closed. Socket.Connect then finds it open and sends CONNECT once.
func Connect(t testing.TB, c *client.Socket) {
	t.Helper()
	for dial := 1; ; dial++ {
		opened := make(chan error, 1)
		c.Io().Open(func(err error) {
			select {
			case opened <- err:
			default:
			}
		})
		select {
		case err := <-opened:
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			c.Connect()
			return
		case <-time.After(openWait):
		}
		if dial == dials {
			t.Fatalf("client missed the engine.io handshake on %d dials", dials)
		}
		t.Logf("dial %d missed the engine.io handshake; redialing", dial)
		// Closes the Manager, so a late OPEN cannot reach it. A socket that
		// never connected emits no disconnect event.
		c.Disconnect()
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
