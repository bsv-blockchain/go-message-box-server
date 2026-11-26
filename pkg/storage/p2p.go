package storage

import (
	"context"
	"errors"
	"time"
)

// Errors returned by P2PDestinationStore.
var (
	// ErrDestinationNotFound is a reference that was never issued, was purged,
	// or is past AcceptUntil with no payment on it.
	ErrDestinationNotFound = errors.New("p2p destination not found")
	// ErrDestinationUsed is a second, different transaction for one reference
	// from MarkDestinationUsed. The caller does not refuse the payment for it.
	ErrDestinationUsed = errors.New("p2p destination already used by another transaction")
)

// P2PDestinationRetention is how long a destination record outlives each
// accepted payment. A sender that lost the response may retry long after the
// accept window, and the retry has to find the record. Every accepted txid gets
// the full window, not only the first (see ExtendDestinationRetention).
const P2PDestinationRetention = 30 * 24 * time.Hour

// P2PUnpaidRetention is how long an unpaid destination is kept, counted from
// issue and not from the end of its accept window. A sender may broadcast a
// payment itself after an error from us; the record is then the only copy of
// the derivation prefix and suffix, and without it the recipient cannot spend
// the output. Once a payment is accepted, PurgeAt moves out to P2PDestinationRetention.
const P2PUnpaidRetention = 7 * 24 * time.Hour

// P2PDestination is one payment destination handed to a paymail sender. It is
// issued for one payment: the first transaction that lands on it holds the
// mark, and a later one that pays the same output is still delivered.
type P2PDestination struct {
	Reference        string // 32 lowercase hex, random
	Handle           string
	IdentityKey      string // recipient, 66 hex; fixed at issue so a released handle cannot redirect a payment
	Script           string // locking script hex
	Satoshis         uint64
	DerivationPrefix string
	DerivationSuffix string
	CreatedAt        time.Time
	AcceptUntil      time.Time // no new payment after this
	PurgeAt          time.Time // TTL index; CreatedAt+P2PUnpaidRetention while unpaid, then latest accepted payment+30d
	UsedTxID         string    // "" until a payment lands
	// DeliveredTxIDs are the transactions whose inbox message was stored. It is
	// the record of delivery: the message itself is deleted when the wallet
	// acknowledges it, so its absence proves nothing.
	DeliveredTxIDs []string
}

// P2PDestinationStore keeps the destinations. Mongo only.
type P2PDestinationStore interface {
	CreateDestination(ctx context.Context, d P2PDestination) error
	// GetDestination returns ErrDestinationNotFound for a missing record, and
	// for an unused one when now is past AcceptUntil. A used record is returned
	// whatever the time, so a replay after the window still succeeds.
	GetDestination(ctx context.Context, reference string, now time.Time) (*P2PDestination, error)
	// MarkDestinationUsed sets UsedTxID and PurgeAt = now+30d atomically when
	// UsedTxID is empty. The same txid again is nil; another txid is
	// ErrDestinationUsed and leaves PurgeAt alone.
	MarkDestinationUsed(ctx context.Context, reference, txid string, now time.Time) error
	// ExtendDestinationRetention sets PurgeAt = max(PurgeAt, until). It never
	// shortens the record's life. The handler calls it before it broadcasts any
	// transaction, so every accepted txid, not only the one that holds the mark,
	// has a full recovery window if we crash before delivery.
	// ErrDestinationNotFound for an unknown reference.
	ExtendDestinationRetention(ctx context.Context, reference string, until time.Time) error
	// MarkDestinationDelivered records that txid's inbox message was stored. It
	// is idempotent and ErrDestinationNotFound for an unknown reference.
	MarkDestinationDelivered(ctx context.Context, reference, txid string) error
}
