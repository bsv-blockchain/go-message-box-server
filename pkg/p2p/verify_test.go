package p2p

import (
	"context"
	"errors"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
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
