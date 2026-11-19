package livesocket

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Limits bounds the live-delivery endpoint. -1 means no limit. The defaults
// and env names match the TypeScript server's "standard" resource profile
// (src/config/resources.ts).
type Limits struct {
	MaxConnections            int // WEBSOCKET_MAX_CONNECTIONS
	MaxConnectionsPerIdentity int // WEBSOCKET_MAX_CONNECTIONS_PER_IDENTITY
	MaxRoomsPerConnection     int // WEBSOCKET_MAX_ROOMS_PER_CONNECTION
	ControlRatePerMinute      int // WEBSOCKET_CONTROL_RATE_LIMIT (joinRoom, leaveRoom)
	SendRatePerMinute         int // WEBSOCKET_SEND_RATE_LIMIT
	MaxRecipientConnections   int // WEBSOCKET_MAX_RECIPIENT_CONNECTIONS, per pushed message

	// AuthTimeout is how long a connected socket may hold a connection slot
	// before it emits authenticated; 0 means no deadline.
	AuthTimeout time.Duration
}

// DefaultLimits is the TypeScript server's "standard" profile.
func DefaultLimits() Limits {
	return Limits{
		MaxConnections:            1_000,
		MaxConnectionsPerIdentity: 25,
		MaxRoomsPerConnection:     128,
		ControlRatePerMinute:      600,
		SendRatePerMinute:         300,
		MaxRecipientConnections:   25,
		AuthTimeout:               DefaultAuthTimeout,
	}
}

// DefaultAuthTimeout bounds how long a socket that has not authenticated
// counts against MaxConnections. Without it, idle sockets that never sign
// anything could hold every slot.
const DefaultAuthTimeout = 30 * time.Second

// pushTimeout bounds one signed push to one socket.
const pushTimeout = 10 * time.Second

// Registry is the process-local routing state for live delivery: which
// sockets are connected, which identity each authenticated as, and which
// rooms each joined. Rooms are tracked here rather than with Socket.IO's own
// rooms because every push is signed per socket, not broadcast.
//
// Only sockets on this process are reached; a multi-node deployment needs
// sticky sessions, as the TypeScript server does.
type Registry struct {
	limits Limits

	mu    sync.Mutex
	seq   uint64
	conns map[string]*entry // by Socket.IO socket id
}

type entry struct {
	sock     *AuthSocket
	seq      uint64 // registration order, for newest-first bounding
	identity string // empty until Authenticate
	rooms    map[string]struct{}

	// authTimer drops the socket at AuthTimeout; stopped by Authenticate and
	// on disconnect, so it does not hold the socket for the full timeout.
	authTimer *time.Timer
}

// NewRegistry returns an empty registry enforcing limits.
func NewRegistry(limits Limits) *Registry {
	return &Registry{limits: limits, conns: map[string]*entry{}}
}

// Limits returns the limits the registry was built with.
func (r *Registry) Limits() Limits { return r.limits }

// Register adds a connected socket and removes it again on disconnect. It
// reports false, registering nothing, when the process is at its connection
// limit. A socket that Authenticate has not bound within AuthTimeout is
// dropped.
func (r *Registry) Register(a *AuthSocket) bool {
	id := string(a.Socket().Id())
	r.mu.Lock()
	if r.limits.MaxConnections != -1 && len(r.conns) >= r.limits.MaxConnections {
		r.mu.Unlock()
		return false
	}
	r.seq++
	e := &entry{sock: a, seq: r.seq, rooms: map[string]struct{}{}}
	if d := r.limits.AuthTimeout; d > 0 {
		e.authTimer = time.AfterFunc(d, func() {
			if r.Identity(id) == "" && a.Socket().Connected() {
				slog.Warn("dropping socket: not authenticated in time", "sid", id)
				Drop(a.Socket())
			}
		})
	}
	r.conns[id] = e
	r.mu.Unlock()

	remove := func(...any) {
		r.mu.Lock()
		delete(r.conns, id)
		r.mu.Unlock()
		e.stopAuthTimer()
	}
	_ = a.Socket().On("disconnect", remove)
	// disconnect runs off the socket's queue, so it may already have fired.
	if !a.Socket().Connected() {
		remove()
		return false
	}
	return true
}

func (e *entry) stopAuthTimer() {
	if e.authTimer != nil {
		e.authTimer.Stop()
	}
}

// Authenticate binds identity to a registered socket. It reports false for an
// unregistered socket, a socket already bound to another identity, or an
// identity at its per-identity connection limit.
func (r *Registry) Authenticate(socketID, identity string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.conns[socketID]
	if e == nil {
		return false
	}
	if e.identity != "" {
		return e.identity == identity
	}
	if limit := r.limits.MaxConnectionsPerIdentity; limit != -1 {
		n := 0
		for _, o := range r.conns {
			if o.identity == identity {
				n++
			}
		}
		if n >= limit {
			return false
		}
	}
	e.identity = identity
	e.stopAuthTimer()
	return true
}

// Identity is the identity a socket authenticated as, or "".
func (r *Registry) Identity(socketID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.conns[socketID]; e != nil {
		return e.identity
	}
	return ""
}

// Join adds room to an authenticated socket. Joining a room twice is a no-op;
// it reports false when unauthenticated or at the per-connection room limit.
func (r *Registry) Join(socketID, room string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.conns[socketID]
	if e == nil || e.identity == "" {
		return false
	}
	if _, ok := e.rooms[room]; ok {
		return true
	}
	if limit := r.limits.MaxRoomsPerConnection; limit != -1 && len(e.rooms) >= limit {
		return false
	}
	e.rooms[room] = struct{}{}
	return true
}

// Leave removes room from a socket.
func (r *Registry) Leave(socketID, room string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.conns[socketID]; e != nil {
		delete(e.rooms, room)
	}
}

// recipients returns the newest MaxRecipientConnections sockets that
// authenticated as recipient and joined room. Newest-first, so a pile of old
// tabs cannot crowd out the one that just joined.
func (r *Registry) recipients(recipient, room string) []*AuthSocket {
	r.mu.Lock()
	var matching []*entry
	for _, e := range r.conns {
		if _, joined := e.rooms[room]; joined && e.identity == recipient {
			matching = append(matching, e)
		}
	}
	r.mu.Unlock()

	slices.SortFunc(matching, func(a, b *entry) int { return cmp.Compare(a.seq, b.seq) })
	if limit := r.limits.MaxRecipientConnections; limit != -1 && len(matching) > limit {
		matching = matching[len(matching)-limit:]
	}
	out := make([]*AuthSocket, len(matching))
	for i, e := range matching {
		out[i] = e.sock
	}
	return out
}

// Deliver pushes a stored message to the recipient's sockets joined to the
// room "<recipient>-<messageBox>", as sendMessage-<room> {sender, messageId,
// body}: the event and shape the TypeScript server's deliverLiveMessage uses.
// A failed push is logged; the message stays stored for listMessages.
func (r *Registry) Deliver(ctx context.Context, sender, recipient, messageBox, messageID, body string) {
	room := recipient + "-" + messageBox
	payload := map[string]any{"sender": sender, "messageId": messageID, "body": body}
	for _, a := range r.recipients(recipient, room) {
		pushCtx, cancel := context.WithTimeout(ctx, pushTimeout)
		if err := a.Emit(pushCtx, "sendMessage-"+room, payload); err != nil {
			slog.Warn("live delivery failed", "sid", a.Socket().Id(), "error", err)
		}
		cancel()
	}
}

// MinuteLimiter is a fixed one-minute window counter, the TypeScript server's
// WebSocketMinuteRateLimiter. -1 means unlimited.
type MinuteLimiter struct {
	limit int
	now   func() time.Time

	mu    sync.Mutex
	start time.Time
	n     int
}

// NewMinuteLimiter returns a limiter allowing limit events per minute.
func NewMinuteLimiter(limit int) *MinuteLimiter {
	return &MinuteLimiter{limit: limit, now: time.Now, start: time.Now()}
}

// Allow consumes one event, reporting false when the window is full.
func (l *MinuteLimiter) Allow() bool {
	if l.limit == -1 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if now := l.now(); now.Sub(l.start) >= time.Minute {
		l.start, l.n = now, 0
	}
	if l.n >= l.limit {
		return false
	}
	l.n++
	return true
}
