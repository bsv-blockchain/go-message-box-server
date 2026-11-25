package handlers

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bsv-blockchain/go-message-box-server/pkg/livesocket"
	"github.com/bsv-blockchain/go-message-box-server/pkg/p2p"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// A wallet listening on its socket reads the pushed text as the token itself:
// message-box-client does not unwrap {"message": ...} on the live path, only
// when it lists stored messages.
func TestP2PBeefLivePushIsTheToken(t *testing.T) {
	e := newP2PEnv(t, true)
	e.srv.wallet, _ = newProtoWallet(t)
	io := livesocket.NewServer(livesocket.Options{})
	t.Cleanup(func() { io.Close(nil) })
	e.srv.AttachWebSockets(io, livesocket.NewRegistry(livesocket.DefaultLimits()))
	mux := http.NewServeMux()
	mux.Handle(livesocket.Path, io.ServeHandler(nil))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	alice := dialLive(t, ts.URL)
	alice.authenticate()
	room := alice.identity + "-" + liveBox
	alice.emit("joinRoom", room)
	alice.expect("joinedRoom")

	// A destination issued for the listening wallet's key.
	derived, err := p2p.Derive(alice.identity, rand.Reader)
	require.NoError(t, err)
	const ref = "0123456789abcdef0123456789abcdef"
	require.NoError(t, e.srv.Store.(*fakeStore).CreateDestination(t.Context(), storage.P2PDestination{
		Reference: ref, Handle: "rosa", IdentityKey: alice.identity, Script: derived.Script, Satoshis: 1000,
		DerivationPrefix: derived.Prefix, DerivationSuffix: derived.Suffix,
		CreatedAt: e.now, AcceptUntil: e.now.Add(time.Hour), PurgeAt: e.now.Add(time.Hour),
	}))
	pay := newPayment(t, derived.Script, 1000)
	w := e.post(beefPath, beefBody(t, pay.v1(t), ref, nil))
	require.Equal(t, 200, w.Code, w.Body.String())

	push := alice.expect("sendMessage-" + room)
	text, _ := push["body"].(string)
	var tok p2p.Token
	require.NoError(t, json.Unmarshal([]byte(text), &tok))
	assert.Equal(t, derived.Prefix, tok.CustomInstructions.DerivationPrefix, "live text must be the token, got %q", text)
	assert.Equal(t, derived.Suffix, tok.CustomInstructions.DerivationSuffix)
	assert.EqualValues(t, 1000, tok.Amount)
}
