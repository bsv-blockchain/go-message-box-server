package p2p

import (
	"context"

	"github.com/bsv-blockchain/go-sdk/transaction"
)

// The three outside systems the paymail receive routes depend on. They are
// interfaces so handlers can be tested without Arcade or WhatsOnChain.

// Broadcaster sends a transaction to the network.
type Broadcaster interface {
	Broadcast(ctx context.Context, tx *transaction.Transaction) (txid string, err error)
}

// Verifier SPV-checks a transaction against the chain before it is broadcast.
type Verifier interface {
	Verify(ctx context.Context, tx *transaction.Transaction) error
}

// ParentFetcher loads a transaction (with its proof, when mined) by txid.
type ParentFetcher interface {
	Parent(ctx context.Context, txid string) (*transaction.Transaction, error)
}
