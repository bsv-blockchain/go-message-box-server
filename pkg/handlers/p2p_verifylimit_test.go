package handlers

import (
	"context"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// extendCounter counts the retention extends that reach the store.
type extendCounter struct {
	*fakeStore

	mu      sync.Mutex
	extends int
}

func (c *extendCounter) ExtendDestinationRetention(ctx context.Context, ref string, until time.Time) error {
	c.mu.Lock()
	c.extends++
	c.mu.Unlock()
	return c.fakeStore.ExtendDestinationRetention(ctx, ref, until)
}

func (c *extendCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.extends
}

func countExtends(e *p2pEnv) *extendCounter {
	c := &extendCounter{fakeStore: e.srv.Store.(*fakeStore)}
	e.srv.destinations = c
	return c
}

// holdAllSlots takes every verification slot, as long-running verifications
// would, and gives them back when the test ends.
func holdAllSlots(t *testing.T, e *p2pEnv) {
	t.Helper()
	n := cap(e.srv.verifySlots)
	require.Positive(t, n)
	for range n {
		e.srv.verifySlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range n {
			<-e.srv.verifySlots
		}
	})
}

func assertNothingSettled(t *testing.T, e *p2pEnv, c *extendCounter, ref, idKey string) {
	t.Helper()
	assert.Zero(t, e.vf.calls, "verifier")
	assert.Zero(t, c.count(), "extend")
	assert.Zero(t, e.bc.calls, "broadcast")
	d, err := e.srv.Store.(*fakeStore).GetDestination(t.Context(), ref, e.now)
	require.NoError(t, err)
	assert.Empty(t, d.UsedTxID, "mark")
	assert.Empty(t, e.inbox(idKey))
}

func TestP2PVerifySlotsFullBeef(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	c := countExtends(e)
	holdAllSlots(t, e)

	start := time.Now()
	w := e.post(beefPath, beefBody(t, newPayment(t, d.script, d.sats).v1(t), d.ref, nil))
	assert.Less(t, time.Since(start), time.Second, "no queueing")
	assert.Equal(t, 503, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "ERR_VERIFY_UNAVAILABLE")
	assertNothingSettled(t, e, c, d.ref, idKey)
}

func TestP2PVerifySlotsFullRaw(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	e.srv.p2p.Parents = newFakeParents(pay.parent)
	c := countExtends(e)
	holdAllSlots(t, e)

	w := e.post(rawPath, rawBody(t, rawHex(pay.child), d.ref, nil))
	assert.Equal(t, 503, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "ERR_VERIFY_UNAVAILABLE")
	assertNothingSettled(t, e, c, d.ref, idKey)
}

// The slot covers Verify only: a failed verification gives it back, so the
// next payment is verified.
func TestP2PVerifySlotReleasedAfterVerify(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	e.vf.err = context.DeadlineExceeded
	for range cap(e.srv.verifySlots) + 2 {
		w := e.post(beefPath, beefBody(t, newPayment(t, d.script, d.sats).v1(t), d.ref, nil))
		require.Equal(t, 503, w.Code)
	}
	assert.Equal(t, cap(e.srv.verifySlots)+2, e.vf.calls)
	assert.Empty(t, e.srv.verifySlots)
}

func TestP2PVerifyConcurrencyDefault(t *testing.T) {
	e := newP2PEnv(t, true)
	assert.Equal(t, defaultVerifyConcurrency, cap(e.srv.verifySlots))
}

// expensivePayment is the review's probe: a mined P2PKH grandparent, an
// unmined parent whose output squares an 8 MB number, and a child that spends
// it and pays the destination.
func expensivePayment(t *testing.T, scriptHex string, sats uint64) *transaction.Transaction {
	t.Helper()
	key, err := ec.NewPrivateKey()
	require.NoError(t, err)
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), true)
	require.NoError(t, err)
	lock, err := p2pkh.Lock(addr)
	require.NoError(t, err)
	grand := transaction.NewTransaction()
	grand.AddOutput(&transaction.TransactionOutput{Satoshis: sats + 10000, LockingScript: lock})
	grand.MerklePath = fakeMerklePath(t, grand)

	heavy := &script.Script{}
	require.NoError(t, heavy.AppendPushData([]byte{1}))
	require.NoError(t, heavy.AppendPushData([]byte{0x00, 0x12, 0x7a}))
	require.NoError(t, heavy.AppendOpcodes(script.OpNUM2BIN, script.OpINVERT))
	for range 8 {
		require.NoError(t, heavy.AppendOpcodes(script.OpDUP, script.OpDUP, script.OpMUL, script.OpDROP))
	}
	unlock, err := p2pkh.Unlock(key, nil)
	require.NoError(t, err)
	parent := transaction.NewTransaction()
	parent.AddInputFromTx(grand, 0, unlock)
	parent.AddOutput(&transaction.TransactionOutput{Satoshis: sats + 5000, LockingScript: heavy})
	require.NoError(t, parent.Sign())

	child := transaction.NewTransaction()
	child.AddInput(&transaction.TransactionInput{
		SourceTXID: parent.TxID(), SourceTxOutIndex: 0, SourceTransaction: parent, UnlockingScript: &script.Script{},
	})
	dest, err := script.NewFromHex(scriptHex)
	require.NoError(t, err)
	child.AddOutput(&transaction.TransactionOutput{Satoshis: sats, LockingScript: dest})
	return child
}

func TestP2PBeefNonStandardSpendRefused(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	c := countExtends(e)
	b, err := expensivePayment(t, d.script, d.sats).BEEF()
	require.NoError(t, err)

	start := time.Now()
	w := e.post(beefPath, beefBody(t, hex.EncodeToString(b), d.ref, nil))
	assert.Less(t, time.Since(start), time.Second)
	assert.Equal(t, 400, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "ERR_NONSTANDARD")
	assertNothingSettled(t, e, c, d.ref, idKey)
}

// Verify has its own deadline, shorter than the settle deadline.
func TestP2PVerifyDeadline(t *testing.T) {
	old := p2pVerifyTimeout
	p2pVerifyTimeout = 100 * time.Millisecond
	t.Cleanup(func() { p2pVerifyTimeout = old })
	require.Less(t, p2pVerifyTimeout, p2pSettleTimeout)

	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	e.srv.p2p.Verifier = blockingVerifier{}
	start := time.Now()
	w := e.post(beefPath, beefBody(t, newPayment(t, d.script, d.sats).v1(t), d.ref, nil))
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, 503, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VERIFY_UNAVAILABLE")
	assert.Zero(t, e.bc.calls)
}

var _ storage.P2PDestinationStore = (*extendCounter)(nil)
