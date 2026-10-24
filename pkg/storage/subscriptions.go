package storage

import (
	"context"
	"errors"
	"time"
)

// Errors returned by SubscriptionStore.
var (
	ErrSubscriptionNotFound = errors.New("subscription not found")

	// ErrSubscriptionRaced says a bind lost to a concurrent write: the row was
	// inserted, or rebound, between the caller's read and its write. Nothing
	// was written, so the caller reads again and decides afresh.
	ErrSubscriptionRaced = errors.New("subscription bind raced a concurrent write")
)

// The stores a subscription can come from, as the wallet names them.
const (
	StoreAppStore   = "app-store"
	StoreGooglePlay = "google-play"
)

// Subscription states, normalised across both stores.
const (
	// SubActive is paid up and renewing.
	SubActive = "active"
	// SubCanceled will not renew but is paid up until ExpiresAt.
	SubCanceled = "canceled"
	// SubGrace is past ExpiresAt while the store retries a failed renewal in a
	// grace period it grants the subscriber; the subscriber keeps access.
	SubGrace = "grace"
	// SubOnHold is a failed renewal without access: billing retry, account hold
	// or a pause.
	SubOnHold = "on_hold"
	// SubExpired has ended.
	SubExpired = "expired"
	// SubRevoked was refunded or revoked; ExpiresAt is when.
	SubRevoked = "revoked"
	// SubPending awaits payment and has never granted access.
	SubPending = "pending"
	// SubWrongTier renewed into a product whose tier does not fit the handle
	// it pays for (a crossgrade). ExpiresAt stays at the end of the last period
	// that did fit.
	SubWrongTier = "wrong_tier"
)

// SubscriptionID is the registry's key for a store subscription: the store and
// the identifier the store keeps for the subscription's whole life — Apple's
// originalTransactionId or Google's purchase token.
func SubscriptionID(store, storeID string) string { return store + ":" + storeID }

// SubscriptionStatus is what a store says about a subscription right now.
type SubscriptionStatus struct {
	ProductID   string
	BasePlan    string // Google's base plan; empty for Apple
	Tier        int
	State       string
	ExpiresAt   time.Time
	Environment string // Apple: Production | Sandbox; Google: Production | Test
}

// Subscription is one store subscription and the handle it pays for. One row
// per subscription, so the primary key is what stops a receipt paying for two
// handles: a subscription is bound to a single handle at a time.
type Subscription struct {
	ID          string // SubscriptionID(Store, StoreID)
	Store       string
	StoreID     string
	Handle      string
	IdentityKey string
	SubscriptionStatus
	CreatedAt time.Time
	UpdatedAt time.Time
	// ReleasedAt is set once the handle was released because the
	// subscription lapsed, so the sweep stops coming back to it. Binding the
	// subscription again clears it.
	ReleasedAt *time.Time
}

// SubscriptionStore keeps store subscriptions. Like HandleStore it is a
// standalone contract that only the Mongo backend implements.
type SubscriptionStore interface {
	// GetSubscription returns the row, or (nil, nil).
	GetSubscription(ctx context.Context, id string) (*Subscription, error)

	// BindSubscription writes sub, binding it to sub.Handle and
	// sub.IdentityKey with sub's status. With prev nil it is an insert, and an
	// existing row is ErrSubscriptionRaced. With prev set it replaces the row
	// only while the row is still bound to prev.Handle and prev.IdentityKey,
	// and is ErrSubscriptionRaced otherwise. Either way ReleasedAt is cleared
	// and CreatedAt is kept from the first insert.
	BindSubscription(ctx context.Context, sub Subscription, prev *Subscription) error

	// UpdateSubscriptionStatus records what the store now says. Errors:
	// ErrSubscriptionNotFound.
	UpdateSubscriptionStatus(ctx context.Context, id string, st SubscriptionStatus, now time.Time) error

	// MarkSubscriptionReleased records that the lapse sweep is done with the
	// row. Errors: ErrSubscriptionNotFound.
	MarkSubscriptionReleased(ctx context.Context, id string, at time.Time) error

	// ListSubscriptionsByHandle returns every row bound to handle, in no
	// particular order. None is an empty, non-nil slice.
	ListSubscriptionsByHandle(ctx context.Context, handle string) ([]Subscription, error)

	// ListLapsedSubscriptions returns up to limit rows with ExpiresAt before
	// before and no ReleasedAt, earliest expiry first.
	ListLapsedSubscriptions(ctx context.Context, before time.Time, limit int) ([]Subscription, error)
}
