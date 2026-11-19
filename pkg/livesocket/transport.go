package livesocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/bsv-blockchain/go-sdk/auth"
)

// AuthMessageEvent is the Socket.IO event @bsv/authsocket and
// @bsv/authsocket-client carry BRC-103 AuthMessages on.
const AuthMessageEvent = "authMessage"

// Transport is a go-sdk auth.Transport over one Socket.IO connection: the
// Go side of @bsv/authsocket's SocketServerTransport.
type Transport struct {
	emit    func(string, ...any) error
	onError func(error)

	mu     sync.RWMutex
	onData func(context.Context, *auth.AuthMessage) error

	handshakes atomic.Int32
}

// maxHandshakes caps the handshakes one connection may start. Each costs the
// server a signature and leaves a session in this connection's Peer, and an
// initialRequest is unsigned, so without a cap one socket could make the
// server sign and keep sessions without end. A client re-handshakes only when
// it has lost its session, which a handful covers.
const maxHandshakes = 8

var _ auth.Transport = (*Transport)(nil)

// NewTransport sends with emit, a Socket.IO socket's Emit. Register Receive
// as that socket's AuthMessageEvent listener. onError is called when a frame
// fails to decode or the Peer rejects it; callers should then drop the socket.
func NewTransport(emit func(string, ...any) error, onError func(error)) *Transport {
	return &Transport{emit: emit, onError: onError}
}

// Send emits m as a JSON object. It goes through a map rather than raw bytes:
// the Socket.IO parser would send a []byte as a binary attachment, which the
// TS transport does not accept as an AuthMessage.
func (t *Transport) Send(_ context.Context, m *auth.AuthMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode AuthMessage: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return fmt.Errorf("encode AuthMessage: %w", err)
	}
	// go-sdk writes requestedCertificates on every message; the TS SDK only
	// on handshake and certificate messages. Match it on the busiest one.
	if m.MessageType == auth.MessageTypeGeneral {
		delete(obj, "requestedCertificates")
	}
	return t.emit(AuthMessageEvent, obj)
}

// OnData registers the Peer's handler. auth.NewPeer calls it once.
func (t *Transport) OnData(cb func(context.Context, *auth.AuthMessage) error) error {
	if cb == nil {
		return errors.New("callback cannot be nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onData = cb
	return nil
}

// GetRegisteredOnData returns the handler set by OnData.
func (t *Transport) GetRegisteredOnData() (func(context.Context, *auth.AuthMessage) error, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.onData == nil {
		return nil, errors.New("no handler registered")
	}
	return t.onData, nil
}

// Receive is the AuthMessageEvent listener.
func (t *Transport) Receive(args ...any) {
	if len(args) == 0 {
		t.fail(errors.New("empty authMessage"))
		return
	}
	// The parser hands JSON over as map[string]any; round-trip it through
	// AuthMessage's own JSON decoding, which knows the number[] byte fields.
	b, err := json.Marshal(args[0])
	if err != nil {
		t.fail(fmt.Errorf("decode authMessage: %w", err))
		return
	}
	var m auth.AuthMessage
	if err = json.Unmarshal(b, &m); err != nil {
		t.fail(fmt.Errorf("decode authMessage: %w", err))
		return
	}
	if m.MessageType == auth.MessageTypeInitialRequest && t.handshakes.Add(1) > maxHandshakes {
		t.fail(fmt.Errorf("more than %d handshakes on one connection", maxHandshakes))
		return
	}
	cb, err := t.GetRegisteredOnData()
	if err != nil {
		t.fail(err)
		return
	}
	if err := cb(context.Background(), &m); err != nil {
		t.fail(err)
	}
}

func (t *Transport) fail(err error) {
	if t.onError != nil {
		t.onError(err)
	}
}
