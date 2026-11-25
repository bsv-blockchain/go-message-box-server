package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingBroadcaster waits for its context to end, as a hung upstream would.
type blockingBroadcaster struct{}

func (blockingBroadcaster) Broadcast(ctx context.Context, _ *transaction.Transaction) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// blockingVerifier waits for its context to end.
type blockingVerifier struct{}

func (blockingVerifier) Verify(ctx context.Context, _ *transaction.Transaction) error {
	<-ctx.Done()
	return ctx.Err()
}

func shortenSettleTimeout(t *testing.T) {
	t.Helper()
	old := p2pSettleTimeout
	p2pSettleTimeout = 100 * time.Millisecond
	t.Cleanup(func() { p2pSettleTimeout = old })
}

func TestP2PSettleBroadcastTimeout(t *testing.T) {
	shortenSettleTimeout(t)
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	e.srv.p2p.Broadcaster = blockingBroadcaster{}

	start := time.Now()
	w := e.post(beefPath, beefBody(t, pay.v1(t), d.ref, nil))
	require.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, 503, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_BROADCAST_UNAVAILABLE")
	assert.Empty(t, e.inbox(idKey))
}

func TestP2PSettleVerifyTimeout(t *testing.T) {
	shortenSettleTimeout(t)
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	e.srv.p2p.Verifier = blockingVerifier{}

	start := time.Now()
	w := e.post(beefPath, beefBody(t, pay.v1(t), d.ref, nil))
	require.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, 503, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VERIFY_UNAVAILABLE")
	assert.Zero(t, e.bc.calls)
}

// blockingExtendStore holds the retention extend until its context ends.
type blockingExtendStore struct{ *fakeStore }

func (blockingExtendStore) ExtendDestinationRetention(ctx context.Context, _ string, _ time.Time) error {
	<-ctx.Done()
	return ctx.Err()
}

// A settle deadline that ends during the extend is a timeout, not a storage
// fault, so the sender gets the same retryable 503 as the other steps.
func TestP2PSettleExtendTimeout(t *testing.T) {
	shortenSettleTimeout(t)
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	e.srv.destinations = blockingExtendStore{e.srv.Store.(*fakeStore)}

	w := e.post(beefPath, beefBody(t, newPayment(t, d.script, d.sats).v1(t), d.ref, nil))
	assert.Equal(t, 503, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "ERR_BROADCAST_UNAVAILABLE")
	assert.Zero(t, e.bc.calls)
	assert.Empty(t, e.inbox(idKey))
}
