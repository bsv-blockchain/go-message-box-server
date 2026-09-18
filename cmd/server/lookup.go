package main

import (
	"fmt"
	"net/http"

	"github.com/bsv-blockchain/go-message-box-server/pkg/config"
	"github.com/bsv-blockchain/go-message-box-server/pkg/handlers"
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
		return nil, nil
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
	return handlers.NewRateLimiter(cfg.LookupRatePerMin, cfg.TrustProxy, cfg.TrustedProxyHops).Wrap(srv.LookupRoutes())
}
