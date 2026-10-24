// Package purchasestest stubs the App Store Server API and the Play Developer
// API, and mints the fixture receipts a wallet would send, so verifier and
// handler tests run without either store.
package purchasestest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/option"

	"github.com/bsv-blockchain/go-message-box-server/pkg/purchases"
)

// Fixed identifiers of the stub app.
const (
	BundleID    = "org.bsvblockchain.wallet"
	PackageName = "org.bsvblockchain.wallet"
	IssuerID    = "57246542-96fe-1a63-e053-0824d011072a"
	KeyID       = "2X9R4HXF34"
)

// SignJWS signs payload as a compact ES256 JWS with a throwaway key, the shape
// of everything StoreKit 2 and the App Store Server API sign.
func SignJWS(t testing.TB, payload any) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signWith(t, key, map[string]any{"alg": "ES256", "x5c": []string{"fixture"}}, payload)
}

func signWith(t testing.TB, key *ecdsa.PrivateKey, header, payload any) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	p, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	d := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig)
}

// writeJSON answers with v, or a 500 when v cannot be encoded.
func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(b)
}

// AppleTx is one App Store subscription as the stub serves it.
type AppleTx struct {
	TransactionID         string
	OriginalTransactionID string
	BundleID              string // defaults to BundleID
	ProductID             string
	Expires               time.Time
	Revoked               time.Time
	// Status is the API's subscription status: 1 active, 2 expired,
	// 3 billing retry, 4 grace period, 5 revoked.
	Status      int
	AutoRenewOn bool
	Environment string // defaults to Production
}

func (tx AppleTx) payload() map[string]any {
	m := map[string]any{
		"transactionId": tx.TransactionID, "originalTransactionId": tx.OriginalTransactionID,
		"bundleId": tx.BundleID, "productId": tx.ProductID, "expiresDate": tx.Expires.UnixMilli(),
		"environment": tx.Environment, "type": "Auto-Renewable Subscription",
	}
	if !tx.Revoked.IsZero() {
		m["revocationDate"] = tx.Revoked.UnixMilli()
	}
	return m
}

// SignedTransaction is the StoreKit 2 JWS the wallet holds for tx.
func (tx AppleTx) SignedTransaction(t testing.TB) string {
	t.Helper()
	return SignJWS(t, tx.fill().payload())
}

func (tx AppleTx) fill() AppleTx {
	if tx.BundleID == "" {
		tx.BundleID = BundleID
	}
	if tx.Environment == "" {
		tx.Environment = purchases.AppleProduction
	}
	if tx.OriginalTransactionID == "" {
		tx.OriginalTransactionID = tx.TransactionID
	}
	return tx
}

// Notification is the body Apple POSTs for an App Store Server Notification V2
// about tx.
func (tx AppleTx) Notification(t testing.TB, notificationType string) []byte {
	t.Helper()
	tx = tx.fill()
	payload := map[string]any{
		"notificationType": notificationType, "notificationUUID": "9ad56bd2-0bc6-42e0-af24-fd996d87a1e6",
		"version": "2.0", "signedDate": time.Now().UnixMilli(),
		"data": map[string]any{
			"bundleId": tx.BundleID, "environment": tx.Environment,
			"signedTransactionInfo": SignJWS(t, tx.payload()),
		},
	}
	b, err := json.Marshal(map[string]string{"signedPayload": SignJWS(t, payload)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// AppleStub is a fake App Store Server API over both environments.
type AppleStub struct {
	t      testing.TB
	Server *httptest.Server
	Key    []byte // the .p8 PEM the verifier signs with
	pub    *ecdsa.PublicKey

	mu  sync.Mutex
	txs map[string]AppleTx // by transaction id and by original transaction id
	// Fail, when non-zero, is the status every request answers with.
	Fail  int
	Calls int
}

// NewAppleStub starts the stub. Close it with t.Cleanup.
func NewAppleStub(t testing.TB) *AppleStub {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	s := &AppleStub{
		t: t, pub: &key.PublicKey, txs: map[string]AppleTx{},
		Key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Server.Close)
	return s
}

// Put adds or replaces a subscription's latest transaction.
func (s *AppleStub) Put(tx AppleTx) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx = tx.fill()
	s.txs[tx.TransactionID] = tx
	s.txs[tx.OriginalTransactionID] = tx
}

// Config is an AppleConfig pointing at the stub.
func (s *AppleStub) Config(envs ...string) purchases.AppleConfig {
	return purchases.AppleConfig{
		IssuerID: IssuerID, KeyID: KeyID, PrivateKey: s.Key, BundleID: BundleID, Environments: envs,
		BaseURLs: map[string]string{
			purchases.AppleProduction: s.Server.URL + "/Production",
			purchases.AppleSandbox:    s.Server.URL + "/Sandbox",
		},
	}
}

// checkBearer verifies the request's JWT the way Apple does.
func (s *AppleStub) checkBearer(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(tok, ".")
	if !ok || len(parts) != 3 {
		return false
	}
	var h map[string]string
	var c map[string]any
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if json.Unmarshal(hb, &h) != nil || json.Unmarshal(cb, &c) != nil {
		return false
	}
	if h["alg"] != "ES256" || h["kid"] != KeyID || c["iss"] != IssuerID || c["aud"] != "appstoreconnect-v1" || c["bid"] != BundleID {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(s.pub, d[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

func (s *AppleStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls++
	if s.Fail != 0 {
		http.Error(w, `{"errorCode":5000000,"errorMessage":"stub failure"}`, s.Fail)
		return
	}
	if !s.checkBearer(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	env, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	kind, id, _ := strings.Cut(strings.TrimPrefix(rest, "inApps/v1/"), "/")
	tx, ok := s.txs[id]
	if !ok || tx.Environment != env {
		http.Error(w, `{"errorCode":4040010,"errorMessage":"Transaction id not found."}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch kind {
	case "transactions":
		writeJSON(w, map[string]string{"signedTransactionInfo": SignJWS(s.t, tx.payload())})
	case "subscriptions":
		autoRenew := 0
		if tx.AutoRenewOn {
			autoRenew = 1
		}
		renewal := SignJWS(s.t, map[string]any{
			"originalTransactionId": tx.OriginalTransactionID, "autoRenewProductId": tx.ProductID,
			"autoRenewStatus": autoRenew, "environment": tx.Environment,
		})
		writeJSON(w, map[string]any{
			"environment": tx.Environment, "bundleId": tx.BundleID, "appAppleId": 6470000000,
			"data": []any{map[string]any{
				"subscriptionGroupIdentifier": "22508849",
				"lastTransactions": []any{map[string]any{
					"originalTransactionId": tx.OriginalTransactionID, "status": tx.Status,
					"signedTransactionInfo": SignJWS(s.t, tx.payload()), "signedRenewalInfo": renewal,
				}},
			}},
		})
	default:
		http.NotFound(w, r)
	}
}

// GoogleSub is one Play subscription as the stub serves it.
type GoogleSub struct {
	Token        string
	ProductID    string
	BasePlan     string
	State        string // SUBSCRIPTION_STATE_*
	Expires      time.Time
	Linked       string
	TestPurchase bool
}

// GoogleStub is a fake Play Developer API.
type GoogleStub struct {
	Server *httptest.Server

	mu   sync.Mutex
	subs map[string]GoogleSub
	// Fail, when non-zero, is the status every request answers with.
	Fail  int
	Calls int
}

// NewGoogleStub starts the stub.
func NewGoogleStub(t testing.TB) *GoogleStub {
	t.Helper()
	s := &GoogleStub{subs: map[string]GoogleSub{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Server.Close)
	return s
}

// Put adds or replaces a subscription.
func (s *GoogleStub) Put(sub GoogleSub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[sub.Token] = sub
}

// Config is a GoogleConfig pointing at the stub.
func (s *GoogleStub) Config(allowTest bool) purchases.GoogleConfig {
	return purchases.GoogleConfig{
		PackageName: PackageName, AllowTest: allowTest,
		Options: []option.ClientOption{option.WithEndpoint(s.Server.URL), option.WithoutAuthentication()},
	}
}

func (s *GoogleStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls++
	writeErr := func(code int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		writeJSON(w, map[string]any{"error": map[string]any{"code": code, "message": msg}})
	}
	if s.Fail != 0 {
		writeErr(s.Fail, "stub failure")
		return
	}
	prefix := "/androidpublisher/v3/applications/" + PackageName + "/purchases/subscriptionsv2/tokens/"
	token, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		writeErr(http.StatusNotFound, "unknown path "+r.URL.Path)
		return
	}
	sub, ok := s.subs[token]
	if !ok {
		writeErr(http.StatusBadRequest, "The purchase token is no longer valid.")
		return
	}
	body := map[string]any{
		"kind":              "androidpublisher#subscriptionPurchaseV2",
		"subscriptionState": sub.State,
		"latestOrderId":     "GPA.3345-1234-5678-90123",
		"lineItems": []any{map[string]any{
			"productId": sub.ProductID, "expiryTime": sub.Expires.UTC().Format(time.RFC3339Nano),
			"offerDetails": map[string]any{"basePlanId": sub.BasePlan},
		}},
	}
	if sub.Linked != "" {
		body["linkedPurchaseToken"] = sub.Linked
	}
	if sub.TestPurchase {
		body["testPurchase"] = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, body)
}
