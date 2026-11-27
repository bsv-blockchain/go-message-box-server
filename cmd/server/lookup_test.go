package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/config"
	"github.com/bsv-blockchain/go-message-box-server/pkg/handlers"
	mbstorage "github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// emptyRegistry is a handle registry with nothing in it. The wiring under test
// never reads a row; it decides whether the routes exist at all.
type emptyRegistry struct{}

func (emptyRegistry) GetHandle(context.Context, string) (*mbstorage.HandleRecord, error) {
	return nil, nil //nolint:nilnil // HandleStore contract: no record is (nil, nil)
}

func (emptyRegistry) GetHandleBySkeleton(context.Context, string) (*mbstorage.HandleRecord, error) {
	return nil, nil //nolint:nilnil // HandleStore contract: no record is (nil, nil)
}

func (emptyRegistry) GetHandleByIdentityKey(context.Context, string) (*mbstorage.HandleRecord, error) {
	return nil, nil //nolint:nilnil // HandleStore contract: no record is (nil, nil)
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
	if _, err = lookupRegistry(lookupConfig("example.com", "sql"), noRegistry{}); err == nil {
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
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", path, nil))
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
	limited.ServeHTTP(first, httptest.NewRequestWithContext(t.Context(), "GET", "/.well-known/bsvalias", nil))
	second := httptest.NewRecorder()
	limited.ServeHTTP(second, httptest.NewRequestWithContext(t.Context(), "GET", "/.well-known/bsvalias", nil))
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
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/.well-known/bsvalias", nil)
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

// emptySubscriptions is a subscription store with nothing in it.
type emptySubscriptions struct{}

func (emptySubscriptions) GetSubscription(context.Context, string) (*mbstorage.Subscription, error) {
	return nil, nil //nolint:nilnil // SubscriptionStore contract: no row is (nil, nil)
}

func (emptySubscriptions) BindSubscription(context.Context, mbstorage.Subscription, *mbstorage.Subscription) error {
	return nil
}

func (emptySubscriptions) UpdateSubscriptionStatus(context.Context, string, mbstorage.SubscriptionStatus, time.Time) error {
	return nil
}

func (emptySubscriptions) MarkSubscriptionReleased(context.Context, string, time.Time) error {
	return nil
}

func (emptySubscriptions) ListSubscriptionsByHandle(context.Context, string) ([]mbstorage.Subscription, error) {
	return []mbstorage.Subscription{}, nil
}

func (emptySubscriptions) ListLapsedSubscriptions(context.Context, time.Time, int) ([]mbstorage.Subscription, error) {
	return []mbstorage.Subscription{}, nil
}

// emptyDestinations is a destination store that issues anything and finds
// nothing. The wiring under test never reads a row back.
type emptyDestinations struct{}

func (emptyDestinations) CreateDestination(context.Context, mbstorage.P2PDestination) error {
	return nil
}

func (emptyDestinations) GetDestination(context.Context, string, time.Time) (*mbstorage.P2PDestination, error) {
	return nil, mbstorage.ErrDestinationNotFound
}

func (emptyDestinations) MarkDestinationUsed(context.Context, string, string, time.Time) error {
	return nil
}

func (emptyDestinations) ExtendDestinationRetention(context.Context, string, time.Time) error {
	return nil
}

func (emptyDestinations) MarkDestinationDelivered(context.Context, string, string) error {
	return nil
}

// p2pTestConfig is a lookup config with the receive rail switched on. The
// handle registry holds one handle, rosa, so the routes have something to
// resolve.
func p2pTestConfig() *config.Config {
	cfg := lookupConfig("example.com", "mongo")
	cfg.P2PReceiveEnabled = true
	cfg.BroadcasterURL = "https://arcade.example.com"
	cfg.P2PRatePerMin = 120
	cfg.P2PDestinationTTL = time.Hour
	cfg.P2PMaxSatoshis = 1_000_000
	return cfg
}

type rosaRegistry struct{ emptyRegistry }

func (rosaRegistry) GetHandle(_ context.Context, h string) (*mbstorage.HandleRecord, error) {
	if h != "rosa" {
		return nil, nil //nolint:nilnil // HandleStore contract: no record is (nil, nil)
	}
	key := "02" + strings.Repeat("ab", 32)
	return &mbstorage.HandleRecord{Handle: "rosa", IdentityKey: &key}, nil
}

// mountP2PRoot builds the root mux the way main does, so the test sees the
// routes a deployment would serve.
func mountP2PRoot(t *testing.T, cfg *config.Config) *http.ServeMux {
	t.Helper()
	srv := handlers.NewServer(nil, nil)
	public := mountLookup(cfg, srv, rosaRegistry{})
	p2pHandler := mountP2P(cfg, srv, &p2pWiring{Store: emptyDestinations{}})
	root := http.NewServeMux()
	mountPublicRoutes(root, public, p2pHandler, nil)
	return root
}

func serve(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body)))
	return w
}

func TestMountP2P_ServesTheRoutesThroughTheRootMux(t *testing.T) {
	root := mountP2PRoot(t, p2pTestConfig())
	if w := serve(t, root, "GET", "/api/bsvalias/id/rosa@example.com", ""); w.Code != http.StatusOK {
		t.Errorf("GET pki = %d, want 200", w.Code)
	}
	if w := serve(t, root, "GET", "/api/bsvalias/public-profile/rosa@example.com", ""); w.Code != http.StatusOK {
		t.Errorf("GET public-profile = %d, want 200", w.Code)
	}
	if w := serve(t, root, "POST", "/api/bsvalias/p2p-payment-destination/rosa@example.com", `{"satoshis":1000}`); w.Code != http.StatusOK {
		t.Errorf("POST destination = %d, want 200", w.Code)
	}
	// The two receive routes are mounted: a bad body is the route's own 400,
	// not the mux's 404 or 405.
	for _, p := range []string{"beef", "receive-transaction"} {
		if w := serve(t, root, "POST", "/api/bsvalias/"+p+"/rosa@example.com", `not json`); w.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", p, w.Code)
		}
	}
	// The capability document names them.
	var doc struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(serve(t, root, "GET", "/.well-known/bsvalias", "").Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Capabilities["2a40af698840"] == nil {
		t.Errorf("capabilities = %v, want the p2p destination", doc.Capabilities)
	}
}

// The paymail routes, and only they, write the request log: it is how a
// refused sender shows where it stopped.
func TestMountPublicRoutes_LogsPaymailRequests(t *testing.T) {
	var buf bytes.Buffer
	t.Cleanup(logger.SetOutput(&buf))
	root := mountP2PRoot(t, p2pTestConfig())

	serve(t, root, "GET", "/.well-known/bsvalias", "")
	serve(t, root, "POST", "/api/bsvalias/receive-transaction/rosa@example.com", `not json`)
	serve(t, root, "GET", "/api/handle/rosa", "")

	out := buf.String()
	if !strings.Contains(out, "path=/.well-known/bsvalias status=200") {
		t.Errorf("log %q has no capability document record", out)
	}
	if !strings.Contains(out, "path=/api/bsvalias/receive-transaction/rosa@example.com status=400") ||
		!strings.Contains(out, "code=ERR_INVALID_BODY") {
		t.Errorf("log %q has no refused receive record", out)
	}
	if strings.Contains(out, "path=/api/handle/") {
		t.Errorf("log %q records a handle lookup, which is not a paymail route", out)
	}
}

func TestMountP2P_OffServesNothing(t *testing.T) {
	cfg := p2pTestConfig()
	cfg.P2PReceiveEnabled = false
	if h := mountP2P(cfg, handlers.NewServer(nil, nil), &p2pWiring{Store: emptyDestinations{}}); h != nil {
		t.Error("mountP2P returned a handler with P2P_RECEIVE_ENABLED unset")
	}
	root := mountP2PRoot(t, cfg)
	if w := serve(t, root, "GET", "/api/bsvalias/id/rosa@example.com", ""); w.Code != http.StatusNotFound {
		t.Errorf("GET pki with the rail off = %d, want 404", w.Code)
	}
	// The well-known document is what it was before the rail existed.
	var doc struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(serve(t, root, "GET", "/.well-known/bsvalias", "").Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Capabilities) != 2 {
		t.Errorf("capabilities = %v, want only the two lookup ones", doc.Capabilities)
	}
}

// Traefik hides client addresses, so every public caller shares one bucket per
// limiter. A payment needs several P2P calls; lookup traffic must not eat them.
func TestMountP2P_OwnRateBucket(t *testing.T) {
	cfg := p2pTestConfig()
	cfg.LookupRatePerMin = 1
	root := mountP2PRoot(t, cfg)

	serve(t, root, "GET", "/.well-known/bsvalias", "")
	if w := serve(t, root, "GET", "/.well-known/bsvalias", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("lookup bucket not exhausted: %d", w.Code)
	}
	if w := serve(t, root, "POST", "/api/bsvalias/p2p-payment-destination/rosa@example.com", `{"satoshis":1000}`); w.Code != http.StatusOK {
		t.Errorf("destination after lookup exhaustion = %d, want 200", w.Code)
	}

	// And the P2P bucket does limit.
	cfg.P2PRatePerMin = 1
	root = mountP2PRoot(t, cfg)
	serve(t, root, "GET", "/api/bsvalias/id/rosa@example.com", "")
	if w := serve(t, root, "GET", "/api/bsvalias/id/rosa@example.com", ""); w.Code != http.StatusTooManyRequests {
		t.Errorf("second p2p call = %d, want 429", w.Code)
	}
}

func TestP2PWiring(t *testing.T) {
	cfg := p2pTestConfig()
	got, err := newP2PWiring(lookupConfig("example.com", "mongo"), noRegistry{})
	if got != nil || err != nil {
		t.Errorf("rail off = %v, %v; want nil, nil", got, err)
	}
	if _, err = newP2PWiring(cfg, noRegistry{}); err == nil {
		t.Error("a backend without a destination store must be refused")
	}
	got, err = newP2PWiring(cfg, emptyDestinations{})
	if err != nil || got == nil || got.Broadcaster == nil || got.Verifier == nil || got.Parents == nil {
		t.Errorf("wiring = %+v, %v; want every part set", got, err)
	}
}
