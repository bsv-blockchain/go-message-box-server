package p2p

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/spv"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker"
)

// ErrInvalidProof means the transaction, or an ancestor the sender supplied,
// failed SPV verification: a bad merkle path, a missing parent, a bad script.
// It is the sender's fault. Any other Verify error means the chain tracker
// could not answer, which is ours.
var ErrInvalidProof = errors.New("transaction failed SPV verification")

// spvErrors are go-sdk's verdicts about the transaction itself.
var spvErrors = []error{
	spv.ErrFeeTooLow, spv.ErrInvalidMerklePath, spv.ErrMissingSourceTransaction,
	spv.ErrScriptVerificationFailed, spv.ErrSourceTransactionMismatch,
	spv.ErrDuplicateInput, spv.ErrValueOutOfRange, spv.ErrOutputsExceedInputs,
	spv.ErrNoInputs, spv.ErrNoOutputs, spv.ErrP2SHOutput, spv.ErrUnminedCoinbase,
	spv.ErrNullOutpoint, spv.ErrNonFinalTransaction, spv.ErrPrematureCoinbaseSpend,
	spv.ErrConfiscationTransaction, spv.ErrPrematureConfiscationSpend,
}

type spvVerifier struct{ tracker chaintracker.ChainTracker }

// NewSPV returns a Verifier that runs go-sdk spv.Verify with the given chain
// tracker (production: chaintracker.NewWhatsOnChain(MainNet, key)). Arcade
// trusts the parents a sender supplies and has no wait-for option, so this
// runs before the broadcast. No fee model is passed: Arcade checks fees.
func NewSPV(tracker chaintracker.ChainTracker) Verifier { return &spvVerifier{tracker: tracker} }

func (v *spvVerifier) Verify(ctx context.Context, tx *transaction.Transaction) error {
	ok, err := spv.Verify(ctx, tx, v.tracker, nil)
	if err != nil {
		for _, e := range spvErrors {
			if errors.Is(err, e) {
				return fmt.Errorf("%w: %w", ErrInvalidProof, err)
			}
		}
		return err
	}
	if !ok {
		return ErrInvalidProof
	}
	return nil
}
