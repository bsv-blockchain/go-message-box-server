package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-message-box-server/pkg/config"
	"github.com/bsv-blockchain/go-message-box-server/pkg/handlers"
	mbstorage "github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// emptyRegistry is a handle registry with nothing in it. The wiring under test
// never reads a row; it decides whether the routes exist at all.
type emptyRegistry struct{}

func (emptyRegistry) GetHandle(context.Context, string) (*mbstorage.HandleRecord, error) {
	return nil, nil
}

func (emptyRegistry) GetHandleBySkeleton(context.Context, string) (*mbstorage.HandleRecord, error) {
	return nil, nil
}

func (emptyRegistry) GetHandleByIdentityKey(context.Context, string) (*mbstorage.HandleRecord, error) {
	return nil, nil
}

func (emptyRegistry) ClaimHandle(context.Context, mbstorage.HandleClaim) (mbstorage.ClaimResult, error) {
	return 0, nil
}

func (emptyRegistry) ReleaseHandle(context.Context, mbstorage.HandleRelease) error { return nil }

func (emptyRegistry) FindHandles(context.Context, mbstorage.HandleMatch) ([]mbstorage.HandleRecord, error) {
	return []mbstorage.HandleRecord{}, nil
}

// noRegistry is a backend that carries no handle registry, as every SQL backend
// does.
type noRegistry struct{}

func lookupConfig(domain, backend string) *config.Config {
	return &config.Config{
		PaymailDomain:    domain,
		PaymailHost:      "https://mb.example.com",
		StorageBackend:   backend,
		LookupRatePerMin: 60,
		TrustedProxyHops: 1,
	}
}

// The feature requires the Mongo backend, and the refusal has to come from the
// config alone: by the time a store is open there is a database file and a
// wallet to clean up.
func TestCheckLookupBackend(t *testing.T) {
	for _, c := range []struct {
		name, domain, backend string
		wantErr               bool
	}{
		{"off on sql", "", "sql", false},
		{"off on an empty backend name", "", "", false},
		{"on with mongo", "example.com", "mongo", false},
		{"on with sql", "example.com", "sql", true},
		{"on with the default backend", "example.com", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := checkLookupBackend(lookupConfig(c.domain, c.backend))
			if (err != nil) != c.wantErr {
				t.Fatalf("checkLookupBackend = %v, want error: %v", err, c.wantErr)
			}
		})
	}
}

func TestLookupRegistry(t *testing.T) {
	// Off: the backend is never even asked, so a store with no registry is fine.
	got, err := lookupRegistry(lookupConfig("", "sql"), noRegistry{})
	if got != nil || err != nil {
		t.Errorf("feature off = %v, %v; want nil, nil", got, err)
	}
	// On, but the opened backend carries no registry: refuse rather than mount
	// routes with nothing behind them.
	if _, err := lookupRegistry(lookupConfig("example.com", "sql"), noRegistry{}); err == nil {
		t.Error("a backend without a registry must be refused")
	}
	got, err = lookupRegistry(lookupConfig("example.com", "mongo"), emptyRegistry{})
	if got == nil || err != nil {
		t.Errorf("mongo registry = %v, %v; want the registry", got, err)
	}
}

// Without PAYMAIL_DOMAIN there is no public handler, so nothing of the feature
// is mounted — the unauthenticated write route included.
func TestMountLookup_OffWithoutDomain(t *testing.T) {
	if h := mountLookup(lookupConfig("", "mongo"), handlers.NewServer(nil, nil), emptyRegistry{}); h != nil {
		t.Error("mountLookup returned a handler with PAYMAIL_DOMAIN unset")
	}
	if h := mountLookup(lookupConfig("example.com", "mongo"), handlers.NewServer(nil, nil), nil); h != nil {
		t.Error("mountLookup returned a handler with no registry")
	}
}

func TestMountLookup_ServesThePublicRoutes(t *testing.T) {
	cfg := lookupConfig("example.com", "mongo")
	h := mountLookup(cfg, handlers.NewServer(nil, nil), emptyRegistry{})
	if h == nil {
		t.Fatal("mountLookup returned no handler")
	}

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	for _, path := range []string{
		"/.well-known/bsvalias",
		"/api/handle/deg",
		"/api/handle/available/deggen",
	} {
		if w := get(path); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, w.Code)
		}
	}
	if w := get("/api/identityKey/" + strings.Repeat("0", 66)); w.Code != http.StatusBadRequest {
		t.Errorf("reverse lookup of a malformed key = %d, want 400", w.Code)
	}

	// The capability document is what clients resolve the routes from, so it has
	// to name this deployment's host.
	var doc struct {
		Capabilities map[string]string `json:"capabilities"`
	}
	if err := json.Unmarshal(get("/.well-known/bsvalias").Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Capabilities["0ace65da5987"] != cfg.PaymailHost+"/api/handle/{query}" {
		t.Errorf("capabilities = %v", doc.Capabilities)
	}

	// PUT is unauthenticated by design, so the limiter around it is the only
	// thing in front of the registry.
	cfg.LookupRatePerMin = 1
	limited := mountLookup(cfg, handlers.NewServer(nil, nil), emptyRegistry{})
	first := httptest.NewRecorder()
	limited.ServeHTTP(first, httptest.NewRequest("GET", "/.well-known/bsvalias", nil))
	second := httptest.NewRecorder()
	limited.ServeHTTP(second, httptest.NewRequest("GET", "/.well-known/bsvalias", nil))
	if first.Code != http.StatusOK || second.Code != http.StatusTooManyRequests {
		t.Errorf("rate limit = %d then %d, want 200 then 429", first.Code, second.Code)
	}
}

// TestMountLookup_ClientIPHeader proves cfg.ClientIPHeader reaches the
// RateLimiter that mountLookup builds: two requests with different RemoteAddrs
// but the same configured header share a bucket, and a differing header value
// does not.
func TestMountLookup_ClientIPHeader(t *testing.T) {
	cfg := lookupConfig("example.com", "mongo")
	cfg.LookupRatePerMin = 1
	cfg.ClientIPHeader = "CF-Connecting-IP"
	h := mountLookup(cfg, handlers.NewServer(nil, nil), emptyRegistry{})

	get := func(remoteAddr, headerValue string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "/.well-known/bsvalias", nil)
		r.RemoteAddr = remoteAddr
		if headerValue != "" {
			r.Header.Set("CF-Connecting-IP", headerValue)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := get("10.0.0.1:1", "5.5.5.5"); w.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", w.Code)
	}
	// Different RemoteAddr, same header value: same bucket, so this is refused.
	if w := get("10.0.0.2:1", "5.5.5.5"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("same header value from a different RemoteAddr = %d, want 429", w.Code)
	}
	// Different header value: its own bucket.
	if w := get("10.0.0.1:1", "6.6.6.6"); w.Code != http.StatusOK {
		t.Fatalf("different header value = %d, want 200", w.Code)
	}
}
