package livesocket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bsv-blockchain/go-sdk/auth"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/zishang520/socket.io/servers/socket/v3"
)

// emitWaitMs bounds how long Emit waits for a session. Emit only runs after
// the client has authenticated, so this is a ceiling, not an expected wait.
const emitWaitMs = 10_000

// Envelope limits from @bsv/authsocket's eventPayload.ts. The TS client drops
// its socket on a payload over maxPayloadBytes, so Emit refuses to send one.
const (
	maxPayloadBytes   = 1 << 20
	maxEventNameBytes = 256
)

// reservedEvents are Socket.IO lifecycle names, which a signed event may not
// use: a remote "disconnect" must never reach a handler as if it were real.
var reservedEvents = map[string]bool{
	"_unknown": true, "connect": true, "connect_error": true, "disconnect": true,
	"disconnecting": true, "newListener": true, "removeListener": true,
}

// AuthSocket is one Socket.IO connection wrapped in its own BRC-103 Peer: the
// Go side of @bsv/authsocket's AuthSocket. Application events travel as signed
// general messages whose payload is the UTF-8 JSON {"eventName", "data"}.
//
// The first authenticated sender binds the socket's identity; a message from
// any other identity, or one that does not decode, drops the connection.
type AuthSocket struct {
	sock *socket.Socket
	peer *auth.Peer

	mu          sync.RWMutex
	identityKey *ec.PublicKey
	handlers    map[string]func(data any)
}

// NewAuthSocket starts a Peer for s that signs with w. Register handlers with
// On before the connection listener returns, so no event arrives unhandled.
//
// With sessions, a reconnect may resume a session negotiated on an earlier
// connection (see SessionStore); with nil, the Peer keeps its own sessions
// only and a reconnected client must handshake again.
func NewAuthSocket(s *socket.Socket, w wallet.Interface, sessions *SessionStore) (*AuthSocket, error) {
	a := &AuthSocket{sock: s, handlers: map[string]func(any){}}
	tr := NewTransport(s.Emit, func(err error) { a.drop("authentication", err) })
	if err := s.On(AuthMessageEvent, tr.Receive); err != nil {
		return nil, err
	}
	opts := &auth.PeerOptions{Wallet: w, Transport: tr}
	if sessions != nil {
		conn := sessions.Conn()
		if err := s.On("disconnect", func(...any) { conn.Release() }); err != nil {
			return nil, err
		}
		opts.SessionManager = conn
	}
	a.peer = auth.NewPeer(opts)
	a.peer.ListenForGeneralMessages(a.onGeneral)
	return a, nil
}

// On registers the handler for an application event, replacing any earlier one.
func (a *AuthSocket) On(event string, h func(data any)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.handlers[event] = h
}

// Emit sends an application event to the authenticated client. Data must not
// hold []byte, which encoding/json writes as base64 where TS expects number[].
func (a *AuthSocket) Emit(ctx context.Context, event string, data any) error {
	key := a.IdentityKey()
	if key == nil {
		return errors.New("socket has not authenticated")
	}
	if !a.sock.Connected() {
		return errors.New("socket is disconnected")
	}
	if err := checkEventName(event); err != nil {
		return err
	}
	payload, err := json.Marshal(envelope{EventName: event, Data: data})
	if err != nil {
		return fmt.Errorf("encode %s: %w", event, err)
	}
	if len(payload) > maxPayloadBytes {
		return fmt.Errorf("%s payload is %d bytes, over the %d the client accepts", event, len(payload), maxPayloadBytes)
	}
	return a.peer.ToPeer(ctx, payload, key, emitWaitMs)
}

// IdentityKey is the client's verified identity, or nil before its first
// authenticated message.
func (a *AuthSocket) IdentityKey() *ec.PublicKey {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.identityKey
}

// Socket is the underlying Socket.IO socket, for rooms and the socket id.
func (a *AuthSocket) Socket() *socket.Socket { return a.sock }

type envelope struct {
	EventName string `json:"eventName"`
	Data      any    `json:"data,omitempty"`
}

func (a *AuthSocket) onGeneral(_ context.Context, sender *ec.PublicKey, payload []byte) error {
	a.mu.Lock()
	switch {
	case a.identityKey == nil:
		a.identityKey = sender
	case !a.identityKey.IsEqual(sender):
		a.mu.Unlock()
		a.drop("application", errors.New("authenticated identity changed during the connection"))
		return nil
	}
	a.mu.Unlock()

	env, err := decodeEnvelope(payload)
	if err != nil {
		a.drop("application", err)
		return nil
	}
	a.mu.RLock()
	h := a.handlers[env.EventName]
	a.mu.RUnlock()
	if h != nil {
		h(env.Data)
	}
	return nil
}

// checkEventName applies @bsv/authsocket's assertEventName: non-empty, at
// most 256 UTF-8 bytes, not reserved, and free of C0/C1 control and bidi
// formatting characters.
func checkEventName(name string) error {
	if name == "" || len(name) > maxEventNameBytes || reservedEvents[name] {
		return fmt.Errorf("event name %q is empty, reserved, or over %d bytes", name, maxEventNameBytes)
	}
	for _, r := range name {
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return errors.New("event name contains control or bidirectional formatting characters")
		}
	}
	return nil
}

// decodeEnvelope accepts exactly {"eventName": name, "data"?: any} in valid
// UTF-8, the shape @bsv/authsocket's parseAuthSocketEventPayload accepts.
func decodeEnvelope(payload []byte) (envelope, error) {
	if len(payload) > maxPayloadBytes {
		return envelope{}, fmt.Errorf("event payload is over %d bytes", maxPayloadBytes)
	}
	if !utf8.Valid(payload) {
		return envelope{}, errors.New("event payload is not valid UTF-8")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return envelope{}, fmt.Errorf("event payload: %w", err)
	}
	for k := range fields {
		if k != "eventName" && k != "data" {
			return envelope{}, fmt.Errorf("event payload has unexpected field %q", k)
		}
	}
	var env envelope
	if err := json.Unmarshal(fields["eventName"], &env.EventName); err != nil {
		return envelope{}, errors.New("event payload needs a string eventName")
	}
	if err := checkEventName(env.EventName); err != nil {
		return envelope{}, err
	}
	if raw, ok := fields["data"]; ok && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &env.Data); err != nil {
			return envelope{}, fmt.Errorf("event data: %w", err)
		}
	}
	return env, nil
}

func (a *AuthSocket) drop(phase string, err error) {
	slog.Warn("dropping socket", "phase", phase, "sid", a.sock.Id(), "error", err)
	Drop(a.sock)
}

// dropGrace is how long Drop leaves the connection for the DISCONNECT packet
// to go out; a socket.io client closes it itself on receipt.
const dropGrace = time.Second

// Drop ends s as @bsv/authsocket's socket.disconnect(true) does: the client
// gets the namespace DISCONNECT (io server disconnect, which socket.io does not
// reconnect from), then the connection is closed. zishang520's Disconnect(true)
// closes the transport before that packet is written: the client sees a
// transport close, reconnects, and is dropped again, in a loop.
func Drop(s *socket.Socket) {
	conn := s.Conn()
	s.Disconnect(false)
	time.AfterFunc(dropGrace, func() { conn.Close(false) })
}
