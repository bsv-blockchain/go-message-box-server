package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-message-box-server/pkg/config"
	"github.com/bsv-blockchain/go-message-box-server/pkg/handlers"
	"github.com/bsv-blockchain/go-message-box-server/pkg/purchases/purchasestest"
)

func TestEnablePurchases_OffUnlessRequired(t *testing.T) {
	cfg := lookupConfig("example.com", "mongo")
	h, err := enablePurchases(context.Background(), cfg, handlers.NewServer(nil, nil), emptyRegistry{})
	if h != nil || err != nil {
		t.Fatalf("enablePurchases = %v, %v; want nil, nil", h, err)
	}
}

func TestEnablePurchases_NeedsSubscriptionStore(t *testing.T) {
	cfg := lookupConfig("example.com", "mongo")
	cfg.HandlePurchasesRequired = true
	cfg.Apple = config.AppleStoreConfig{BundleID: purchasestest.BundleID}
	if _, err := enablePurchases(context.Background(), cfg, handlers.NewServer(nil, nil), noRegistry{}); err == nil ||
		!strings.Contains(err.Error(), "subscriptions") {
		t.Fatalf("err = %v, want a refusal naming the backend", err)
	}
}

func TestEnablePurchases_MountsNotificationRoutes(t *testing.T) {
	cfg := lookupConfig("example.com", "mongo")
	cfg.HandlePurchasesRequired = true
	cfg.Apple = config.AppleStoreConfig{
		BundleID: purchasestest.BundleID, IssuerID: purchasestest.IssuerID, KeyID: purchasestest.KeyID,
		PrivateKey: string(purchasestest.NewAppleStub(t).Key), Environments: []string{"Production"},
	}
	srv := handlers.NewServer(nil, nil)
	srv.EnableLookup(handlers.LookupConfig{Domain: "example.com"}, emptyRegistry{})
	h, err := enablePurchases(context.Background(), cfg, srv, struct {
		emptyRegistry
		emptySubscriptions
	}{})
	if err != nil || h == nil {
		t.Fatalf("enablePurchases = %v, %v", h, err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/store/apple/notifications", strings.NewReader(`{}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("apple notification route = %d, want 400 for an empty body", w.Code)
	}

	// A bad key is a startup error, not a server that refuses every purchase.
	cfg.Apple.PrivateKey = "not a key"
	if _, err := enablePurchases(context.Background(), cfg, handlers.NewServer(nil, nil), struct {
		emptyRegistry
		emptySubscriptions
	}{}); err == nil {
		t.Fatal("bad Apple key accepted")
	}
}
