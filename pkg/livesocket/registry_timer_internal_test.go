package livesocket

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"
	client "github.com/zishang520/socket.io/clients/socket/v3"
	"github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/bsv-blockchain/go-message-box-server/pkg/livesocket/livesockettest"
)

// registerOne connects one client to a Registry with an hour-long auth
// deadline and returns the client and the socket's registry entry.
func registerOne(t *testing.T) (*Registry, *client.Socket, string, *entry) {
	t.Helper()
	k, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	w, err := wallet.NewCompletedProtoWallet(k)
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.AuthTimeout = time.Hour
	r := NewRegistry(limits)
	registered := make(chan string, 1)
	io := NewServer(Options{})
	t.Cleanup(func() { io.Close(nil) })
	_ = io.On("connection", func(args ...any) {
		a, aerr := NewAuthSocket(args[0].(*socket.Socket), w, nil)
		if aerr != nil {
			t.Error(aerr)
			return
		}
		if r.Register(a) {
			registered <- string(a.Socket().Id())
		}
	})
	mux := http.NewServeMux()
	mux.Handle(Path, io.ServeHandler(nil))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	c := livesockettest.NewClient(t, ts.URL)
	livesockettest.Connect(t, c)
	select {
	case id := <-registered:
		r.mu.Lock()
		e := r.conns[id]
		r.mu.Unlock()
		return r, c, id, e
	case <-time.After(5 * time.Second):
		t.Fatal("no registration")
		return nil, nil, "", nil
	}
}

// The auth deadline must not outlive its reason: once a socket authenticates
// or disconnects, its timer is stopped rather than left holding the socket.
// Timer.Stop reports true only for a timer that was still pending.
func TestAuthTimerStopped(t *testing.T) {
	t.Run("on authenticate", func(t *testing.T) {
		r, _, id, e := registerOne(t)
		if !r.Authenticate(id, "identity") {
			t.Fatal("authenticate refused")
		}
		if e.authTimer.Stop() {
			t.Fatal("auth timer still pending after authenticate")
		}
	})
	t.Run("on disconnect", func(t *testing.T) {
		r, c, id, e := registerOne(t)
		c.Disconnect()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			r.mu.Lock()
			_, open := r.conns[id]
			r.mu.Unlock()
			if !open {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("disconnect not seen")
			}
		}
		if e.authTimer.Stop() {
			t.Fatal("auth timer still pending after disconnect")
		}
	})
}
