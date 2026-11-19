package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/auth"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"
	engine "github.com/zishang520/socket.io/clients/engine/v3"
	client "github.com/zishang520/socket.io/clients/socket/v3"
	"github.com/zishang520/socket.io/v3/pkg/types"

	"github.com/bsv-blockchain/go-message-box-server/pkg/livesocket"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// The box postSendMessage uses; free under the default fee policy.
const liveBox = "payment_inbox"

func newProtoWallet(t *testing.T) (wallet.Interface, string) {
	t.Helper()
	k, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	w, err := wallet.NewCompletedProtoWallet(k)
	if err != nil {
		t.Fatal(err)
	}
	return w, k.PubKey().ToDERHex()
}

// newLiveServer serves srv's socket events over a real Socket.IO endpoint.
func newLiveServer(t *testing.T, limits livesocket.Limits) (*Server, string) {
	t.Helper()
	srv := setupTestServer(t)
	srv.wallet, _ = newProtoWallet(t)
	io := livesocket.NewServer(livesocket.Options{})
	t.Cleanup(func() { io.Close(nil) })
	srv.AttachWebSockets(io, livesocket.NewRegistry(limits))
	mux := http.NewServeMux()
	mux.Handle(livesocket.Path, io.ServeHandler(nil))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return srv, ts.URL
}

type liveEvent struct {
	Name string         `json:"eventName"`
	Data map[string]any `json:"data"`
}

// liveClient is a go-sdk Peer over a Socket.IO client, speaking the
// {eventName, data} envelope the way @bsv/authsocket-client does.
type liveClient struct {
	t        *testing.T
	identity string
	peer     *auth.Peer
	events   chan liveEvent
	gone     chan struct{}
}

func dialLive(t *testing.T, url string) *liveClient {
	t.Helper()
	w, identity := newProtoWallet(t)
	return dialLiveAs(t, url, w, identity)
}

// newLiveSocket returns a Socket.IO client for url that has not started
// connecting. The client emits each event once, on its own goroutine, to the
// handlers attached at that moment, so attach them before calling Connect.
func newLiveSocket(t *testing.T, url string) *client.Socket {
	t.Helper()
	opts := client.DefaultOptions()
	opts.SetAutoConnect(false)
	opts.SetReconnection(false)
	opts.SetForceNew(true) // the client otherwise shares one Manager per URL
	// WebSocket only: see the note in pkg/livesocket/authsocket_test.go.
	opts.SetTransports(types.NewSet[engine.TransportCtor](&engine.WebSocketBuilder{}))
	c, err := client.Connect(url, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Disconnect() })
	return c
}

// dialLiveAs connects with its own Peer, and so its own session, for w.
func dialLiveAs(t *testing.T, url string, w wallet.Interface, identity string) *liveClient {
	t.Helper()
	c := newLiveSocket(t, url)
	lc := &liveClient{t: t, identity: identity, events: make(chan liveEvent, 16), gone: make(chan struct{})}
	tr := livesocket.NewTransport(c.Emit, func(err error) { t.Error(err) })
	_ = c.On(livesocket.AuthMessageEvent, tr.Receive)
	var goneOnce sync.Once // the client can report one disconnect twice
	_ = c.On("disconnect", func(...any) { goneOnce.Do(func() { close(lc.gone) }) })
	lc.peer = auth.NewPeer(&auth.PeerOptions{Wallet: w, Transport: tr})
	lc.peer.ListenForGeneralMessages(func(_ context.Context, _ *ec.PublicKey, payload []byte) error {
		var ev liveEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			t.Errorf("event %q: %v", payload, err)
		}
		lc.events <- ev
		return nil
	})
	c.Connect()
	// The zishang520 client can lose an Emit made before it has connected.
	for deadline := time.Now().Add(5 * time.Second); !c.Connected(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no connect")
		}
	}
	return lc
}

func (lc *liveClient) emit(name string, data any) {
	lc.t.Helper()
	payload, err := json.Marshal(map[string]any{"eventName": name, "data": data})
	if err != nil {
		lc.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := lc.peer.ToPeer(ctx, payload, nil, 10_000); err != nil {
		lc.t.Fatalf("emit %s: %v", name, err)
	}
}

// expect returns the next event, failing unless it is named name.
func (lc *liveClient) expect(name string) map[string]any {
	lc.t.Helper()
	select {
	case ev := <-lc.events:
		if ev.Name != name {
			lc.t.Fatalf("got event %s %v, want %s", ev.Name, ev.Data, name)
		}
		return ev.Data
	case <-time.After(10 * time.Second):
		lc.t.Fatalf("no %s", name)
		return nil
	}
}

func (lc *liveClient) authenticate() {
	lc.t.Helper()
	lc.emit("authenticated", map[string]any{"identityKey": lc.identity})
	if got := lc.expect("authenticationSuccess"); got["status"] != "success" {
		lc.t.Fatalf("authenticationSuccess %v", got)
	}
}

// expectNothing fails if any event arrives within wait.
func (lc *liveClient) expectNothing(wait time.Duration) {
	lc.t.Helper()
	select {
	case ev := <-lc.events:
		lc.t.Fatalf("unexpected event %s %v", ev.Name, ev.Data)
	case <-time.After(wait):
	}
}

// expectGone fails unless the server drops the socket within 5 s.
func (lc *liveClient) expectGone(what string) {
	lc.t.Helper()
	select {
	case <-lc.gone:
	case <-time.After(5 * time.Second):
		lc.t.Fatalf("socket kept: %s", what)
	}
}

func wantFields(t *testing.T, what string, got, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %s = %v, want %v (all: %v)", what, k, got[k], v, got)
		}
	}
}

// TestLiveSocket_SendAndPush follows the TypeScript server's flow: both
// sides authenticate, the recipient joins its own room, a socket send is
// stored and acked and pushed, an HTTP send is pushed the same way, and a
// duplicate is acked as the route's error.
func TestLiveSocket_SendAndPush(t *testing.T) {
	srv, url := newLiveServer(t, livesocket.DefaultLimits())
	alice, bob := dialLive(t, url), dialLive(t, url)
	alice.authenticate()
	bob.authenticate()

	room := alice.identity + "-" + liveBox
	alice.emit("joinRoom", room)
	wantFields(t, "joinedRoom", alice.expect("joinedRoom"), map[string]any{"roomId": room})

	bobRoom := bob.identity + "-" + liveBox
	alice.emit("joinRoom", bobRoom)
	wantFields(t, "join another identity's room", alice.expect("joinFailed"),
		map[string]any{"roomId": bobRoom, "code": "ERR_WEBSOCKET_ROOM_NOT_OWNED"})

	send := map[string]any{"roomId": room, "message": map[string]any{
		"messageId": "m-1", "recipient": alice.identity, "body": "hi alice",
	}}
	bob.emit("sendMessage", send)
	wantFields(t, "ack", bob.expect("sendMessageAck-"+room), map[string]any{"status": "success", "messageId": "m-1"})
	wantFields(t, "push", alice.expect("sendMessage-"+room),
		map[string]any{"sender": bob.identity, "messageId": "m-1", "body": "hi alice"})

	stored, err := srv.Store.ListMessages(context.Background(), alice.identity, liveBox)
	if err != nil || len(stored) != 1 || stored[0].Sender != bob.identity {
		t.Fatalf("stored %+v, %v; want one message from bob", stored, err)
	}

	wantStatus(t, postSendMessage(t, srv, bob.identity, alice.identity, "m-2"), 200, "")
	wantFields(t, "HTTP push", alice.expect("sendMessage-"+room),
		map[string]any{"sender": bob.identity, "messageId": "m-2", "body": "ciphertext"})

	// A paid send is stored but not pushed: listMessages internalizes its
	// payment, and a pushed copy could be acknowledged before that.
	paid := mustMarshal(t, map[string]any{
		"message": map[string]any{"recipient": alice.identity, "messageBox": liveBox, "messageId": "m-paid", "body": "paid"},
		"payment": map[string]any{"description": "fee"},
	})
	w := httptest.NewRecorder()
	srv.sendMessage(w, httptest.NewRequestWithContext(t.Context(), "POST", "/sendMessage", bytes.NewReader(paid)), bob.identity)
	wantStatus(t, w, 200, "")
	alice.expectNothing(time.Second)
	stored, err = srv.Store.ListMessages(context.Background(), alice.identity, liveBox)
	if err != nil || !slices.ContainsFunc(stored, func(m storage.Message) bool { return m.MessageID == "m-paid" }) {
		t.Fatalf("stored %+v, %v; want m-paid stored", stored, err)
	}

	// A batch send pushes each recipient its own stored copy.
	carol := dialLive(t, url)
	carol.authenticate()
	carolRoom := carol.identity + "-" + liveBox
	carol.emit("joinRoom", carolRoom)
	carol.expect("joinedRoom")
	batch := mustMarshal(t, map[string]any{"message": map[string]any{
		"recipients": []string{alice.identity, carol.identity},
		"messageBox": liveBox,
		"messageId":  []string{"m-3a", "m-3b"},
		"body":       "to both",
	}})
	r := httptest.NewRequestWithContext(t.Context(), "POST", "/sendMessage", bytes.NewReader(batch))
	w = httptest.NewRecorder()
	srv.sendMessage(w, r, bob.identity)
	wantStatus(t, w, 200, "")
	wantFields(t, "batch push to alice", alice.expect("sendMessage-"+room), map[string]any{"messageId": "m-3a", "body": "to both"})
	wantFields(t, "batch push to carol", carol.expect("sendMessage-"+carolRoom), map[string]any{"messageId": "m-3b", "body": "to both"})

	bob.emit("sendMessage", send)
	wantFields(t, "duplicate ack", bob.expect("sendMessageAck-"+room),
		map[string]any{"status": "error", "code": "ERR_DUPLICATE_MESSAGE"})
}

// TestLiveSocket_LeaveRoom checks that a left room gets no push, and that a
// socket may leave only its own identity's rooms. A push leaked after the
// leave would arrive before the one sent after the rejoin.
func TestLiveSocket_LeaveRoom(t *testing.T) {
	srv, url := newLiveServer(t, livesocket.DefaultLimits())
	alice, bob := dialLive(t, url), dialLive(t, url)
	alice.authenticate()
	room := alice.identity + "-" + liveBox
	alice.emit("joinRoom", room)
	alice.expect("joinedRoom")

	alice.emit("leaveRoom", bob.identity+"-"+liveBox)
	wantFields(t, "leave another identity's room", alice.expect("leaveFailed"),
		map[string]any{"reason": "Room is not owned by authenticated identity"})
	alice.emit("leaveRoom", " ")
	wantFields(t, "leave a blank room", alice.expect("leaveFailed"), map[string]any{"reason": "Invalid room ID"})

	alice.emit("leaveRoom", room)
	wantFields(t, "leftRoom", alice.expect("leftRoom"), map[string]any{"roomId": room})
	wantStatus(t, postSendMessage(t, srv, bob.identity, alice.identity, "after-leave"), 200, "")

	alice.emit("joinRoom", room)
	alice.expect("joinedRoom")
	wantStatus(t, postSendMessage(t, srv, bob.identity, alice.identity, "after-rejoin"), 200, "")
	wantFields(t, "push after rejoin", alice.expect("sendMessage-"+room), map[string]any{"messageId": "after-rejoin"})
}

// TestLiveSocket_RequiresAuthenticatedEvent checks that the BRC-103 handshake
// alone does not admit a socket to rooms: it must emit authenticated first.
func TestLiveSocket_RequiresAuthenticatedEvent(t *testing.T) {
	_, url := newLiveServer(t, livesocket.DefaultLimits())
	c := dialLive(t, url)
	c.emit("joinRoom", c.identity+"-"+liveBox)
	wantFields(t, "joinFailed", c.expect("joinFailed"), map[string]any{"code": "ERR_WEBSOCKET_NOT_AUTHENTICATED"})
}

// TestLiveSocket_IdentityClaimMismatch checks that a claimed identityKey
// other than the handshake's is refused and the socket dropped.
func TestLiveSocket_IdentityClaimMismatch(t *testing.T) {
	_, url := newLiveServer(t, livesocket.DefaultLimits())
	c := dialLive(t, url)
	c.emit("authenticated", map[string]any{"identityKey": mockIdentityKey})
	wantFields(t, "authenticationFailed", c.expect("authenticationFailed"),
		map[string]any{"reason": "Identity claim does not match authenticated peer"})
	select {
	case <-c.gone:
	case <-time.After(5 * time.Second):
		t.Fatal("socket kept after a mismatched identity claim")
	}
}

// TestLiveSocket_PerIdentityLimit checks that a second socket for the same
// identity is refused once the per-identity limit is reached.
func TestLiveSocket_PerIdentityLimit(t *testing.T) {
	limits := livesocket.DefaultLimits()
	limits.MaxConnectionsPerIdentity = 1
	_, url := newLiveServer(t, limits)
	w, identity := newProtoWallet(t)
	dialLiveAs(t, url, w, identity).authenticate()

	second := dialLiveAs(t, url, w, identity)
	second.emit("authenticated", map[string]any{"identityKey": identity})
	wantFields(t, "authenticationFailed", second.expect("authenticationFailed"),
		map[string]any{"reason": "WebSocket identity connection limit exceeded"})
}

func TestValidateLiveMessage(t *testing.T) {
	recipient := mockIdentityKey
	room := recipient + "-" + liveBox
	msg := func(id, rcpt, body any) map[string]any {
		return map[string]any{"messageId": id, "recipient": rcpt, "body": body}
	}
	upper := "028D37B941208CD6B8A4C28288EDA5F2F16C2B3AB0FCB6D13C18B47FE37B971FC1"
	for _, tc := range []struct {
		name    string
		room    any
		message any
		want    string
	}{
		{"valid", room, msg("m", recipient, "x"), ""},
		{"blank room", " ", msg("m", recipient, "x"), "Invalid room ID"},
		{"message not an object", room, "x", "Invalid message object"},
		{"blank body", room, msg("m", recipient, " "+string(rune(0xFEFF))), "Invalid message body"},
		{"body too large", room, msg("m", recipient, "xxxxx"), "Invalid message body"},
		{"non-string recipient", room, msg("m", 1, "x"), "Invalid recipient identity key"},
		{"non-canonical recipient", room, msg("m", upper, "x"), "Recipient identity key must use canonical encoding"},
		{"messageId with newline", room, msg("m\n", recipient, "x"), "Invalid message ID"},
		{"room for another box owner", "02" + recipient[2:] + "x-" + liveBox, msg("m", recipient, "x"), "Room does not match canonical recipient and message box"},
		{"room without a box", recipient + "-", msg("m", recipient, "x"), "Room does not match canonical recipient and message box"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got, reason := validateLiveMessage(tc.room, tc.message, 4)
			if reason != tc.want {
				t.Fatalf("reason %q, want %q", reason, tc.want)
			}
			if reason == "" && (got.messageBox != liveBox || got.recipient != recipient) {
				t.Fatalf("parsed %+v", got)
			}
		})
	}
}

// TestLiveSocket_ConnectionLimit checks that a socket over
// WEBSOCKET_MAX_CONNECTIONS is dropped on connect, and that the socket
// holding the slot is still served.
func TestLiveSocket_ConnectionLimit(t *testing.T) {
	limits := livesocket.DefaultLimits()
	limits.MaxConnections = 1
	_, url := newLiveServer(t, limits)
	first := dialLive(t, url)
	// The server writes CONNECT before it registers the socket, so a client
	// can be connected but not yet counted. Only the authenticated reply
	// shows that first holds the one slot.
	first.authenticate()

	c := newLiveSocket(t, url)
	// The socket connects and is dropped at once, too fast for dialLive's wait.
	gone := make(chan string, 1)
	_ = c.On("disconnect", func(args ...any) {
		select {
		case gone <- fmt.Sprint(args[0]):
		default:
		}
	})
	c.Connect()
	select {
	case reason := <-gone:
		if reason != "io server disconnect" {
			t.Fatalf("over-limit socket saw %q, want io server disconnect", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("socket kept over the process connection limit")
	}

	room := first.identity + "-" + liveBox
	first.emit("joinRoom", room)
	wantFields(t, "first socket after the refusal", first.expect("joinedRoom"), map[string]any{"roomId": room})
}

// TestLiveSocket_AuthTimeout checks that a socket which never emits
// authenticated is dropped, so idle sockets cannot hold every slot, and that
// an authenticated one is kept.
func TestLiveSocket_AuthTimeout(t *testing.T) {
	limits := livesocket.DefaultLimits()
	limits.AuthTimeout = time.Second
	_, url := newLiveServer(t, limits)
	idle, authed := dialLive(t, url), dialLive(t, url)
	authed.authenticate()
	idle.expectGone("never authenticated")
	select {
	case <-authed.gone:
		t.Fatal("authenticated socket dropped")
	case <-time.After(time.Second):
	}
}

// TestLiveSocket_RoomLimit checks the WEBSOCKET_MAX_ROOMS_PER_CONNECTION
// refusal, and that rejoining a joined room does not count against it.
func TestLiveSocket_RoomLimit(t *testing.T) {
	limits := livesocket.DefaultLimits()
	limits.MaxRoomsPerConnection = 2
	_, url := newLiveServer(t, limits)
	c := dialLive(t, url)
	c.authenticate()
	for _, box := range []string{"a", "b", "a"} {
		c.emit("joinRoom", c.identity+"-"+box)
		c.expect("joinedRoom")
	}
	c.emit("joinRoom", c.identity+"-c")
	wantFields(t, "third room", c.expect("joinFailed"),
		map[string]any{"roomId": c.identity + "-c", "code": "ERR_WEBSOCKET_ROOM_LIMIT"})
}

// TestLiveSocket_NewestRecipientsFirst checks that with more joined sockets
// than WEBSOCKET_MAX_RECIPIENT_CONNECTIONS, the push goes to the newest.
func TestLiveSocket_NewestRecipientsFirst(t *testing.T) {
	limits := livesocket.DefaultLimits()
	limits.MaxRecipientConnections = 1
	srv, url := newLiveServer(t, limits)
	w, identity := newProtoWallet(t)
	room := identity + "-" + liveBox
	old, newest := dialLiveAs(t, url, w, identity), dialLiveAs(t, url, w, identity)
	for _, c := range []*liveClient{newest, old} { // join order must not matter
		c.authenticate()
		c.emit("joinRoom", room)
		c.expect("joinedRoom")
	}
	wantStatus(t, postSendMessage(t, srv, mockIdentityKey, identity, "m"), 200, "")
	wantFields(t, "push to the newest socket", newest.expect("sendMessage-"+room), map[string]any{"messageId": "m"})
	old.expectNothing(time.Second)
}

// TestLiveSocket_RateLimits checks the send and control limits and their
// reply codes.
func TestLiveSocket_RateLimits(t *testing.T) {
	limits := livesocket.DefaultLimits()
	limits.SendRatePerMinute = 1
	limits.ControlRatePerMinute = 1
	_, url := newLiveServer(t, limits)
	c := dialLive(t, url)
	c.authenticate()
	room := c.identity + "-" + liveBox

	c.emit("joinRoom", room)
	c.expect("joinedRoom")
	c.emit("joinRoom", room)
	wantFields(t, "second join", c.expect("joinFailed"), map[string]any{"code": "ERR_WEBSOCKET_CONTROL_RATE_LIMITED"})
	c.emit("leaveRoom", room)
	wantFields(t, "leave", c.expect("leaveFailed"), map[string]any{"code": "ERR_WEBSOCKET_CONTROL_RATE_LIMITED"})

	// To a recipient with no socket, so the ack is the only reply.
	other := mockIdentityKey + "-" + liveBox
	send := func(id string) {
		c.emit("sendMessage", map[string]any{"roomId": other, "message": map[string]any{"messageId": id, "recipient": mockIdentityKey, "body": "x"}})
	}
	send("m-1")
	wantFields(t, "first send", c.expect("sendMessageAck-"+other), map[string]any{"status": "success"})
	send("m-2")
	wantFields(t, "second send", c.expect("messageFailed"), map[string]any{"code": "ERR_WEBSOCKET_RATE_LIMITED"})
}

// TestLiveSocket_BodyLimit checks that a socket send is held to the server's
// MAX_MESSAGE_BODY_BYTES, the same cap as an HTTP send.
func TestLiveSocket_BodyLimit(t *testing.T) {
	srv, url := newLiveServer(t, livesocket.DefaultLimits())
	srv.SetMaxMessageBodyBytes(4)
	c := dialLive(t, url)
	c.authenticate()
	room := mockIdentityKey + "-" + liveBox
	send := func(id, body string) {
		c.emit("sendMessage", map[string]any{"roomId": room, "message": map[string]any{"messageId": id, "recipient": mockIdentityKey, "body": body}})
	}
	send("at-limit", "abcd")
	wantFields(t, "at the cap", c.expect("sendMessageAck-"+room), map[string]any{"status": "success"})
	send("over", "abcde")
	wantFields(t, "over the cap", c.expect("messageFailed"), map[string]any{"reason": "Invalid message body"})
}
