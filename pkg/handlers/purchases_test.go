package handlers

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"

	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert/certtest"
	"github.com/bsv-blockchain/go-message-box-server/pkg/purchases"
	"github.com/bsv-blockchain/go-message-box-server/pkg/purchases/purchasestest"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

const (
	iosLen6  = "org.bsvblockchain.wallet.handle.len6.monthly"
	iosLen7  = "org.bsvblockchain.wallet.handle.len7.yearly"
	playLen6 = "org.bsvblockchain.wallet.handle.len6"
	// freeHandle has the shape of a handle the app generates: able + otter + 07.
	freeHandle = "ableotter07"
	grace      = 14 * 24 * time.Hour
)

// purchaseEnv is a lookup env with paid handles on, both stores stubbed.
type purchaseEnv struct {
	*lookupEnv
	apple  *purchasestest.AppleStub
	google *purchasestest.GoogleStub
	store  *fakeStore
	auth   *fakePushAuth
}

type fakePushAuth struct{ err error }

func (a *fakePushAuth) Authenticate(*http.Request) error { return a.err }

func newPurchaseEnv(t *testing.T) *purchaseEnv {
	t.Helper()
	e := &purchaseEnv{
		apple: purchasestest.NewAppleStub(t), google: purchasestest.NewGoogleStub(t),
		auth: &fakePushAuth{},
	}
	apple, err := purchases.NewApple(e.apple.Config())
	if err != nil {
		t.Fatal(err)
	}
	google, err := purchases.NewGoogle(context.Background(), e.google.Config(false))
	if err != nil {
		t.Fatal(err)
	}
	// The two route sets are mounted side by side as main does, so the lookup
	// env is built by hand here rather than through newLookupEnv.
	e.lookupEnv = &lookupEnv{t: t, srv: setupTestServer(t), clock: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	e.store = e.srv.Store.(*fakeStore)
	e.srv.EnableLookup(LookupConfig{Domain: testDomain, Host: "https://mb.example.com", Cooldown: 30 * 24 * time.Hour}, e.store)
	e.srv.EnablePurchases(PurchaseConfig{
		Verifiers:     map[string]purchases.Verifier{storage.StoreAppStore: apple, storage.StoreGooglePlay: google},
		Grace:         grace,
		GooglePackage: purchasestest.PackageName,
		PushAuth:      e.auth,
	}, e.store)
	e.srv.now = e.now
	e.mux = http.NewServeMux()
	e.mux.Handle("/", e.srv.LookupRoutes())
	e.mux.Handle("/api/store/", e.srv.PurchaseRoutes())
	return e
}

func pubHex(k *ec.PrivateKey) string { return hex.EncodeToString(k.PubKey().Compressed()) }

// putPaid sends the certificate wrapped with a purchase, as the wallet does.
func (e *purchaseEnv) putPaid(key *ec.PrivateKey, handle string, issuedAt time.Time, p *purchases.Purchase) *httptest.ResponseRecorder {
	e.t.Helper()
	cert := certtest.Profile(e.t, key, handle, testDomain, issuedAt, nil)
	body, err := json.Marshal(PutHandleRequest{Certificate: cert, Purchase: p})
	if err != nil {
		e.t.Fatal(err)
	}
	return e.do("PUT", "/api/handle", body)
}

// appleSub puts an active subscription in the Apple stub, expiring a month
// after the env's clock, and returns the purchase the wallet would send.
func (e *purchaseEnv) appleSub(txID, product string, status int) (*purchases.Purchase, purchasestest.AppleTx) {
	tx := purchasestest.AppleTx{TransactionID: txID, OriginalTransactionID: "orig-" + txID, ProductID: product,
		Expires: e.now().Add(30 * 24 * time.Hour), Status: status, AutoRenewOn: true}
	e.apple.Put(tx)
	return &purchases.Purchase{Store: storage.StoreAppStore, ProductID: product, TransactionID: txID, Token: tx.SignedTransaction(e.t)}, tx
}

func (e *purchaseEnv) sub(id string) *storage.Subscription {
	e.t.Helper()
	s, err := e.store.GetSubscription(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *purchaseEnv) holder(handle string) string {
	e.t.Helper()
	rec, _ := e.store.GetHandle(context.Background(), handle)
	if !rec.Active() {
		return ""
	}
	return *rec.IdentityKey
}

func TestPaidHandle_RequiresPurchase(t *testing.T) {
	e := newPurchaseEnv(t)
	key := testKey(t)
	now := e.now()

	wantStatus(t, e.put(key, "deggen", now, nil), 402, "ERR_PURCHASE_REQUIRED")
	// The envelope without a purchase is no different.
	wantStatus(t, e.putPaid(key, "deggen", now, nil), 402, "ERR_PURCHASE_REQUIRED")
	if e.holder("deggen") != "" {
		t.Fatal("handle granted without a purchase")
	}
	// A generated handle is free, bare or wrapped.
	wantStatus(t, e.put(key, freeHandle, now, nil), 201, "")
	wantStatus(t, e.put(testKey(t), "absentzebra99", now, nil), 201, "")
}

func TestPaidHandle_AcceptsMatchingAppleSubscription(t *testing.T) {
	e := newPurchaseEnv(t)
	key := testKey(t)
	p, tx := e.appleSub("1001", iosLen6, 1)

	wantStatus(t, e.putPaid(key, "deggen", e.now(), p), 201, "")
	if e.holder("deggen") != pubHex(key) {
		t.Fatal("handle not granted")
	}
	s := e.sub("app-store:" + tx.OriginalTransactionID)
	if s == nil || s.Handle != "deggen" || s.IdentityKey != pubHex(key) || s.ProductID != iosLen6 || s.Tier != 6 ||
		s.State != storage.SubActive || !s.ExpiresAt.Equal(tx.Expires) || s.Environment != purchases.AppleProduction {
		t.Fatalf("subscription on file = %+v", s)
	}

	// Updating the certificate needs no purchase: the subscription on file pays.
	wantStatus(t, e.put(key, "deggen", e.advance(time.Minute), map[string]string{"name": "Deggen"}), 200, "")
	// Nor does the replayed purchase hurt.
	wantStatus(t, e.putPaid(key, "deggen", e.advance(time.Minute), p), 200, "")
}

func TestPaidHandle_AcceptsGooglePlaySubscription(t *testing.T) {
	e := newPurchaseEnv(t)
	key := testKey(t)
	e.google.Put(purchasestest.GoogleSub{Token: "g-tok", ProductID: playLen6, BasePlan: "monthly", State: "SUBSCRIPTION_STATE_ACTIVE", Expires: e.now().Add(30 * 24 * time.Hour)})
	p := &purchases.Purchase{Store: storage.StoreGooglePlay, ProductID: playLen6, TransactionID: "GPA.3345-1234-5678-90123", Token: "g-tok"}

	wantStatus(t, e.putPaid(key, "deggen", e.now(), p), 201, "")
	if s := e.sub("google-play:g-tok"); s == nil || s.Handle != "deggen" || s.BasePlan != "monthly" || s.Tier != 6 {
		t.Fatalf("subscription on file = %+v", s)
	}
}

func TestPaidHandle_Rejects(t *testing.T) {
	e := newPurchaseEnv(t)
	key := testKey(t)
	now := e.now()

	// Seven or more characters is tier 7; a tier 6 subscription does not fit.
	p6, _ := e.appleSub("2001", iosLen6, 1)
	wantStatus(t, e.putPaid(key, "deggenx", now, p6), 409, "ERR_PURCHASE_TIER_MISMATCH")
	p7, _ := e.appleSub("2002", iosLen7, 1)
	wantStatus(t, e.putPaid(key, "deg", now, p7), 409, "ERR_PURCHASE_TIER_MISMATCH")

	// The product the wallet names must be the one Apple has on file.
	lie := *p6
	lie.ProductID = iosLen7
	wantStatus(t, e.putPaid(key, "deggenx", now, &lie), 400, "ERR_PURCHASE_INVALID")

	wantStatus(t, e.putPaid(key, "deggen", now, &purchases.Purchase{Store: storage.StoreAppStore, TransactionID: "nope"}), 400, "ERR_PURCHASE_INVALID")
	wantStatus(t, e.putPaid(key, "deggen", now, &purchases.Purchase{Store: "paypal", Token: "x"}), 400, "ERR_PURCHASE_INVALID")
	wantStatus(t, e.putPaid(key, "deggen", now, &purchases.Purchase{Store: storage.StoreGooglePlay, Token: "unknown"}), 400, "ERR_PURCHASE_INVALID")

	expired, _ := e.appleSub("2003", iosLen6, 2)
	wantStatus(t, e.putPaid(key, "deggen", now, expired), 402, "ERR_PURCHASE_INACTIVE")

	e.apple.Fail = http.StatusServiceUnavailable
	wantStatus(t, e.putPaid(key, "deggen", now, p6), 502, "ERR_STORE_UNAVAILABLE")
	e.apple.Fail = 0

	if e.holder("deggen") != "" || e.holder("deggenx") != "" || e.holder("deg") != "" {
		t.Fatal("a rejected purchase granted a handle")
	}
	// The certificate inside the envelope keeps its own 16 KB cap.
	big := map[string]string{}
	for i := range 20 {
		big["f"+string(rune('a'+i))] = strings.Repeat("x", 1000)
	}
	cert := certtest.Profile(t, key, "deggen", testDomain, now, big)
	body, _ := json.Marshal(PutHandleRequest{Certificate: cert, Purchase: p6})
	wantStatus(t, e.do("PUT", "/api/handle", body), 413, "ERR_BODY_TOO_LARGE")
}

func TestPaidHandle_ReceiptCannotPayForTwoHandles(t *testing.T) {
	e := newPurchaseEnv(t)
	alice, bob := testKey(t), testKey(t)
	p, _ := e.appleSub("3001", iosLen6, 1)

	wantStatus(t, e.putPaid(alice, "deggen", e.now(), p), 201, "")
	// Another key cannot use it while alice holds the handle it pays for.
	wantStatus(t, e.putPaid(bob, "bobbyx", e.now(), p), 409, "ERR_PURCHASE_USED")
	// Nor can bob take alice's handle with it.
	wantStatus(t, e.putPaid(bob, "deggen", e.now(), p), 409, "ERR_PURCHASE_USED")
	if e.holder("bobbyx") != "" {
		t.Fatal("reused receipt granted a second handle")
	}

	// A handle change: alice releases, then claims another name of the same
	// tier. The subscription follows her.
	wantStatus(t, e.put(alice, "deggen", e.advance(time.Minute), map[string]string{"released": "true"}), 200, "")
	wantStatus(t, e.putPaid(alice, "alicex", e.advance(time.Minute), p), 201, "")
	if s := e.sub("app-store:orig-3001"); s.Handle != "alicex" {
		t.Fatalf("subscription bound to %q, want alicex", s.Handle)
	}
	// And the released handle cannot be held by the same receipt as well.
	wantStatus(t, e.putPaid(bob, "deggen", e.advance(time.Minute), p), 409, "ERR_PURCHASE_USED")
}

func TestPaidHandle_FailedClaimLeavesReceiptUsable(t *testing.T) {
	e := newPurchaseEnv(t)
	alice, bob := testKey(t), testKey(t)
	pa, _ := e.appleSub("4001", iosLen6, 1)
	pb, _ := e.appleSub("4002", iosLen6, 1)
	wantStatus(t, e.putPaid(alice, "deggen", e.now(), pa), 201, "")

	// Bob's purchase is fine but the handle is alice's: the claim fails after
	// the receipt was bound to "deggen" for bob.
	wantStatus(t, e.putPaid(bob, "deggen", e.now(), pb), 409, "ERR_HANDLE_TAKEN")
	// That binding is to a handle bob does not hold, so the receipt still pays
	// for the next one he picks.
	wantStatus(t, e.putPaid(bob, "bobbyx", e.now(), pb), 201, "")
}

// appleNotify posts an App Store Server Notification about tx.
func (e *purchaseEnv) appleNotify(tx purchasestest.AppleTx, kind string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/api/store/apple/notifications", tx.Notification(e.t, kind))
}

func TestAppleNotification_UpdatesState(t *testing.T) {
	e := newPurchaseEnv(t)
	key := testKey(t)
	p, tx := e.appleSub("5001", iosLen6, 1)
	wantStatus(t, e.putPaid(key, "deggen", e.now(), p), 201, "")

	// Renewed: the API, not the notification, says until when.
	tx.TransactionID, tx.Expires = "5002", tx.Expires.Add(30*24*time.Hour)
	e.apple.Put(tx)
	wantStatus(t, e.appleNotify(tx, "DID_RENEW"), 200, "")
	if s := e.sub("app-store:orig-5001"); !s.ExpiresAt.Equal(tx.Expires) || s.State != storage.SubActive {
		t.Fatalf("after renewal %+v", s)
	}

	// Auto-renew turned off, then expired.
	tx.AutoRenewOn = false
	e.apple.Put(tx)
	wantStatus(t, e.appleNotify(tx, "DID_CHANGE_RENEWAL_STATUS"), 200, "")
	if s := e.sub("app-store:orig-5001"); s.State != storage.SubCanceled {
		t.Fatalf("after auto-renew off %+v", s)
	}
	tx.Status = 2
	e.apple.Put(tx)
	wantStatus(t, e.appleNotify(tx, "EXPIRED"), 200, "")
	if s := e.sub("app-store:orig-5001"); s.State != storage.SubExpired {
		t.Fatalf("after expiry %+v", s)
	}

	// A crossgrade to tier 7 does not keep a six-character handle paid.
	tx.Status, tx.ProductID, tx.Expires = 1, iosLen7, tx.Expires.Add(365*24*time.Hour)
	before := e.sub("app-store:orig-5001").ExpiresAt
	e.apple.Put(tx)
	wantStatus(t, e.appleNotify(tx, "DID_CHANGE_RENEWAL_PREF"), 200, "")
	if s := e.sub("app-store:orig-5001"); s.State != storage.SubWrongTier || !s.ExpiresAt.Equal(before) {
		t.Fatalf("after crossgrade %+v, want wrong_tier at %v", s, before)
	}

	// The store being down asks Apple to try again.
	e.apple.Fail = http.StatusInternalServerError
	wantStatus(t, e.appleNotify(tx, "DID_RENEW"), 503, "ERR_STORE_UNAVAILABLE")
}

func TestAppleNotification_IgnoresWhatItCannotUse(t *testing.T) {
	e := newPurchaseEnv(t)
	// A subscription bound to no handle, and Apple's TEST ping, are acknowledged.
	_, tx := e.appleSub("6001", iosLen6, 1)
	wantStatus(t, e.appleNotify(tx, "SUBSCRIBED"), 200, "")
	if e.sub("app-store:orig-6001") != nil {
		t.Fatal("a notification created a subscription")
	}
	ping, _ := json.Marshal(map[string]string{"signedPayload": purchasestest.SignJWS(t, map[string]any{"notificationType": "TEST", "data": map[string]any{}})})
	wantStatus(t, e.do("POST", "/api/store/apple/notifications", ping), 200, "")
	wantStatus(t, e.do("POST", "/api/store/apple/notifications", []byte(`{}`)), 400, "ERR_INVALID_NOTIFICATION")
	wantStatus(t, e.do("POST", "/api/store/apple/notifications", []byte(`{"signedPayload":"x.y.z"}`)), 400, "ERR_INVALID_NOTIFICATION")
	if e.apple.Calls != 0 {
		t.Fatalf("Apple was asked %d times about nothing", e.apple.Calls)
	}
}

// googlePush posts a Pub/Sub push carrying a developer notification.
func (e *purchaseEnv) googlePush(pkg, token string) *httptest.ResponseRecorder {
	e.t.Helper()
	n := map[string]any{"version": "1.0", "packageName": pkg, "eventTimeMillis": "1760000000000"}
	if token != "" {
		n["subscriptionNotification"] = map[string]any{"version": "1.0", "notificationType": 2, "purchaseToken": token}
	} else {
		n["testNotification"] = map[string]any{"version": "1.0"}
	}
	data, _ := json.Marshal(n)
	body, _ := json.Marshal(map[string]any{
		"message":      map[string]any{"data": base64.StdEncoding.EncodeToString(data), "messageId": "136969346945"},
		"subscription": "projects/bsv-wallet/subscriptions/play-rtdn",
	})
	return e.do("POST", "/api/store/google/notifications", body)
}

func TestGoogleNotification_UpdatesState(t *testing.T) {
	e := newPurchaseEnv(t)
	key := testKey(t)
	exp := e.now().Add(30 * 24 * time.Hour)
	g := purchasestest.GoogleSub{Token: "g1", ProductID: playLen6, BasePlan: "monthly", State: "SUBSCRIPTION_STATE_ACTIVE", Expires: exp}
	e.google.Put(g)
	wantStatus(t, e.putPaid(key, "deggen", e.now(), &purchases.Purchase{Store: storage.StoreGooglePlay, ProductID: playLen6, Token: "g1"}), 201, "")

	g.State, g.Expires = "SUBSCRIPTION_STATE_IN_GRACE_PERIOD", exp.Add(time.Hour)
	e.google.Put(g)
	wantStatus(t, e.googlePush(purchasestest.PackageName, "g1"), 200, "")
	if s := e.sub("google-play:g1"); s.State != storage.SubGrace || !s.ExpiresAt.Equal(g.Expires) {
		t.Fatalf("after push %+v", s)
	}

	// An upgrade to yearly: Google issues a new token linked to the old, and
	// the new token takes over the handle.
	e.google.Put(purchasestest.GoogleSub{Token: "g2", ProductID: playLen6, BasePlan: "yearly", State: "SUBSCRIPTION_STATE_ACTIVE", Expires: exp.Add(365 * 24 * time.Hour), Linked: "g1"})
	wantStatus(t, e.googlePush(purchasestest.PackageName, "g2"), 200, "")
	if s := e.sub("google-play:g2"); s == nil || s.Handle != "deggen" || s.IdentityKey != pubHex(key) || s.BasePlan != "yearly" {
		t.Fatalf("linked token = %+v", s)
	}

	// Other packages, test pings and unknown unlinked tokens are acknowledged.
	calls := e.google.Calls
	wantStatus(t, e.googlePush("com.someone.else", "g1"), 200, "")
	wantStatus(t, e.googlePush(purchasestest.PackageName, ""), 200, "")
	if e.google.Calls != calls {
		t.Fatal("ignored notifications reached Google")
	}
	e.google.Put(purchasestest.GoogleSub{Token: "g9", ProductID: playLen6, BasePlan: "monthly", State: "SUBSCRIPTION_STATE_ACTIVE", Expires: exp})
	wantStatus(t, e.googlePush(purchasestest.PackageName, "g9"), 200, "")
	if e.sub("google-play:g9") != nil {
		t.Fatal("an unbound token was recorded")
	}

	wantStatus(t, e.do("POST", "/api/store/google/notifications", []byte(`{"message":{"data":"!!"}}`)), 400, "ERR_INVALID_NOTIFICATION")
	e.google.Fail = http.StatusInternalServerError
	wantStatus(t, e.googlePush(purchasestest.PackageName, "g1"), 503, "ERR_STORE_UNAVAILABLE")
	e.google.Fail = 0

	e.auth.err = errors.New("bad token")
	wantStatus(t, e.googlePush(purchasestest.PackageName, "g1"), 401, "ERR_UNAUTHORIZED")
}

func TestSweep_ReleasesAfterGrace(t *testing.T) {
	e := newPurchaseEnv(t)
	ctx := context.Background()
	key := testKey(t)
	p, tx := e.appleSub("7001", iosLen6, 1)
	wantStatus(t, e.putPaid(key, "deggen", e.now(), p), 201, "")

	// It expires; Apple tells us.
	tx.Status = 2
	e.apple.Put(tx)
	wantStatus(t, e.appleNotify(tx, "EXPIRED"), 200, "")

	// Within the grace period nothing happens.
	e.set(tx.Expires.Add(grace - time.Hour))
	if n, err := e.srv.SweepLapsedSubscriptions(ctx); err != nil || n != 0 || e.holder("deggen") == "" {
		t.Fatalf("inside grace: released %d, %v; holder %q", n, err, e.holder("deggen"))
	}

	// Past it, the handle goes, with no cooldown, and only once.
	e.set(tx.Expires.Add(grace + time.Hour))
	if n, err := e.srv.SweepLapsedSubscriptions(ctx); err != nil || n != 1 || e.holder("deggen") != "" {
		t.Fatalf("after grace: released %d, %v; holder %q", n, err, e.holder("deggen"))
	}
	rec, _ := e.store.GetHandle(ctx, "deggen")
	if rec.ReleasedBy == nil || *rec.ReleasedBy != ReleasedBySubscription || rec.CooldownUntil != nil {
		t.Fatalf("released row %+v", rec)
	}
	if s := e.sub("app-store:orig-7001"); s.ReleasedAt == nil {
		t.Fatal("subscription not marked released")
	}
	if n, _ := e.srv.SweepLapsedSubscriptions(ctx); n != 0 {
		t.Fatal("swept twice")
	}

	// Resubscribing (Apple keeps the original transaction) wins it straight back.
	tx.Status, tx.TransactionID, tx.Expires = 1, "7002", e.now().Add(30*24*time.Hour)
	e.apple.Put(tx)
	p.TransactionID, p.Token = "7002", tx.SignedTransaction(t)
	wantStatus(t, e.putPaid(key, "deggen", e.advance(time.Minute), p), 201, "")
}

func TestSweep_MissedRenewalKeepsHandle(t *testing.T) {
	e := newPurchaseEnv(t)
	ctx := context.Background()
	key := testKey(t)
	p, tx := e.appleSub("8001", iosLen6, 1)
	wantStatus(t, e.putPaid(key, "deggen", e.now(), p), 201, "")

	// The renewal notification never arrived, but Apple knows.
	tx.Expires = tx.Expires.Add(60 * 24 * time.Hour)
	e.apple.Put(tx)
	e.set(tx.Expires.Add(-30*24*time.Hour + grace + time.Hour))
	if n, err := e.srv.SweepLapsedSubscriptions(ctx); err != nil || n != 0 || e.holder("deggen") == "" {
		t.Fatalf("released %d, %v", n, err)
	}
	if s := e.sub("app-store:orig-8001"); !s.ExpiresAt.Equal(tx.Expires) {
		t.Fatalf("sweep did not record the renewal: %+v", s)
	}

	// A store outage leaves the candidate for next time.
	e.set(tx.Expires.Add(grace + time.Hour))
	e.apple.Fail = http.StatusInternalServerError
	if n, err := e.srv.SweepLapsedSubscriptions(ctx); err != nil || n != 0 || e.holder("deggen") == "" {
		t.Fatalf("store down: released %d, %v", n, err)
	}
}

func TestSweep_LeavesHandleItNoLongerPaysFor(t *testing.T) {
	e := newPurchaseEnv(t)
	ctx := context.Background()
	alice, bob := testKey(t), testKey(t)
	p, tx := e.appleSub("9001", iosLen6, 1)
	wantStatus(t, e.putPaid(alice, "deggen", e.now(), p), 201, "")
	wantStatus(t, e.put(alice, "deggen", e.advance(time.Minute), map[string]string{"released": "true"}), 200, "")

	// Bob has since taken the name with his own subscription.
	e.set(e.now().Add(31 * 24 * time.Hour))
	pb, _ := e.appleSub("9002", iosLen6, 1)
	wantStatus(t, e.putPaid(bob, "deggen", e.now(), pb), 201, "")

	tx.Status = 2
	e.apple.Put(tx)
	e.set(tx.Expires.Add(grace + time.Hour))
	if n, err := e.srv.SweepLapsedSubscriptions(ctx); err != nil || n != 0 || e.holder("deggen") != pubHex(bob) {
		t.Fatalf("alice's lapse took bob's handle: released %d, %v, holder %q", n, err, e.holder("deggen"))
	}
}

func TestPurchases_OffByDefault(t *testing.T) {
	// Without EnablePurchases the registry grants names as before.
	e := newLookupEnv(t)
	wantStatus(t, e.put(testKey(t), "deggen", e.now(), nil), 201, "")
	defer func() {
		if recover() == nil {
			t.Fatal("PurchaseRoutes without EnablePurchases did not panic")
		}
	}()
	e.srv.PurchaseRoutes()
}
