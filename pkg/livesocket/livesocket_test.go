package livesocket_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/bsv-blockchain/go-message-box-server/pkg/livesocket"
)

// The tests below speak raw Engine.IO v4 / Socket.IO v5 packets, the protocol
// socket.io-client 4.x uses, instead of trusting the library's own Go client:
//
//	0{...}            engine OPEN (server -> client, carries sid)
//	40                socket CONNECT to the main namespace
//	421["echo","hi"]  engine MESSAGE + socket EVENT with ack id 1
//	431["hi"]         engine MESSAGE + socket ACK for id 1

// newEchoServer mounts the Socket.IO handler on a plain ServeMux, as main does,
// with an "echo" event that acks back whatever it was sent.
func newEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	io := livesocket.NewServer(livesocket.Options{})
	t.Cleanup(func() { io.Close(nil) })
	_ = io.On("connection", func(args ...any) {
		s := args[0].(*socket.Socket)
		_ = s.On("echo", func(args ...any) {
			ack := args[len(args)-1].(socket.Ack)
			ack(args[:len(args)-1], nil)
		})
	})
	mux := http.NewServeMux()
	mux.Handle(livesocket.Path, io.ServeHandler(nil))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestEchoOverWebSocket(t *testing.T) {
	ts := newEchoServer(t)
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + livesocket.Path + "?EIO=4&transport=websocket"
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	read := func() string {
		t.Helper()
		_, b, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	write := func(s string) {
		t.Helper()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}

	expectOpen(t, read())
	write("40")
	expectPrefix(t, read(), "40{")
	write(`421["echo","hi"]`)
	if got := read(); got != `431["hi"]` {
		t.Fatalf("ack packet = %q, want %q", got, `431["hi"]`)
	}
}

func TestEchoOverPolling(t *testing.T) {
	ts := newEchoServer(t)
	base := ts.URL + livesocket.Path + "?EIO=4&transport=polling"
	hc := &http.Client{Timeout: 5 * time.Second}

	get := func(url string) []string {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET status %d: %s", resp.StatusCode, b)
		}
		return strings.Split(string(b), "\x1e") // polling payload separator
	}
	post := func(url, body string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(b) != "ok" {
			t.Fatalf("POST status %d: %s", resp.StatusCode, b)
		}
	}

	sid := expectOpen(t, get(base)[0])
	url := base + "&sid=" + sid
	post(url, "40")
	expectPrefix(t, get(url)[0], "40{")
	post(url, `421["echo","hi"]`)
	if got := get(url)[0]; got != `431["hi"]` {
		t.Fatalf("ack packet = %q, want %q", got, `431["hi"]`)
	}
}

// expectOpen checks an Engine.IO v4 OPEN packet and returns its sid.
func expectOpen(t *testing.T, pkt string) string {
	t.Helper()
	expectPrefix(t, pkt, "0{")
	var open struct {
		Sid          string   `json:"sid"`
		Upgrades     []string `json:"upgrades"`
		PingInterval int      `json:"pingInterval"`
		PingTimeout  int      `json:"pingTimeout"`
		MaxPayload   int      `json:"maxPayload"`
	}
	if err := json.Unmarshal([]byte(pkt[1:]), &open); err != nil {
		t.Fatalf("open packet %q: %v", pkt, err)
	}
	if open.Sid == "" || open.PingInterval == 0 || open.PingTimeout == 0 || open.MaxPayload == 0 {
		t.Fatalf("open packet missing v4 fields: %q", pkt)
	}
	return open.Sid
}

func expectPrefix(t *testing.T, got, prefix string) {
	t.Helper()
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("packet = %q, want prefix %q", got, prefix)
	}
}
