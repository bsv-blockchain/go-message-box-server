package livesocket

import (
	"errors"
	"sync"
	"time"

	"github.com/bsv-blockchain/go-sdk/auth"
)

// DefaultResumeWindow is how long a closed connection's sessions stay
// resumable. socket.io reconnects within seconds of the server coming back;
// this covers outages the process itself survives.
const DefaultResumeWindow = 10 * time.Minute

// SessionStore lets a socket.io reconnect carry on with the BRC-103 session it
// had, as @bsv/authsocket's AuthSocketServer allows with its one process-wide
// SessionManager, without that manager's hazard (compose.ts): there, a reply
// to an identity key can go out under another tab's session. Here each
// connection looks sessions up by identity key among its own only. Another
// connection's session is reached only by its exact session nonce, which a
// client presents as yourNonce for the session it holds, and every message on
// it is still signed under that session. Replay claims on message nonces are
// process-wide, so a frame captured on one connection fails on the next.
type SessionStore struct {
	window time.Duration
	now    func() time.Time
	claims *auth.DefaultSessionManager // nonce claims only, never sessions

	mu        sync.Mutex
	sessions  map[string]*storedSession
	lastPrune time.Time
}

type storedSession struct {
	s        *auth.PeerSession
	owners   int       // open connections holding it
	detached time.Time // when owners last dropped to 0
}

// NewSessionStore keeps a session resumable for window after its last
// connection closes.
func NewSessionStore(window time.Duration) *SessionStore {
	return &SessionStore{
		window:   window,
		now:      time.Now,
		claims:   auth.NewSessionManager(),
		sessions: map[string]*storedSession{},
	}
}

// Conn returns the session manager for one connection. Call Release when the
// connection closes.
func (st *SessionStore) Conn() *ConnSessions {
	return &ConnSessions{store: st, local: auth.NewSessionManager(), held: map[string]struct{}{}}
}

func (st *SessionStore) attach(s *auth.PeerSession) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked()
	if e := st.sessions[s.SessionNonce]; e != nil {
		e.s = s
		e.owners++
		return
	}
	st.sessions[s.SessionNonce] = &storedSession{s: s, owners: 1}
}

// adopt hands the session with this exact nonce to another connection.
func (st *SessionStore) adopt(nonce string) *auth.PeerSession {
	st.mu.Lock()
	defer st.mu.Unlock()
	e := st.sessions[nonce]
	if e == nil || (e.owners == 0 && st.now().Sub(e.detached) > st.window) {
		return nil
	}
	e.owners++
	return e.s
}

func (st *SessionStore) has(nonce string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	e := st.sessions[nonce]
	return e != nil && (e.owners > 0 || st.now().Sub(e.detached) <= st.window)
}

func (st *SessionStore) detach(nonce string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if e := st.sessions[nonce]; e != nil && e.owners > 0 {
		e.owners--
		if e.owners == 0 {
			e.detached = st.now()
		}
	}
}

func (st *SessionStore) remove(nonce string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.dropLocked(nonce)
}

func (st *SessionStore) dropLocked(nonce string) {
	delete(st.sessions, nonce)
	st.claims.RemoveSession(&auth.PeerSession{SessionNonce: nonce})
}

// pruneLocked forgets sessions detached for longer than the window, at most
// once a second.
func (st *SessionStore) pruneLocked() {
	now := st.now()
	if now.Sub(st.lastPrune) < time.Second {
		return
	}
	st.lastPrune = now
	for nonce, e := range st.sessions {
		if e.owners == 0 && now.Sub(e.detached) > st.window {
			st.dropLocked(nonce)
		}
	}
}

// ConnSessions is one connection's auth.SessionManager and auth.NonceClaimer.
type ConnSessions struct {
	store *SessionStore
	local *auth.DefaultSessionManager

	mu       sync.Mutex
	held     map[string]struct{}
	released bool
}

var (
	_ auth.SessionManager = (*ConnSessions)(nil)
	_ auth.NonceClaimer   = (*ConnSessions)(nil)
)

func (c *ConnSessions) hold(s *auth.PeerSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.held[s.SessionNonce]; ok || c.released {
		return
	}
	c.held[s.SessionNonce] = struct{}{}
	c.store.attach(s)
}

// AddSession implements auth.SessionManager.
func (c *ConnSessions) AddSession(s *auth.PeerSession) error {
	if err := c.local.AddSession(s); err != nil {
		return err
	}
	c.hold(s)
	return nil
}

// UpdateSession implements auth.SessionManager.
func (c *ConnSessions) UpdateSession(s *auth.PeerSession) {
	c.local.UpdateSession(s)
	if s.SessionNonce != "" {
		c.hold(s)
	}
}

// GetSession implements auth.SessionManager: this connection's sessions by
// nonce or identity key, then another connection's by its exact nonce only.
func (c *ConnSessions) GetSession(id string) (*auth.PeerSession, error) {
	if s, err := c.local.GetSession(id); err == nil && s != nil {
		return s, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released {
		return nil, errors.New("session-not-found")
	}
	s := c.store.adopt(id)
	if s == nil {
		return nil, errors.New("session-not-found")
	}
	c.held[id] = struct{}{}
	_ = c.local.AddSession(s)
	return s, nil
}

// HasSession implements auth.SessionManager.
func (c *ConnSessions) HasSession(id string) bool {
	return c.local.HasSession(id) || c.store.has(id)
}

// RemoveSession implements auth.SessionManager.
func (c *ConnSessions) RemoveSession(s *auth.PeerSession) {
	c.local.RemoveSession(s)
	c.mu.Lock()
	delete(c.held, s.SessionNonce)
	c.mu.Unlock()
	c.store.remove(s.SessionNonce)
}

// ClaimMessageNonce implements auth.NonceClaimer, process-wide.
func (c *ConnSessions) ClaimMessageNonce(sessionNonce, messageNonce string) bool {
	return c.store.claims.ClaimMessageNonce(sessionNonce, messageNonce)
}

// ClaimInitialRequestNonce implements auth.NonceClaimer, process-wide.
func (c *ConnSessions) ClaimInitialRequestNonce(identityKey, initialNonce string) bool {
	return c.store.claims.ClaimInitialRequestNonce(identityKey, initialNonce)
}

// Release marks the connection closed: its sessions stay resumable for the
// store's window.
func (c *ConnSessions) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released {
		return
	}
	c.released = true
	for nonce := range c.held {
		c.store.detach(nonce)
	}
	c.held = nil
}
