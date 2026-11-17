package livesocket

import (
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/auth"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
)

func session(t *testing.T, nonce string, key *ec.PublicKey, lastUpdate int64) *auth.PeerSession {
	t.Helper()
	return &auth.PeerSession{IsAuthenticated: true, SessionNonce: nonce, PeerIdentityKey: key, LastUpdate: lastUpdate}
}

func identity(t *testing.T) *ec.PublicKey {
	t.Helper()
	k, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k.PubKey()
}

// Two tabs of one wallet: a reply to the identity key on one connection must
// use that connection's session, never the other tab's newer one (the hazard
// of @bsv/authsocket's shared SessionManager).
func TestConnSessionsIdentityLookupStaysLocal(t *testing.T) {
	store := NewSessionStore(time.Minute)
	key := identity(t)
	a, b := store.Conn(), store.Conn()
	if err := a.AddSession(session(t, "tab-a", key, 1)); err != nil {
		t.Fatal(err)
	}
	if err := b.AddSession(session(t, "tab-b", key, 2)); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*ConnSessions{"tab-a": a, "tab-b": b} {
		s, err := c.GetSession(key.ToDERHex())
		if err != nil || s == nil || s.SessionNonce != name {
			t.Fatalf("%s: identity lookup gave %+v, %v", name, s, err)
		}
	}
}

// A message nonce claimed on one connection is a replay on any other.
func TestConnSessionsReplayIsProcessWide(t *testing.T) {
	store := NewSessionStore(time.Minute)
	if !store.Conn().ClaimMessageNonce("s", "m") {
		t.Fatal("first claim refused")
	}
	if store.Conn().ClaimMessageNonce("s", "m") {
		t.Fatal("replay accepted on another connection")
	}
}

// A session its Peer removes is gone for every connection, not only its own.
func TestConnSessionsRemoveIsProcessWide(t *testing.T) {
	store := NewSessionStore(time.Minute)
	owner, other := store.Conn(), store.Conn()
	s := session(t, "n", identity(t), 1)
	if err := owner.AddSession(s); err != nil {
		t.Fatal(err)
	}
	if !other.HasSession("n") {
		t.Fatal("open session not visible by nonce")
	}
	owner.RemoveSession(s)
	if owner.HasSession("n") || other.HasSession("n") {
		t.Fatal("removed session still reported")
	}
	if got, err := other.GetSession("n"); err == nil {
		t.Fatalf("removed session resumed: %+v", got)
	}
}

// A closed connection's session is resumable for the window, then gone.
func TestConnSessionsResumeWindow(t *testing.T) {
	store := NewSessionStore(time.Minute)
	now := time.Unix(1_000, 0)
	store.now = func() time.Time { return now }
	old := store.Conn()
	if err := old.AddSession(session(t, "n", identity(t), 1)); err != nil {
		t.Fatal(err)
	}
	probe := store.Conn()
	if s, err := probe.GetSession("n"); err != nil || s == nil {
		t.Fatalf("open connection's session not resumable: %v", err)
	}
	probe.Release()
	old.Release()

	now = now.Add(59 * time.Second)
	resumed := store.Conn()
	if s, err := resumed.GetSession("n"); err != nil || s == nil {
		t.Fatalf("within the window: %v", err)
	}
	resumed.Release()

	now = now.Add(61 * time.Second)
	if s, err := store.Conn().GetSession("n"); err == nil {
		t.Fatalf("resumed %+v after the window", s)
	}
}
