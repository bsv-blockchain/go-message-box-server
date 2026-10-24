// Package purchases checks App Store and Google Play subscriptions with the
// stores themselves. Nothing a client sends is taken as proof: the client names
// a purchase, and the store's own server API says whether it exists, what it is
// for and until when it is paid.
package purchases

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

var (
	// ErrInvalid says the store does not know the purchase, or that it is not a
	// handle subscription of this app. The client should not retry it.
	ErrInvalid = errors.New("invalid purchase")
	// ErrUnavailable says the store could not be asked: a network failure, a
	// rate limit, a store outage or bad server credentials. Safe to retry.
	ErrUnavailable = errors.New("store unavailable")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func unavailable(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUnavailable, fmt.Sprintf(format, a...))
}

// Purchase is what the wallet sends alongside a handle: the store, the product
// it bought and the store's handle on the purchase. For the App Store, Token is
// the StoreKit 2 signed transaction and TransactionID its id; for Google Play,
// Token is the purchase token.
type Purchase struct {
	Store         string `json:"store"`
	ProductID     string `json:"productId"`
	TransactionID string `json:"transactionId"`
	Token         string `json:"token"`
}

// Verified is a purchase the store vouched for.
type Verified struct {
	storage.SubscriptionStatus

	// StoreID identifies the subscription for its whole life: Apple's
	// originalTransactionId, or Google's purchase token.
	StoreID string
	// LinkedStoreID is the Google purchase token this one replaced (an
	// upgrade, downgrade or resubscription), if any.
	LinkedStoreID string
}

// Verifier is one store.
type Verifier interface {
	// Verify asks the store about a purchase the wallet presents.
	Verify(ctx context.Context, p Purchase) (*Verified, error)
	// Refresh asks the store about a subscription already on file.
	Refresh(ctx context.Context, sub storage.Subscription) (*Verified, error)
}

// ProductPrefix starts every handle subscription's product id. The tier
// follows it: org.bsvblockchain.wallet.handle.len<N>[.monthly|.yearly].
const ProductPrefix = "org.bsvblockchain.wallet.handle.len"

// Billing periods, as the iOS product suffix and the Google base plan id.
const (
	Monthly = "monthly"
	Yearly  = "yearly"
)

// parseTier reads N out of "len<N>" product ids; 0 means not a handle product.
func parseTier(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 7 || strconv.Itoa(n) != s {
		return 0
	}
	return n
}

// AppleProductTier parses an App Store product id,
// org.bsvblockchain.wallet.handle.len<N>.monthly|yearly. 0 means it is not one.
func AppleProductTier(productID string) int {
	rest, ok := strings.CutPrefix(productID, ProductPrefix)
	if !ok {
		return 0
	}
	n, period, ok := strings.Cut(rest, ".")
	if !ok || (period != Monthly && period != Yearly) {
		return 0
	}
	return parseTier(n)
}

// GoogleProductTier parses a Play product id, org.bsvblockchain.wallet.handle.len<N>,
// with its base plan, monthly or yearly. 0 means it is not one.
func GoogleProductTier(productID, basePlan string) int {
	rest, ok := strings.CutPrefix(productID, ProductPrefix)
	if !ok || (basePlan != Monthly && basePlan != Yearly) {
		return 0
	}
	return parseTier(rest)
}

// Entitled reports whether a subscription in st pays for its handle at now. A
// subscription that will not renew is still paid up until it expires, and one
// in the store's billing grace period keeps access while the store retries.
func Entitled(st storage.SubscriptionStatus, now time.Time) bool {
	switch st.State {
	case storage.SubActive, storage.SubCanceled:
		return st.ExpiresAt.After(now)
	case storage.SubGrace:
		return true
	}
	return false
}
