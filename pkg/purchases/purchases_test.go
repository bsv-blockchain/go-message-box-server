package purchases_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/purchases"
	"github.com/bsv-blockchain/go-message-box-server/pkg/purchases/purchasestest"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

const (
	iosLen6   = "org.bsvblockchain.wallet.handle.len6.monthly"
	playLen6  = "org.bsvblockchain.wallet.handle.len6"
	iosLen3Yr = "org.bsvblockchain.wallet.handle.len3.yearly"
)

var ctx = context.Background()

func TestProductTiers(t *testing.T) {
	apple := map[string]int{
		iosLen6: 6, iosLen3Yr: 3, "org.bsvblockchain.wallet.handle.len1.monthly": 1, "org.bsvblockchain.wallet.handle.len7.yearly": 7,
		"org.bsvblockchain.wallet.handle.len8.monthly": 0, "org.bsvblockchain.wallet.handle.len0.monthly": 0,
		"org.bsvblockchain.wallet.handle.len6": 0, "org.bsvblockchain.wallet.handle.len6.weekly": 0,
		"org.bsvblockchain.wallet.handle.len06.monthly": 0, "com.other.handle.len6.monthly": 0,
	}
	for p, want := range apple {
		if got := purchases.AppleProductTier(p); got != want {
			t.Errorf("AppleProductTier(%q) = %d, want %d", p, got, want)
		}
	}
	google := []struct {
		product, plan string
		want          int
	}{
		{playLen6, "monthly", 6}, {playLen6, "yearly", 6}, {"org.bsvblockchain.wallet.handle.len7", "monthly", 7},
		{playLen6, "weekly", 0}, {playLen6, "", 0}, {iosLen6, "monthly", 0}, {"org.bsvblockchain.wallet.handle.len9", "monthly", 0},
	}
	for _, c := range google {
		if got := purchases.GoogleProductTier(c.product, c.plan); got != c.want {
			t.Errorf("GoogleProductTier(%q, %q) = %d, want %d", c.product, c.plan, got, c.want)
		}
	}
}

func TestEntitled(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Hour)
	cases := []struct {
		state   string
		expires time.Time
		want    bool
	}{
		{storage.SubActive, later, true}, {storage.SubActive, earlier, false},
		{storage.SubCanceled, later, true}, {storage.SubCanceled, earlier, false},
		{storage.SubGrace, earlier, true},
		{storage.SubOnHold, later, false}, {storage.SubExpired, later, false}, {storage.SubRevoked, later, false},
		{storage.SubPending, later, false}, {storage.SubWrongTier, later, false},
	}
	for _, c := range cases {
		if got := purchases.Entitled(storage.SubscriptionStatus{State: c.state, ExpiresAt: c.expires}, now); got != c.want {
			t.Errorf("Entitled(%s, expires %v) = %v, want %v", c.state, c.expires, got, c.want)
		}
	}
}

func newApple(t *testing.T, envs ...string) (*purchases.Apple, *purchasestest.AppleStub) {
	t.Helper()
	stub := purchasestest.NewAppleStub(t)
	a, err := purchases.NewApple(stub.Config(envs...))
	if err != nil {
		t.Fatal(err)
	}
	return a, stub
}

func TestAppleVerify(t *testing.T) {
	a, stub := newApple(t)
	expires := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)
	renewal := purchasestest.AppleTx{TransactionID: "2000000000000002", OriginalTransactionID: "2000000000000001", ProductID: iosLen6, Expires: expires, Status: 1, AutoRenewOn: true}
	stub.Put(renewal)

	v, err := a.Verify(ctx, purchases.Purchase{Store: storage.StoreAppStore, ProductID: iosLen6, TransactionID: renewal.TransactionID, Token: renewal.SignedTransaction(t)})
	if err != nil {
		t.Fatal(err)
	}
	if v.StoreID != "2000000000000001" || v.Tier != 6 || v.State != storage.SubActive || !v.ExpiresAt.Equal(expires) || v.Environment != purchases.AppleProduction {
		t.Fatalf("verified %+v", v)
	}

	// The token alone is enough to name the transaction.
	if _, err := a.Verify(ctx, purchases.Purchase{Store: storage.StoreAppStore, Token: renewal.SignedTransaction(t)}); err != nil {
		t.Fatalf("token only: %v", err)
	}

	// Auto-renew off: still paid up, but cancelled.
	stub.Put(purchasestest.AppleTx{TransactionID: "3", ProductID: iosLen6, Expires: expires, Status: 1})
	if v, err := a.Verify(ctx, purchases.Purchase{TransactionID: "3"}); err != nil || v.State != storage.SubCanceled {
		t.Fatalf("auto-renew off = %+v, %v; want canceled", v, err)
	}
	// Refunded: revoked at the revocation date.
	revoked := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	stub.Put(purchasestest.AppleTx{TransactionID: "4", ProductID: iosLen6, Expires: expires, Revoked: revoked, Status: 5})
	if v, err := a.Verify(ctx, purchases.Purchase{TransactionID: "4"}); err != nil || v.State != storage.SubRevoked || !v.ExpiresAt.Equal(revoked) {
		t.Fatalf("revoked = %+v, %v", v, err)
	}
	for status, want := range map[int]string{2: storage.SubExpired, 3: storage.SubOnHold, 4: storage.SubGrace} {
		stub.Put(purchasestest.AppleTx{TransactionID: "5", ProductID: iosLen6, Expires: expires, Status: status})
		if v, err := a.Verify(ctx, purchases.Purchase{TransactionID: "5"}); err != nil || v.State != want {
			t.Fatalf("status %d = %+v, %v; want %s", status, v, err, want)
		}
	}
}

func TestAppleVerifyRejects(t *testing.T) {
	a, stub := newApple(t)
	expires := time.Now().Add(time.Hour)
	tx := purchasestest.AppleTx{TransactionID: "10", ProductID: iosLen6, Expires: expires, Status: 1, AutoRenewOn: true}
	stub.Put(tx)
	stub.Put(purchasestest.AppleTx{TransactionID: "11", ProductID: iosLen6, Expires: expires, Status: 1, BundleID: "com.someone.else"})
	stub.Put(purchasestest.AppleTx{TransactionID: "12", ProductID: "org.bsvblockchain.wallet.coffee", Expires: expires, Status: 1})
	stub.Put(purchasestest.AppleTx{TransactionID: "13", ProductID: iosLen6, Expires: expires, Status: 1, Environment: purchases.AppleSandbox})

	cases := map[string]purchases.Purchase{
		"unknown transaction":      {TransactionID: "999"},
		"no transaction":           {},
		"product mismatch":         {TransactionID: "10", ProductID: iosLen3Yr},
		"token for another tx":     {TransactionID: "999", Token: tx.SignedTransaction(t)},
		"token not a JWS":          {TransactionID: "10", Token: "garbage"},
		"another app's purchase":   {TransactionID: "11"},
		"not a handle product":     {TransactionID: "12"},
		"sandbox, production only": {TransactionID: "13"},
	}
	for name, p := range cases {
		if _, err := a.Verify(ctx, p); !errors.Is(err, purchases.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}

	stub.Fail = http.StatusInternalServerError
	if _, err := a.Verify(ctx, purchases.Purchase{TransactionID: "10"}); !errors.Is(err, purchases.ErrUnavailable) {
		t.Errorf("store down: err = %v, want ErrUnavailable", err)
	}
	stub.Fail = http.StatusUnauthorized
	if _, err := a.Verify(ctx, purchases.Purchase{TransactionID: "10"}); !errors.Is(err, purchases.ErrUnavailable) {
		t.Errorf("bad key: err = %v, want ErrUnavailable", err)
	}
}

func TestAppleSandboxFallback(t *testing.T) {
	a, stub := newApple(t, purchases.AppleProduction, purchases.AppleSandbox)
	stub.Put(purchasestest.AppleTx{TransactionID: "20", ProductID: iosLen6, Expires: time.Now().Add(time.Hour), Status: 1, Environment: purchases.AppleSandbox})
	v, err := a.Verify(ctx, purchases.Purchase{TransactionID: "20"})
	if err != nil || v.Environment != purchases.AppleSandbox {
		t.Fatalf("sandbox = %+v, %v", v, err)
	}
	// A refresh goes straight to the environment on file.
	stub.Put(purchasestest.AppleTx{TransactionID: "20", ProductID: iosLen6, Expires: time.Now().Add(time.Hour), Status: 2, Environment: purchases.AppleSandbox})
	r, err := a.Refresh(ctx, storage.Subscription{StoreID: "20", SubscriptionStatus: storage.SubscriptionStatus{Environment: purchases.AppleSandbox}})
	if err != nil || r.State != storage.SubExpired {
		t.Fatalf("refresh = %+v, %v", r, err)
	}
}

func TestNewAppleRejectsBadConfig(t *testing.T) {
	stub := purchasestest.NewAppleStub(t)
	good := stub.Config()
	for name, mut := range map[string]func(*purchases.AppleConfig){
		"no issuer":   func(c *purchases.AppleConfig) { c.IssuerID = "" },
		"no bundle":   func(c *purchases.AppleConfig) { c.BundleID = "" },
		"bad key":     func(c *purchases.AppleConfig) { c.PrivateKey = []byte("not pem") },
		"unknown env": func(c *purchases.AppleConfig) { c.Environments = []string{"Staging"} },
	} {
		c := good
		mut(&c)
		if _, err := purchases.NewApple(c); err == nil {
			t.Errorf("%s: NewApple succeeded", name)
		}
	}
}

func TestOriginalTransactionID(t *testing.T) {
	tx := purchasestest.AppleTx{TransactionID: "31", OriginalTransactionID: "30", ProductID: iosLen6}
	var n struct {
		SignedPayload string `json:"signedPayload"`
	}
	body := tx.Notification(t, "DID_RENEW")
	if err := jsonUnmarshal(body, &n); err != nil {
		t.Fatal(err)
	}
	if id, err := purchases.OriginalTransactionID(n.SignedPayload); err != nil || id != "30" {
		t.Fatalf("OriginalTransactionID = %q, %v; want 30", id, err)
	}
	if id, err := purchases.OriginalTransactionID(purchasestest.SignJWS(t, map[string]any{"notificationType": "TEST", "data": map[string]any{}})); err != nil || id != "" {
		t.Fatalf("TEST notification = %q, %v; want empty", id, err)
	}
	if _, err := purchases.OriginalTransactionID("nope"); err == nil {
		t.Fatal("garbage payload accepted")
	}
}

func newGoogle(t *testing.T, allowTest bool) (*purchases.Google, *purchasestest.GoogleStub) {
	t.Helper()
	stub := purchasestest.NewGoogleStub(t)
	g, err := purchases.NewGoogle(ctx, stub.Config(allowTest))
	if err != nil {
		t.Fatal(err)
	}
	return g, stub
}

func TestGoogleVerify(t *testing.T) {
	g, stub := newGoogle(t, false)
	expires := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)
	stub.Put(purchasestest.GoogleSub{Token: "tok-1", ProductID: playLen6, BasePlan: "yearly", State: "SUBSCRIPTION_STATE_ACTIVE", Expires: expires, Linked: "tok-0"})

	v, err := g.Verify(ctx, purchases.Purchase{Store: storage.StoreGooglePlay, ProductID: playLen6, TransactionID: "GPA.3345-1234-5678-90123", Token: "tok-1"})
	if err != nil {
		t.Fatal(err)
	}
	if v.StoreID != "tok-1" || v.LinkedStoreID != "tok-0" || v.Tier != 6 || v.BasePlan != "yearly" || v.State != storage.SubActive ||
		!v.ExpiresAt.Equal(expires) || v.Environment != purchases.GoogleProduction {
		t.Fatalf("verified %+v", v)
	}
	states := map[string]string{
		"SUBSCRIPTION_STATE_CANCELED": storage.SubCanceled, "SUBSCRIPTION_STATE_IN_GRACE_PERIOD": storage.SubGrace,
		"SUBSCRIPTION_STATE_ON_HOLD": storage.SubOnHold, "SUBSCRIPTION_STATE_PAUSED": storage.SubOnHold,
		"SUBSCRIPTION_STATE_EXPIRED": storage.SubExpired, "SUBSCRIPTION_STATE_PENDING": storage.SubPending,
	}
	for gs, want := range states {
		stub.Put(purchasestest.GoogleSub{Token: "tok-2", ProductID: playLen6, BasePlan: "monthly", State: gs, Expires: expires})
		if v, err := g.Refresh(ctx, storage.Subscription{StoreID: "tok-2"}); err != nil || v.State != want {
			t.Errorf("%s = %+v, %v; want %s", gs, v, err, want)
		}
	}
}

func TestGoogleVerifyRejects(t *testing.T) {
	g, stub := newGoogle(t, false)
	expires := time.Now().Add(time.Hour)
	stub.Put(purchasestest.GoogleSub{Token: "ok", ProductID: playLen6, BasePlan: "monthly", State: "SUBSCRIPTION_STATE_ACTIVE", Expires: expires})
	stub.Put(purchasestest.GoogleSub{Token: "test", ProductID: playLen6, BasePlan: "monthly", State: "SUBSCRIPTION_STATE_ACTIVE", Expires: expires, TestPurchase: true})
	stub.Put(purchasestest.GoogleSub{Token: "plan", ProductID: playLen6, BasePlan: "weekly", State: "SUBSCRIPTION_STATE_ACTIVE", Expires: expires})
	stub.Put(purchasestest.GoogleSub{Token: "coffee", ProductID: "org.bsvblockchain.wallet.coffee", BasePlan: "monthly", State: "SUBSCRIPTION_STATE_ACTIVE", Expires: expires})
	cases := map[string]purchases.Purchase{
		"unknown token":        {Token: "nope"},
		"no token":             {},
		"product mismatch":     {Token: "ok", ProductID: "org.bsvblockchain.wallet.handle.len3"},
		"test purchase":        {Token: "test"},
		"unknown base plan":    {Token: "plan"},
		"not a handle product": {Token: "coffee"},
	}
	for name, p := range cases {
		if _, err := g.Verify(ctx, p); !errors.Is(err, purchases.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	stub.Fail = http.StatusServiceUnavailable
	if _, err := g.Verify(ctx, purchases.Purchase{Token: "ok"}); !errors.Is(err, purchases.ErrUnavailable) {
		t.Errorf("store down: err = %v, want ErrUnavailable", err)
	}

	tg, tstub := newGoogle(t, true)
	tstub.Put(purchasestest.GoogleSub{Token: "test", ProductID: playLen6, BasePlan: "monthly", State: "SUBSCRIPTION_STATE_ACTIVE", Expires: expires, TestPurchase: true})
	if v, err := tg.Verify(ctx, purchases.Purchase{Token: "test"}); err != nil || v.Environment != purchases.GoogleTest {
		t.Errorf("allowed test purchase = %+v, %v", v, err)
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
