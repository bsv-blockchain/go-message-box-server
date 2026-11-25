package p2p

import (
	"context"
	"errors"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/stretchr/testify/require"
)

type fakeTracker struct {
	valid bool
	err   error
}

func (f fakeTracker) IsValidRootForHeight(context.Context, *chainhash.Hash, uint32) (bool, error) {
	return f.valid, f.err
}
func (f fakeTracker) CurrentHeight(context.Context) (uint32, error) { return 900000, f.err }

func TestSPVAcceptsMinedTransaction(t *testing.T) {
	require.NoError(t, NewSPV(fakeTracker{valid: true}).Verify(context.Background(), minedTx(t)))
}

func TestSPVForgedMerklePath(t *testing.T) {
	err := NewSPV(fakeTracker{valid: false}).Verify(context.Background(), minedTx(t))
	require.ErrorIs(t, err, ErrInvalidProof)
}

func TestSPVMissingParents(t *testing.T) {
	tx := minedTx(t)
	tx.MerklePath = nil // unmined, and the inputs carry no source transactions
	err := NewSPV(fakeTracker{valid: true}).Verify(context.Background(), tx)
	require.ErrorIs(t, err, ErrInvalidProof)
}

func TestSPVTrackerDownIsNotInvalidProof(t *testing.T) {
	down := errors.New("woc unreachable")
	err := NewSPV(fakeTracker{err: down}).Verify(context.Background(), minedTx(t))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrInvalidProof)
	require.ErrorIs(t, err, down)
}

// cancelTracker accepts every root, and cancels the context when asked, so
// the cancel lands after the merkle check and inside script execution.
type cancelTracker struct{ cancel context.CancelFunc }

func (c cancelTracker) IsValidRootForHeight(context.Context, *chainhash.Hash, uint32) (bool, error) {
	c.cancel()
	return true, nil
}
func (cancelTracker) CurrentHeight(context.Context) (uint32, error) { return 900000, nil }

// go-sdk wraps a context stop during script execution with both
// ErrScriptVerificationFailed and the context error. It is our budget running
// out, not the sender's fault, so it must not read as an invalid proof.
func TestSPVContextStopIsNotInvalidProof(t *testing.T) {
	parent := minedTx(t)
	child := transaction.NewTransaction()
	child.AddInputFromTx(parent, 0, nil)
	child.Inputs[0].UnlockingScript = &script.Script{}
	child.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: &script.Script{}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := NewSPV(cancelTracker{cancel: cancel}).Verify(ctx, child)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrInvalidProof)
}
