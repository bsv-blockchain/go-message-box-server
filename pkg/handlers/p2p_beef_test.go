package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bsv-blockchain/go-message-box-server/pkg/p2p"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

type fakeBroadcaster struct {
	mu    sync.Mutex
	seen  []string
	err   error
	calls int
}

func (f *fakeBroadcaster) Broadcast(_ context.Context, tx *transaction.Transaction) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	f.seen = append(f.seen, tx.TxID().String())
	return tx.TxID().String(), nil
}

type fakeVerifier struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (f *fakeVerifier) Verify(context.Context, *transaction.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

type pushRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (p *pushRecorder) record(_ context.Context, recipient, messageID, _ string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, recipient+"/"+messageID)
}

func (p *pushRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.msgs)
}

// payment is a sender's transaction chain paying one destination: a mined-looking
// parent (fake merkle path, as the verifier is a fake) and a child paying it.
type payment struct {
	parent, child *transaction.Transaction
}

func fakeMerklePath(t *testing.T, tx *transaction.Transaction) *transaction.MerklePath {
	t.Helper()
	id := tx.TxID()
	yes := true
	var sibling chainhash.Hash
	sibling[0] = 7
	return transaction.NewMerklePath(850000, [][]*transaction.PathElement{{
		{Offset: 0, Hash: id, Txid: &yes},
		{Offset: 1, Hash: &sibling},
	}})
}

func newPayment(t *testing.T, scriptHex string, sats uint64) payment {
	t.Helper()
	key, err := ec.NewPrivateKey()
	require.NoError(t, err)
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), true)
	require.NoError(t, err)
	lock, err := p2pkh.Lock(addr)
	require.NoError(t, err)

	parent := transaction.NewTransaction()
	parent.AddOutput(&transaction.TransactionOutput{Satoshis: sats + 5000, LockingScript: lock})
	parent.MerklePath = fakeMerklePath(t, parent)

	unlock, err := p2pkh.Unlock(key, nil)
	require.NoError(t, err)
	child := transaction.NewTransaction()
	child.AddInputFromTx(parent, 0, unlock)
	dest, err := script.NewFromHex(scriptHex)
	require.NoError(t, err)
	child.AddOutput(&transaction.TransactionOutput{Satoshis: sats, LockingScript: dest})
	child.AddOutput(&transaction.TransactionOutput{Satoshis: 4000, LockingScript: lock})
	require.NoError(t, child.Sign())
	return payment{parent: parent, child: child}
}

func (p payment) v1(t *testing.T) string {
	t.Helper()
	b, err := p.child.BEEF()
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func (p payment) v2(t *testing.T) string {
	t.Helper()
	b, err := transaction.NewBeefFromBytes(mustHex(t, p.v1(t)))
	require.NoError(t, err)
	b.Version = transaction.BEEF_V2
	out, err := b.Bytes()
	require.NoError(t, err)
	return hex.EncodeToString(out)
}

func (p payment) atomic(t *testing.T) string {
	t.Helper()
	b, err := p.child.AtomicBEEF(false)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

// withGrandchild returns a V1 BEEF whose last transaction is not the payment:
// it spends the payment's change.
func (p payment) withGrandchild(t *testing.T) string {
	t.Helper()
	// The change output is locked to the parent's key, which p.child's creator
	// owns; the grandchild is unsigned because the verifier is a fake.
	gc := transaction.NewTransaction()
	gc.AddInput(&transaction.TransactionInput{
		SourceTXID: p.child.TxID(), SourceTxOutIndex: 1, SourceTransaction: p.child,
		UnlockingScript: &script.Script{}, SequenceNumber: transaction.DefaultSequenceNumber,
	})
	gc.AddOutput(&transaction.TransactionOutput{Satoshis: 3000, LockingScript: p.child.Outputs[1].LockingScript})
	b, err := gc.BEEF()
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

const beefPath = "/api/bsvalias/beef/rosa@example.com"

type issued struct {
	ref    string
	script string
	sats   uint64
}

// issue asks for a destination the way a paymail sender does.
func (e *p2pEnv) issue(handle string, sats uint64) issued {
	e.t.Helper()
	w := e.post("/api/bsvalias/p2p-payment-destination/"+handle+"@example.com", `{"satoshis":`+itoa(sats)+`}`)
	require.Equal(e.t, 200, w.Code, w.Body.String())
	var r destResponse
	require.NoError(e.t, json.Unmarshal(w.Body.Bytes(), &r))
	return issued{ref: r.Reference, script: r.Outputs[0].Script, sats: sats}
}

func itoa(n uint64) string { b, _ := json.Marshal(n); return string(b) }

func beefBody(beef, ref string, meta map[string]string) string {
	m := map[string]any{"beef": beef, "reference": ref}
	if meta != nil {
		m["metadata"] = meta
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (e *p2pEnv) inbox(identityKey string) []storage.Message {
	e.t.Helper()
	msgs, err := e.srv.Store.ListMessages(e.t.Context(), identityKey, "payment_inbox")
	require.NoError(e.t, err)
	return msgs
}

// decodeToken unwraps {"message":"<token json>"}.
func decodeToken(t *testing.T, body string) p2p.Token {
	t.Helper()
	var outer struct {
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &outer))
	var tok p2p.Token
	require.NoError(t, json.Unmarshal([]byte(outer.Message), &tok))
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(outer.Message), &raw))
	assert.NotContains(t, raw, "sender", "the token must never carry a sender")
	return tok
}

func tokenBytes(tok p2p.Token) []byte {
	b := make([]byte, len(tok.Transaction))
	for i, v := range tok.Transaction {
		b[i] = byte(v)
	}
	return b
}

func TestP2PBeefAcceptsEveryFormat(t *testing.T) {
	forms := map[string]func(payment, *testing.T) string{
		"v1":     payment.v1,
		"v2":     payment.v2,
		"atomic": payment.atomic,
	}
	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			e := newP2PEnv(t, true)
			idKey := e.register("rosa")
			d := e.issue("rosa", 1000)
			pay := newPayment(t, d.script, d.sats)

			w := e.post(beefPath, beefBody(form(pay, t), d.ref, nil))
			require.Equal(t, 200, w.Code, w.Body.String())
			txid := pay.child.TxID().String()
			assert.JSONEq(t, `{"txid":"`+txid+`","note":""}`, w.Body.String())
			assert.Equal(t, []string{txid}, e.bc.seen)

			msgs := e.inbox(idKey)
			require.Len(t, msgs, 1)
			assert.Equal(t, "p2p-"+txid, msgs[0].MessageID)
			assert.Equal(t, p2p.AnyonePublicKeyHex(), msgs[0].Sender)
			tok := decodeToken(t, msgs[0].Body)
			stored, err := e.srv.Store.(*fakeStore).GetDestination(t.Context(), d.ref, e.now)
			require.NoError(t, err)
			assert.Equal(t, stored.DerivationPrefix, tok.CustomInstructions.DerivationPrefix)
			assert.Equal(t, stored.DerivationSuffix, tok.CustomInstructions.DerivationSuffix)
			assert.EqualValues(t, 1000, tok.Amount)
			assert.Equal(t, 0, tok.OutputIndex)
			_, subject, err := transaction.NewBeefFromAtomicBytes(tokenBytes(tok))
			require.NoError(t, err)
			assert.Equal(t, txid, subject.String())
			assert.Equal(t, txid, stored.UsedTxID)
			assert.Equal(t, 1, e.pushes.count())
		})
	}
}

func TestP2PBeefMetadata(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	w := e.post(beefPath, beefBody(pay.v1(t), d.ref, map[string]string{"note": "hi", "sender": "x@handcash.io"}))
	require.Equal(t, 200, w.Code, w.Body.String())
	tok := decodeToken(t, e.inbox(idKey)[0].Body)
	assert.Equal(t, "hi", tok.Note)
	assert.Equal(t, "x@handcash.io", tok.Paymail)
}

func TestP2PBeefNoMatchingOutput(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	other := e.issue("rosa", 1000)
	for name, pay := range map[string]payment{
		"wrong script": newPayment(t, other.script, 1000),
		"wrong amount": newPayment(t, d.script, 999),
	} {
		w := e.post(beefPath, beefBody(pay.v1(t), d.ref, nil))
		assert.Equal(t, 400, w.Code, name)
		assert.Contains(t, w.Body.String(), "ERR_NO_MATCHING_OUTPUT", name)
	}
	assert.Empty(t, e.bc.seen)
	assert.Empty(t, e.inbox(idKey))
	stored, err := e.srv.Store.(*fakeStore).GetDestination(t.Context(), d.ref, e.now)
	require.NoError(t, err)
	assert.Empty(t, stored.UsedTxID)
}

func TestP2PBeefReference(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	e.register("bob")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)

	w := e.post(beefPath, beefBody(pay.v1(t), "00000000000000000000000000000000", nil))
	assert.Equal(t, 404, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_UNKNOWN_REFERENCE")

	w = e.post("/api/bsvalias/beef/bob@example.com", beefBody(pay.v1(t), d.ref, nil))
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_REFERENCE_MISMATCH")
	assert.Empty(t, e.bc.seen)
}

func TestP2PBeefRejected(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	e.bc.err = joinRejected("script failed")
	w := e.post(beefPath, beefBody(pay.v1(t), d.ref, nil))
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_REJECTED")
	assert.Contains(t, w.Body.String(), "script failed")
	assert.Empty(t, e.inbox(idKey))
	stored, _ := e.srv.Store.(*fakeStore).GetDestination(t.Context(), d.ref, e.now)
	assert.Empty(t, stored.UsedTxID)
}

func joinRejected(reason string) error {
	return errors.Join(p2p.ErrRejected, errors.New(reason))
}

func TestP2PBeefForgedParent(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	e.vf.err = errors.Join(p2p.ErrInvalidProof, errors.New("bad merkle path"))
	w := e.post(beefPath, beefBody(pay.v1(t), d.ref, nil))
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_INVALID_PROOF")
	assert.Zero(t, e.bc.calls)
	assert.Empty(t, e.inbox(idKey))
	assert.Zero(t, e.pushes.count())
}

func TestP2PBeefVerifierDown(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	e.vf.err = errors.New("woc down")
	w := e.post(beefPath, beefBody(pay.v1(t), d.ref, nil))
	assert.Equal(t, 503, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VERIFY_UNAVAILABLE")
	assert.Zero(t, e.bc.calls)
}

func TestP2PBeefBroadcasterDown(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	e.bc.err = errors.New("arcade 502")
	w := e.post(beefPath, beefBody(pay.v1(t), d.ref, nil))
	assert.Equal(t, 503, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_BROADCAST_UNAVAILABLE")
	assert.Empty(t, e.inbox(idKey))
	stored, _ := e.srv.Store.(*fakeStore).GetDestination(t.Context(), d.ref, e.now)
	assert.Empty(t, stored.UsedTxID)
}

func TestP2PBeefPayingTxNotLast(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	w := e.post(beefPath, beefBody(pay.withGrandchild(t), d.ref, nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, []string{pay.child.TxID().String()}, e.bc.seen)
	assert.Len(t, e.inbox(idKey), 1)
}

// Review Focus 1.
func TestP2PBeefReplay(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	body := beefBody(pay.v1(t), d.ref, nil)
	a := e.post(beefPath, body)
	b := e.post(beefPath, body)
	require.Equal(t, 200, a.Code, a.Body.String())
	require.Equal(t, 200, b.Code, b.Body.String())
	assert.Equal(t, a.Body.String(), b.Body.String())
	assert.Len(t, e.inbox(idKey), 1)
	assert.Equal(t, 1, e.pushes.count())
}

// Review Focus 2.
func TestP2PBeefSecondTransactionRefused(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	require.Equal(t, 200, e.post(beefPath, beefBody(newPayment(t, d.script, d.sats).v1(t), d.ref, nil)).Code)
	calls := e.bc.calls
	w := e.post(beefPath, beefBody(newPayment(t, d.script, d.sats).v1(t), d.ref, nil))
	assert.Equal(t, 409, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_REFERENCE_USED")
	assert.Equal(t, calls, e.bc.calls)
}

func TestP2PBeefRaceOnMarkUsed(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	// Another transaction lands between our check and our mark.
	e.srv.destinations = &racingStore{fakeStore: e.srv.Store.(*fakeStore)}
	w := e.post(beefPath, beefBody(pay.v1(t), d.ref, nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	msgs := e.inbox(idKey)
	require.Len(t, msgs, 1)
	assert.Equal(t, "p2p-"+pay.child.TxID().String(), msgs[0].MessageID)
}

// racingStore reports ErrDestinationUsed from MarkDestinationUsed.
type racingStore struct {
	*fakeStore
}

func (r *racingStore) MarkDestinationUsed(context.Context, string, string, time.Time) error {
	return storage.ErrDestinationUsed
}

// Review Focus 3.
func TestP2PBeefReleasedHandleStillPaysFirstOwner(t *testing.T) {
	e := newP2PEnv(t, true)
	idKey := e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)

	fs := e.srv.Store.(*fakeStore)
	until := e.now.Add(30 * 24 * time.Hour)
	require.NoError(t, fs.ReleaseHandle(t.Context(), storage.HandleRelease{
		Handle: "rosa", ReleasedBy: storage.ReleasedByOwner, CooldownUntil: &until, Now: e.now,
	}))
	// The handle goes to a new owner after the cooldown.
	other := e.register("rosa2")
	require.NotEqual(t, idKey, other)

	w := e.post(beefPath, beefBody(pay.v1(t), d.ref, nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Len(t, e.inbox(idKey), 1)
	assert.Empty(t, e.inbox(other))
}

func TestP2PBeefInvalid(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	for name, beef := range map[string]string{
		"not hex":    "zz",
		"empty":      "",
		"not a beef": "deadbeef",
		"no subject": emptyBeefHex(t),
	} {
		w := e.post(beefPath, beefBody(beef, d.ref, nil))
		assert.Equal(t, 400, w.Code, name)
		assert.Contains(t, w.Body.String(), "ERR_INVALID_BEEF", name)
	}
	assert.Zero(t, e.bc.calls)
}

func emptyBeefHex(t *testing.T) string {
	t.Helper()
	b, err := transaction.NewBeef().Bytes()
	require.NoError(t, err)
	return hex.EncodeToString(b)
}
