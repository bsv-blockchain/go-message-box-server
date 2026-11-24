package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bsv-blockchain/go-message-box-server/pkg/handles"
	"github.com/bsv-blockchain/go-message-box-server/pkg/p2p"
	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert"
	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert/certtest"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

const testAvatar = "https://example.com/avatar.png"

// p2pEnv is a lookup server with the paymail receive routes on. Both muxes
// share the one server, as in production.
type p2pEnv struct {
	t      *testing.T
	srv    *Server
	lookup *http.ServeMux
	p2p    *http.ServeMux
	now    time.Time
}

func newP2PEnv(t *testing.T, enableP2P bool) *p2pEnv {
	t.Helper()
	e := newLookupEnv(t)
	env := &p2pEnv{t: t, srv: e.srv, lookup: e.mux, now: e.now()}
	if enableP2P {
		e.srv.EnableP2P(P2PConfig{
			DestinationTTL: 24 * time.Hour, MaxSatoshis: 1_000_000_000, AvatarURL: testAvatar,
		}, e.srv.Store.(*fakeStore))
		env.p2p = e.srv.P2PRoutes()
	}
	return env
}

// register claims handle for a fresh key and returns that key.
func (e *p2pEnv) register(handle string) string {
	e.t.Helper()
	cert := certtest.Profile(e.t, testKey(e.t), handle, testDomain, e.now, nil)
	p, err := profilecert.Parse(e.t.Context(), cert, testDomain, e.now)
	require.NoError(e.t, err)
	_, err = e.srv.Store.(*fakeStore).ClaimHandle(e.t.Context(), storage.HandleClaim{
		Handle: p.Handle, Skeleton: handles.Skeleton(p.Handle), IdentityKey: p.IdentityKey,
		Certificate: p.JSON, SerialNumber: p.SerialNumber, IssuedAt: p.IssuedAt, Now: e.now,
	})
	require.NoError(e.t, err)
	return p.IdentityKey
}

func (e *p2pEnv) get(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(e.t.Context(), http.MethodGet, path, nil))
	return w
}

func TestWellKnownWithoutP2P(t *testing.T) {
	e := newP2PEnv(t, false)
	w := e.get(e.lookup, "/.well-known/bsvalias")
	require.Equal(t, 200, w.Code)
	var doc struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
	assert.Len(t, doc.Capabilities, 2)
	assert.Contains(t, doc.Capabilities, handles.BRFCLookup)
	assert.Contains(t, doc.Capabilities, handles.BRFCReverseLookup)
}

func TestWellKnownWithP2P(t *testing.T) {
	e := newP2PEnv(t, true)
	w := e.get(e.lookup, "/.well-known/bsvalias")
	require.Equal(t, 200, w.Code)
	var doc struct {
		Bsvalias     string                     `json:"bsvalias"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
	assert.Equal(t, "1.0", doc.Bsvalias)
	str := func(k string) string {
		var s string
		require.NoError(t, json.Unmarshal(doc.Capabilities[k], &s), k)
		return s
	}
	const h = "https://mb.example.com/api/bsvalias/"
	const addr = "/{alias}@{domain.tld}"
	assert.Equal(t, h+"id"+addr, str("pki"))
	assert.Equal(t, h+"public-profile"+addr, str("f12f968c92d6"))
	assert.Equal(t, h+"p2p-payment-destination"+addr, str("2a40af698840"))
	assert.Equal(t, h+"receive-transaction"+addr, str("5f1323cddf31"))
	assert.Equal(t, h+"beef"+addr, str("5c55a7fdb7bb"))
	assert.JSONEq(t, "false", string(doc.Capabilities["6745385c3fc0"]))
	assert.Contains(t, doc.Capabilities, handles.BRFCLookup)
	assert.Contains(t, doc.Capabilities, handles.BRFCReverseLookup)
	assert.Len(t, doc.Capabilities, 8)
}

func TestPKI(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")

	w := e.get(e.p2p, "/api/bsvalias/id/rosa@example.com")
	require.Equal(t, 200, w.Code)
	assert.JSONEq(t, `{"bsvalias":"1.0","handle":"rosa@example.com","pubkey":"`+p2p.AnyonePublicKeyHex()+`"}`, w.Body.String())

	assert.Equal(t, 404, e.get(e.p2p, "/api/bsvalias/id/nobody@example.com").Code)
	assert.Equal(t, 400, e.get(e.p2p, "/api/bsvalias/id/rosa@other.com").Code)
	assert.Equal(t, 400, e.get(e.p2p, "/api/bsvalias/id/rosa").Code)

	// A released handle stays on record but belongs to nobody.
	until := e.now.Add(30 * 24 * time.Hour)
	require.NoError(t, e.srv.Store.(*fakeStore).ReleaseHandle(t.Context(), storage.HandleRelease{
		Handle: "rosa", ReleasedBy: storage.ReleasedByOwner, CooldownUntil: &until, Now: e.now,
	}))
	assert.Equal(t, 404, e.get(e.p2p, "/api/bsvalias/id/rosa@example.com").Code)
}

func TestPublicProfile(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")

	w := e.get(e.p2p, "/api/bsvalias/public-profile/rosa@example.com")
	require.Equal(t, 200, w.Code)
	assert.JSONEq(t, `{"name":"rosa","avatar":"`+testAvatar+`"}`, w.Body.String())
	assert.Equal(t, 404, e.get(e.p2p, "/api/bsvalias/public-profile/nobody@example.com").Code)
}

func TestHandleCaseFolding(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")

	a := e.get(e.p2p, "/api/bsvalias/id/rosa@example.com")
	b := e.get(e.p2p, "/api/bsvalias/id/Rosa@Example.com")
	require.Equal(t, 200, b.Code)
	// The response echoes the canonical handle, not what the sender typed.
	assert.Equal(t, a.Body.String(), b.Body.String())
}
