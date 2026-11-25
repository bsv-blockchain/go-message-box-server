package p2p

import (
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
)

// ErrNonStandard means an unmined transaction in the payment's ancestry spends
// an output that is not P2PKH or P2PK, or unlocks it with a script that is not
// push-only. It is final: the same bytes are refused again.
var ErrNonStandard = errors.New("non-standard spend in unmined ancestry")

// CheckStandardSpends walks tx and its unmined ancestors. Every input of each
// one must spend a P2PKH or P2PK output with a push-only unlocking script.
//
// spv.Verify runs the scripts of every unmined transaction, and after
// Chronicle one opcode on a large number (OP_MUL on 8 MB) can take seconds and
// hundreds of MB, with no cancellation point inside it. Standard templates run
// in microseconds, and legacy paymail senders such as HandCash spend only
// P2PKH, so this check runs first and costs real payments nothing.
//
// A mined transaction is not walked: spv.Verify checks its merkle path and
// does not run its scripts. An input's spent output is the parent's output or,
// for an Extended Format input with no parent, the output it carries; both are
// checked. An input with neither is left to spv.Verify, which refuses it.
func CheckStandardSpends(tx *transaction.Transaction) error {
	seen := map[*transaction.Transaction]bool{}
	queue := []*transaction.Transaction{tx}
	for len(queue) > 0 {
		t := queue[0]
		queue = queue[1:]
		if t == nil || seen[t] || t.MerklePath != nil {
			continue
		}
		seen[t] = true
		for vin, in := range t.Inputs {
			// SourceTxOutput is what spv.Verify runs the unlock against: the
			// parent's output, or for an Extended Format input with no parent,
			// the output the input carries itself.
			out := in.SourceTxOutput()
			if out == nil {
				continue // nothing to run: spv.Verify refuses a missing source
			}
			lock := out.LockingScript
			if lock == nil || (!lock.IsP2PKH() && !lock.IsP2PK()) {
				return fmt.Errorf("%w: input %d of %s spends a non-standard output", ErrNonStandard, vin, t.TxID())
			}
			if !pushOnly(in.UnlockingScript) {
				return fmt.Errorf("%w: input %d of %s has an unlocking script that is not push-only", ErrNonStandard, vin, t.TxID())
			}
			queue = append(queue, in.SourceTransaction)
		}
	}
	return nil
}

// pushOnly is true when every opcode of s pushes data. An empty script is
// push-only; spv.Verify refuses it if the lock needs a signature.
func pushOnly(s *script.Script) bool {
	if s == nil {
		return true
	}
	ops, err := s.ParseOps()
	if err != nil {
		return false
	}
	for _, op := range ops {
		if op.Op > script.Op16 {
			return false
		}
	}
	return true
}
