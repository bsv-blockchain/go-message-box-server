package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bsv-blockchain/go-message-box-server/pkg/p2p"
)

// fakeParents serves transactions by txid and counts the calls per txid.
type fakeParents struct {
	mu    sync.Mutex
	txs   map[string]*transaction.Transaction
	calls map[string]int
	err   error
}

func newFakeParents(txs ...*transaction.Transaction) *fakeParents {
	f := &fakeParents{txs: map[string]*transaction.Transaction{}, calls: map[string]int{}}
	for _, tx := range txs {
		f.txs[tx.TxID().String()] = tx
	}
	return f
}

func (f *fakeParents) Parent(_ context.Context, txid string) (*transaction.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[txid]++
	if f.err != nil {
		return nil, f.err
	}
	tx, ok := f.txs[txid]
	if !ok {
		return nil, p2p.ErrParentNotFound
	}
	return tx, nil
}

const rawPath = "/api/bsvalias/receive-transaction/rosa@example.com"

func rawBody(t *testing.T, hex, ref string, meta map[string]string) string {
	t.Helper()
	m := map[string]any{"hex": hex, "reference": ref}
	if meta != nil {
		m["metadata"] = meta
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

func rawHex(tx *transaction.Transaction) string {
	// The sender posts the transaction without its ancestors.
	c, _ := transaction.NewTransactionFromBytes(tx.Bytes())
	return c.Hex()
}

func TestP2PRawAcceptsParentFromFetcher(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	parents := newFakeParents(pay.parent)
	e.srv.p2p.Parents = parents

	w := e.post(rawPath, rawBody(t, rawHex(pay.child), d.ref, map[string]string{"note": "hi"}))
	require.Equal(t, 200, w.Code, w.Body.String())
	txid := pay.child.TxID().String()
	assert.JSONEq(t, `{"txid":"`+txid+`","note":""}`, w.Body.String())
	assert.Equal(t, []string{txid}, e.bc.seen)

	msgs := e.inbox(idKey)
	require.Len(t, msgs, 1)
	assert.Equal(t, p2pMessageID(d.ref, txid), msgs[0].MessageID)
	tok := decodeToken(t, msgs[0].Body)
	assert.Equal(t, "hi", tok.Note)
	assert.EqualValues(t, 1000, tok.Amount)
	beef, subject, err := transaction.NewBeefFromAtomicBytes(tokenBytes(tok))
	require.NoError(t, err)
	assert.Equal(t, txid, subject.String())
	assert.Len(t, beef.Transactions, 2, "the token holds the parent")
}

func TestP2PRawTwoInputsOneParentFetchedOnce(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)

	key, err := ec.NewPrivateKey()
	require.NoError(t, err)
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), true)
	require.NoError(t, err)
	lock, err := p2pkh.Lock(addr)
	require.NoError(t, err)
	parent := transaction.NewTransaction()
	parent.AddOutput(&transaction.TransactionOutput{Satoshis: 3000, LockingScript: lock})
	parent.AddOutput(&transaction.TransactionOutput{Satoshis: 3000, LockingScript: lock})
	parent.MerklePath = fakeMerklePath(t, parent)
	unlock, err := p2pkh.Unlock(key, nil)
	require.NoError(t, err)
	child := transaction.NewTransaction()
	child.AddInputFromTx(parent, 0, unlock)
	child.AddInputFromTx(parent, 1, unlock)
	dest, err := script.NewFromHex(d.script)
	require.NoError(t, err)
	child.AddOutput(&transaction.TransactionOutput{Satoshis: d.sats, LockingScript: dest})
	require.NoError(t, child.Sign())

	parents := newFakeParents(parent)
	e.srv.p2p.Parents = parents
	w := e.post(rawPath, rawBody(t, rawHex(child), d.ref, nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 1, parents.calls[parent.TxID().String()])
}

func TestP2PRawUnminedParent(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)

	// grandparent (mined) -> parent (unmined) -> child (pays).
	key, err := ec.NewPrivateKey()
	require.NoError(t, err)
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), true)
	require.NoError(t, err)
	lock, err := p2pkh.Lock(addr)
	require.NoError(t, err)
	unlock, err := p2pkh.Unlock(key, nil)
	require.NoError(t, err)
	gp := transaction.NewTransaction()
	gp.AddOutput(&transaction.TransactionOutput{Satoshis: 20000, LockingScript: lock})
	gp.MerklePath = fakeMerklePath(t, gp)
	parent := transaction.NewTransaction()
	parent.AddInputFromTx(gp, 0, unlock)
	parent.AddOutput(&transaction.TransactionOutput{Satoshis: 10000, LockingScript: lock})
	require.NoError(t, parent.Sign())
	child := transaction.NewTransaction()
	child.AddInputFromTx(parent, 0, unlock)
	dest, err := script.NewFromHex(d.script)
	require.NoError(t, err)
	child.AddOutput(&transaction.TransactionOutput{Satoshis: d.sats, LockingScript: dest})
	require.NoError(t, child.Sign())

	// What the real fetcher returns for an unmined parent: the parent with its
	// own parent attached.
	served, err := transaction.NewTransactionFromBytes(parent.Bytes())
	require.NoError(t, err)
	served.Inputs[0].SourceTransaction = gp
	e.srv.p2p.Parents = newFakeParents(served)

	w := e.post(rawPath, rawBody(t, rawHex(child), d.ref, nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	msgs := e.inbox(idKey)
	require.Len(t, msgs, 1)
	beef, _, err := transaction.NewBeefFromAtomicBytes(tokenBytes(decodeToken(t, msgs[0].Body)))
	require.NoError(t, err)
	assert.Len(t, beef.Transactions, 3, "the token holds the parent and the grandparent")
}

func TestP2PRawParentUnavailable(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	parents := newFakeParents(pay.parent)
	parents.err = errors.New("woc down")
	e.srv.p2p.Parents = parents

	w := e.post(rawPath, rawBody(t, rawHex(pay.child), d.ref, nil))
	assert.Equal(t, 503, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_PARENT_UNAVAILABLE")
	assert.Empty(t, e.bc.seen)
	assert.Empty(t, e.inbox(idKey))
	stored, _ := e.srv.Store.(*fakeStore).GetDestination(t.Context(), d.ref, e.now)
	assert.Empty(t, stored.UsedTxID)
}

func TestP2PRawInvalidHex(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	for _, h := range []string{"", "zz", "00"} {
		w := e.post(rawPath, rawBody(t, h, d.ref, nil))
		assert.Equal(t, 400, w.Code, h)
		assert.Contains(t, w.Body.String(), "ERR_INVALID_TX", h)
	}
	assert.Empty(t, e.bc.seen)
}

func TestP2PRawDestinationMismatch(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats-1) // right script, wrong amount
	parents := newFakeParents(pay.parent)
	e.srv.p2p.Parents = parents

	w := e.post(rawPath, rawBody(t, rawHex(pay.child), d.ref, nil))
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_NO_MATCHING_OUTPUT")
	assert.Empty(t, e.bc.seen)
	assert.Empty(t, parents.calls, "the output is checked before any parent is fetched")
}

func TestP2PRawParentNotFoundIsClientError(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	e.srv.p2p.Parents = newFakeParents() // knows no parent

	w := e.post(rawPath, rawBody(t, rawHex(pay.child), d.ref, nil))
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_INVALID_TX")
	assert.Empty(t, e.bc.seen)
}

func TestP2PRawTooManyInputs(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	child, err := transaction.NewTransactionFromBytes(pay.child.Bytes())
	require.NoError(t, err)
	for i := 0; i <= maxRawParents; i++ {
		var h chainhash.Hash
		h[0], h[1] = byte(i), byte(i>>8)
		child.Inputs = append(child.Inputs, &transaction.TransactionInput{
			SourceTXID: &h, SequenceNumber: 0xffffffff, UnlockingScript: &script.Script{},
		})
	}
	parents := newFakeParents(pay.parent)
	e.srv.p2p.Parents = parents

	w := e.post(rawPath, rawBody(t, child.Hex(), d.ref, nil))
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_TOO_MANY_INPUTS")
	assert.Empty(t, parents.calls, "no parent is fetched past the cap")
	assert.Empty(t, e.bc.seen)
}

// ctxParents records what the handler gave Parent to work within.
type ctxParents struct {
	hasDeadline bool
	err         error
}

func (c *ctxParents) Parent(ctx context.Context, _ string) (*transaction.Transaction, error) {
	_, c.hasDeadline = ctx.Deadline()
	return nil, c.err
}

// rawWithInputs is the payment's child with extra inputs, each from a parent
// txid of its own (distinct) or all from one (not distinct).
func rawWithInputs(t *testing.T, pay payment, extra int, distinct bool) string {
	t.Helper()
	child, err := transaction.NewTransactionFromBytes(pay.child.Bytes())
	require.NoError(t, err)
	for i := 0; i < extra; i++ {
		var h chainhash.Hash
		h[0] = 0xee
		if distinct {
			h[1], h[2] = byte(i), byte(i>>8)
		}
		child.Inputs = append(child.Inputs, &transaction.TransactionInput{
			SourceTXID: &h, SourceTxOutIndex: uint32(i), SequenceNumber: 0xffffffff, UnlockingScript: &script.Script{},
		})
	}
	return child.Hex()
}

// The cap on distinct parents is what bounds the outside requests one
// unauthenticated call can cause before any signature is checked.
func TestP2PRawParentCapIsSmall(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	parents := newFakeParents(pay.parent)
	e.srv.p2p.Parents = parents

	// One real parent plus 10 more is 11 distinct: over the cap of 10.
	w := e.post(rawPath, rawBody(t, rawWithInputs(t, pay, 10, true), d.ref, nil))
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_TOO_MANY_INPUTS")
	assert.Empty(t, parents.calls)
}

// Many inputs from one parent cost one fetch but still make a large body to
// parse and verify, so the input count is capped too.
func TestP2PRawInputCap(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	parents := newFakeParents(pay.parent)
	e.srv.p2p.Parents = parents

	w := e.post(rawPath, rawBody(t, rawWithInputs(t, pay, 100, false), d.ref, nil))
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_TOO_MANY_INPUTS")
	assert.Empty(t, parents.calls)
}

func TestP2PRawFetchHasDeadline(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	cp := &ctxParents{err: errors.New("woc down")}
	e.srv.p2p.Parents = cp
	e.post(rawPath, rawBody(t, rawHex(pay.child), d.ref, nil))
	assert.True(t, cp.hasDeadline, "an unauthenticated call must not hold a fetch open without a time limit")
}

// A transaction whose ancestry outgrows the budget is the sender's problem, not
// ours to retry.
func TestP2PRawBudgetExceededIsClientError(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	e.srv.p2p.Parents = &ctxParents{err: p2p.ErrBudgetExceeded}
	w := e.post(rawPath, rawBody(t, rawHex(pay.child), d.ref, nil))
	assert.Equal(t, 400, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "ERR_TOO_MANY_INPUTS")
}
