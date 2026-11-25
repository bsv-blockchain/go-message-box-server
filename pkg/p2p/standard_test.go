package p2p

import (
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expensiveLock is the review's memory probe: an 8 MB number squared.
func expensiveLock(t *testing.T) *script.Script {
	t.Helper()
	s := &script.Script{}
	require.NoError(t, s.AppendPushData([]byte{1}))
	require.NoError(t, s.AppendPushData([]byte{0x00, 0x12, 0x7a})) // 8,000,000 little-endian
	require.NoError(t, s.AppendOpcodes(script.OpNUM2BIN, script.OpINVERT))
	for range 4 {
		require.NoError(t, s.AppendOpcodes(script.OpDUP, script.OpDUP, script.OpMUL, script.OpDROP))
	}
	return s
}

func p2pkhKey(t *testing.T) (*ec.PrivateKey, *script.Script) {
	t.Helper()
	key, err := ec.NewPrivateKey()
	require.NoError(t, err)
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), true)
	require.NoError(t, err)
	lock, err := p2pkh.Lock(addr)
	require.NoError(t, err)
	return key, lock
}

// spend builds a child that spends output 0 of parent with unlock.
func spend(parent *transaction.Transaction, unlock *script.Script) *transaction.Transaction {
	child := transaction.NewTransaction()
	child.AddInput(&transaction.TransactionInput{
		SourceTXID: parent.TxID(), SourceTxOutIndex: 0, SourceTransaction: parent, UnlockingScript: unlock,
	})
	child.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: parent.Outputs[0].LockingScript})
	return child
}

func pushes(t *testing.T, items ...[]byte) *script.Script {
	t.Helper()
	s := &script.Script{}
	for _, it := range items {
		require.NoError(t, s.AppendPushData(it))
	}
	return s
}

func TestStandardSpendsP2PKH(t *testing.T) {
	_, lock := p2pkhKey(t)
	parent := transaction.NewTransaction()
	parent.AddOutput(&transaction.TransactionOutput{Satoshis: 10, LockingScript: lock})
	child := spend(parent, pushes(t, make([]byte, 71), make([]byte, 33)))
	// The unmined parent has no inputs, so only the child's input is checked.
	require.NoError(t, CheckStandardSpends(child))
}

func TestStandardSpendsP2PK(t *testing.T) {
	key, _ := p2pkhKey(t)
	lock := &script.Script{}
	require.NoError(t, lock.AppendPushData(key.PubKey().Compressed()))
	require.NoError(t, lock.AppendOpcodes(script.OpCHECKSIG))
	parent := transaction.NewTransaction()
	parent.AddOutput(&transaction.TransactionOutput{Satoshis: 10, LockingScript: lock})
	require.NoError(t, CheckStandardSpends(spend(parent, pushes(t, make([]byte, 71)))))
}

func TestStandardSpendsRefusesExpensiveScript(t *testing.T) {
	_, lock := p2pkhKey(t)
	grand := transaction.NewTransaction()
	grand.AddOutput(&transaction.TransactionOutput{Satoshis: 10, LockingScript: lock})
	grand.MerklePath = &transaction.MerklePath{} // mined: its own inputs are not run
	parent := spend(grand, pushes(t, make([]byte, 71), make([]byte, 33)))
	parent.Outputs[0].LockingScript = expensiveLock(t)
	child := spend(parent, &script.Script{})
	assert.ErrorIs(t, CheckStandardSpends(child), ErrNonStandard)
}

func TestStandardSpendsChecksDeepUnminedAncestors(t *testing.T) {
	_, lock := p2pkhKey(t)
	a := transaction.NewTransaction()
	a.AddOutput(&transaction.TransactionOutput{Satoshis: 10, LockingScript: expensiveLock(t)})
	b := spend(a, &script.Script{}) // unmined b spends a non-standard output
	b.Outputs[0].LockingScript = lock
	c := spend(b, pushes(t, make([]byte, 71), make([]byte, 33)))
	assert.ErrorIs(t, CheckStandardSpends(c), ErrNonStandard)
}

func TestStandardSpendsSkipsMinedTransactions(t *testing.T) {
	_, lock := p2pkhKey(t)
	a := transaction.NewTransaction()
	a.AddOutput(&transaction.TransactionOutput{Satoshis: 10, LockingScript: expensiveLock(t)})
	b := spend(a, &script.Script{})
	b.Outputs[0].LockingScript = lock
	b.MerklePath = &transaction.MerklePath{} // mined: its scripts were run by the network
	require.NoError(t, CheckStandardSpends(spend(b, pushes(t, make([]byte, 71), make([]byte, 33)))))
}

func TestStandardSpendsRefusesNonPushUnlock(t *testing.T) {
	_, lock := p2pkhKey(t)
	parent := transaction.NewTransaction()
	parent.AddOutput(&transaction.TransactionOutput{Satoshis: 10, LockingScript: lock})
	unlock := pushes(t, make([]byte, 71))
	require.NoError(t, unlock.AppendOpcodes(script.OpDUP, script.OpMUL))
	assert.ErrorIs(t, CheckStandardSpends(spend(parent, unlock)), ErrNonStandard)
}

// A missing source is not ours to judge here: spv.Verify refuses it as an
// invalid proof.
func TestStandardSpendsLeavesMissingSourceToSPV(t *testing.T) {
	child := transaction.NewTransaction()
	child.AddInput(&transaction.TransactionInput{SourceTxOutIndex: 0, UnlockingScript: &script.Script{}})
	require.NoError(t, CheckStandardSpends(child))
}
