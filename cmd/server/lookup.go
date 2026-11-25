package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker"

	"github.com/bsv-blockchain/go-message-box-server/pkg/config"
	"github.com/bsv-blockchain/go-message-box-server/pkg/handlers"
	"github.com/bsv-blockchain/go-message-box-server/pkg/p2p"
	mbstorage "github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// The paymail profile lookup feature is wired here rather than inline in main
// so that the three rules deciding whether it runs at all — it is off without a
// domain, it needs the Mongo backend, and its routes are public — are reachable
// from a test. They are the only thing keeping unauthenticated write routes off
// a deployment that never asked for them.

// lookupEnabled reports whether the feature is configured. Everything below is
// off when this is false.
func lookupEnabled(cfg *config.Config) bool { return cfg.PaymailDomain != "" }

// checkLookupBackend refuses a lookup deployment the backend cannot serve. This
// is knowable from the config alone, so main asks before opening anything: a
// misconfigured process in a crash loop should leave no database file, no
// wallet storage and no goroutine behind.
func checkLookupBackend(cfg *config.Config) error {
	if lookupEnabled(cfg) && cfg.StorageBackend != "mongo" {
		return fmt.Errorf("PAYMAIL_DOMAIN requires STORAGE_BACKEND=mongo, got %q", cfg.StorageBackend)
	}
	return nil
}

// lookupRegistry takes the handle registry out of an opened backend; (nil, nil)
// means the feature is off. store is taken as any because the one question
// asked of it here is whether it carries a registry — the caller has already
// opened it as a storage.Store.
func lookupRegistry(cfg *config.Config, store any) (mbstorage.HandleStore, error) {
	if !lookupEnabled(cfg) {
		return nil, nil //nolint:nilnil // (nil, nil) is the documented "feature off" result
	}
	registry, ok := store.(mbstorage.HandleStore)
	if !ok {
		return nil, fmt.Errorf("PAYMAIL_DOMAIN requires a storage backend with a handle registry, %q has none", cfg.StorageBackend)
	}
	return registry, nil
}

// mountLookup turns the feature on and returns the public handler, or nil when
// there is nothing to mount. The returned handler is the whole public surface:
// it goes outside the auth and payment wrap, so the rate limiter around it is
// the only thing in front of these routes.
func mountLookup(cfg *config.Config, srv *handlers.Server, registry mbstorage.HandleStore) http.Handler {
	if !lookupEnabled(cfg) || registry == nil {
		return nil
	}
	srv.EnableLookup(handlers.LookupConfig{
		Domain:    cfg.PaymailDomain,
		Host:      cfg.PaymailHost,
		Cooldown:  cfg.HandleCooldown,
		AdminKeys: cfg.AdminIdentityKeys,
	}, registry)
	return handlers.NewRateLimiter(cfg.LookupRatePerMin, cfg.TrustProxy, cfg.TrustedProxyHops, cfg.ClientIPHeader).Wrap(srv.LookupRoutes())
}

// p2pWiring is everything the paymail receive routes need besides the handle
// registry. It is a struct so a test can mount the routes with fakes.
type p2pWiring struct {
	Store       mbstorage.P2PDestinationStore
	Broadcaster p2p.Broadcaster
	Verifier    p2p.Verifier
	Parents     p2p.ParentFetcher
}

// p2pHTTPTimeout bounds each outside request the paymail rail makes, so a
// hung Arcade or WhatsOnChain cannot hold a payment forever.
const p2pHTTPTimeout = 20 * time.Second

// newP2PWiring builds the production parts, or (nil, nil) when the rail is
// off. store is taken as any for the same reason as in lookupRegistry: only the
// Mongo backend carries a destination store, and a deployment that turned the
// rail on against another backend must fail at startup, not at the first
// payment.
func newP2PWiring(cfg *config.Config, store any) (*p2pWiring, error) {
	if !cfg.P2PEnabled() {
		return nil, nil //nolint:nilnil // (nil, nil) is the documented "feature off" result
	}
	dests, ok := store.(mbstorage.P2PDestinationStore)
	if !ok {
		return nil, fmt.Errorf("P2P_RECEIVE_ENABLED requires a storage backend with a destination store, %q has none", cfg.StorageBackend)
	}
	client := &http.Client{Timeout: p2pHTTPTimeout}
	return &p2pWiring{
		Store:       dests,
		Broadcaster: p2p.NewArcade(cfg.BroadcasterURL, cfg.BroadcasterAPIKey, client),
		Verifier:    p2p.NewSPV(chaintracker.NewWhatsOnChain(chaintracker.MainNet, cfg.WOCAPIKey, chaintracker.WithHTTPClient(client))),
		Parents:     p2p.NewWhatsOnChain(cfg.WOCAPIKey, nil),
	}, nil
}

// mountP2P turns the paymail receive routes on and returns their handler, or
// nil when the rail is off. mountLookup must have run first: the routes resolve
// handles through the registry it installs. The handler has its own limiter,
// separate from the lookup one: Traefik hides client addresses, so each limiter
// is one bucket for everybody, and a HandCash payment makes three or four
// public calls that lookup traffic must not be able to starve.
func mountP2P(cfg *config.Config, srv *handlers.Server, w *p2pWiring) http.Handler {
	if !cfg.P2PEnabled() || w == nil {
		return nil
	}
	srv.EnableP2P(handlers.P2PConfig{
		DestinationTTL: cfg.P2PDestinationTTL,
		MaxSatoshis:    cfg.P2PMaxSatoshis,
		AvatarURL:      cfg.PaymailAvatarURL,
		Broadcaster:    w.Broadcaster,
		Verifier:       w.Verifier,
		Parents:        w.Parents,
	}, w.Store)
	return handlers.NewRateLimiter(cfg.P2PRatePerMin, cfg.TrustProxy, cfg.TrustedProxyHops, cfg.ClientIPHeader).Wrap(srv.P2PRoutes())
}

// mountPublicRoutes registers the unauthenticated handlers on the root mux.
// ServeMux picks the most specific pattern regardless of registration order, so
// these win over the authenticated catch-all. Any handler may be nil.
func mountPublicRoutes(root *http.ServeMux, public, p2pHandler, storeRoutes http.Handler) {
	if public != nil {
		root.Handle("/.well-known/bsvalias", public)
		root.Handle("/api/handle", public)
		root.Handle("/api/handle/", public)
		root.Handle("/api/identityKey/", public)
		if storeRoutes != nil {
			root.Handle("/api/store/", storeRoutes)
		}
	}
	if p2pHandler != nil {
		root.Handle("/api/bsvalias/", p2pHandler)
	}
}
