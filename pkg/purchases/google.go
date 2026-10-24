package purchases

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// The environments a Play subscription can be in: a real purchase, or one a
// license tester made, which Google never charges for.
const (
	GoogleProduction = "Production"
	GoogleTest       = "Test"
)

// GoogleConfig holds a Play Developer API service account.
type GoogleConfig struct {
	PackageName string
	// CredentialsJSON is the service account key. The account needs the
	// "View financial data" and "Manage orders and subscriptions" permissions
	// on the app in Play Console.
	CredentialsJSON []byte
	// AllowTest accepts license testers' purchases, which cost nothing.
	AllowTest bool
	// Options replace the credentials (tests point the client at a stub).
	Options []option.ClientOption
}

// Google verifies Play subscriptions with purchases.subscriptionsv2.get.
type Google struct {
	cfg GoogleConfig
	svc *androidpublisher.Service
}

// NewGoogle builds the Play Developer API client.
func NewGoogle(ctx context.Context, cfg GoogleConfig) (*Google, error) {
	if cfg.PackageName == "" {
		return nil, errors.New("google: package name is required")
	}
	opts := cfg.Options
	if opts == nil {
		if len(cfg.CredentialsJSON) == 0 {
			return nil, errors.New("google: service account JSON is required")
		}
		opts = []option.ClientOption{
			option.WithAuthCredentialsJSON(option.ServiceAccount, cfg.CredentialsJSON),
			option.WithScopes(androidpublisher.AndroidpublisherScope),
		}
	}
	svc, err := androidpublisher.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	return &Google{cfg: cfg, svc: svc}, nil
}

// PackageName is the app this verifier answers for.
func (g *Google) PackageName() string { return g.cfg.PackageName }

var googleStates = map[string]string{
	"SUBSCRIPTION_STATE_ACTIVE":                    storage.SubActive,
	"SUBSCRIPTION_STATE_CANCELED":                  storage.SubCanceled,
	"SUBSCRIPTION_STATE_IN_GRACE_PERIOD":           storage.SubGrace,
	"SUBSCRIPTION_STATE_ON_HOLD":                   storage.SubOnHold,
	"SUBSCRIPTION_STATE_PAUSED":                    storage.SubOnHold,
	"SUBSCRIPTION_STATE_EXPIRED":                   storage.SubExpired,
	"SUBSCRIPTION_STATE_PENDING":                   storage.SubPending,
	"SUBSCRIPTION_STATE_PENDING_PURCHASE_CANCELED": storage.SubPending,
}

// Lookup asks Play about a purchase token.
func (g *Google) Lookup(ctx context.Context, token string) (*Verified, error) {
	if token == "" {
		return nil, invalid("google: purchase token is required")
	}
	sp, err := g.svc.Purchases.Subscriptionsv2.Get(g.cfg.PackageName, token).Context(ctx).Do()
	if err != nil {
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case http.StatusBadRequest, http.StatusNotFound, http.StatusGone:
				return nil, invalid("google: %s", apiErr.Message)
			}
		}
		return nil, unavailable("google: %v", err)
	}
	var item *androidpublisher.SubscriptionPurchaseLineItem
	for _, li := range sp.LineItems {
		if li != nil && strings.HasPrefix(li.ProductId, ProductPrefix) {
			item = li
			break
		}
	}
	if item == nil {
		return nil, invalid("google: purchase is not a handle subscription")
	}
	basePlan := ""
	if item.OfferDetails != nil {
		basePlan = item.OfferDetails.BasePlanId
	}
	tier := GoogleProductTier(item.ProductId, basePlan)
	if tier == 0 {
		return nil, invalid("google: %s/%s is not a handle subscription", item.ProductId, basePlan)
	}
	state, ok := googleStates[sp.SubscriptionState]
	if !ok {
		state = storage.SubPending
	}
	var expires time.Time
	if item.ExpiryTime != "" {
		if expires, err = time.Parse(time.RFC3339Nano, item.ExpiryTime); err != nil {
			return nil, unavailable("google: expiryTime %q: %v", item.ExpiryTime, err)
		}
	}
	env := GoogleProduction
	if sp.TestPurchase != nil {
		env = GoogleTest
	}
	return &Verified{
		StoreID: token, LinkedStoreID: sp.LinkedPurchaseToken,
		SubscriptionStatus: storage.SubscriptionStatus{
			ProductID: item.ProductId, BasePlan: basePlan, Tier: tier, State: state,
			ExpiresAt: expires.UTC(), Environment: env,
		},
	}, nil
}

// Verify implements Verifier.
func (g *Google) Verify(ctx context.Context, p Purchase) (*Verified, error) {
	v, err := g.Lookup(ctx, p.Token)
	if err != nil {
		return nil, err
	}
	if p.ProductID != "" && v.ProductID != p.ProductID {
		return nil, invalid("google: purchase is for %s, not %s", v.ProductID, p.ProductID)
	}
	if v.Environment == GoogleTest && !g.cfg.AllowTest {
		return nil, invalid("google: test purchases are not accepted")
	}
	return v, nil
}

// Refresh implements Verifier.
func (g *Google) Refresh(ctx context.Context, sub storage.Subscription) (*Verified, error) {
	return g.Lookup(ctx, sub.StoreID)
}

var _ Verifier = (*Google)(nil)
