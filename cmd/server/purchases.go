package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"google.golang.org/api/idtoken"

	"github.com/bsv-blockchain/go-message-box-server/pkg/config"
	"github.com/bsv-blockchain/go-message-box-server/pkg/handlers"
	"github.com/bsv-blockchain/go-message-box-server/pkg/purchases"
	mbstorage "github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// readSecret returns value, or the contents of path when value is empty.
func readSecret(value, path, name string) ([]byte, error) {
	if value != "" {
		return []byte(value), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return b, nil
}

// purchaseVerifiers builds a verifier for each configured store. A store left
// out is refused by the handlers, so a paid handle can never be granted on a
// store this server cannot check.
func purchaseVerifiers(ctx context.Context, cfg *config.Config) (map[string]purchases.Verifier, error) {
	v := map[string]purchases.Verifier{}
	if a := cfg.Apple; a.Configured() {
		key, err := readSecret(a.PrivateKey, a.PrivateKeyPath, "APPLE_PRIVATE_KEY_PATH")
		if err != nil {
			return nil, err
		}
		apple, err := purchases.NewApple(purchases.AppleConfig{
			IssuerID: a.IssuerID, KeyID: a.KeyID, PrivateKey: key, BundleID: a.BundleID, Environments: a.Environments,
		})
		if err != nil {
			return nil, err
		}
		v[mbstorage.StoreAppStore] = apple
	}
	if g := cfg.Google; g.Configured() {
		creds, err := readSecret(g.ServiceAccountJSON, g.ServiceAccountPath, "GOOGLE_PLAY_SERVICE_ACCOUNT_PATH")
		if err != nil {
			return nil, err
		}
		google, err := purchases.NewGoogle(ctx, purchases.GoogleConfig{
			PackageName: g.PackageName, CredentialsJSON: creds, AllowTest: g.AllowTestPurchases,
		})
		if err != nil {
			return nil, err
		}
		v[mbstorage.StoreGooglePlay] = google
	}
	return v, nil
}

// pubsubAuth checks the OIDC token a Pub/Sub push subscription attaches when it
// is configured with a service account and audience.
type pubsubAuth struct {
	validator *idtoken.Validator
	audience  string
	email     string
}

func (a *pubsubAuth) Authenticate(r *http.Request) error {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return errors.New("no bearer token")
	}
	p, err := a.validator.Validate(r.Context(), tok, a.audience)
	if err != nil {
		return err
	}
	if a.email != "" && (p.Claims["email"] != a.email || p.Claims["email_verified"] != true) {
		return fmt.Errorf("token is for %v, not %s", p.Claims["email"], a.email)
	}
	return nil
}

// enablePurchases turns paid handles on when HANDLE_PURCHASES_REQUIRED is set
// and returns the notification routes to mount, or nil when the feature is
// off. It needs the registry: the config already refuses one without the other.
func enablePurchases(ctx context.Context, cfg *config.Config, srv *handlers.Server, store any) (http.Handler, error) {
	if !cfg.HandlePurchasesRequired {
		return nil, nil
	}
	subs, ok := store.(mbstorage.SubscriptionStore)
	if !ok {
		return nil, fmt.Errorf("HANDLE_PURCHASES_REQUIRED needs a storage backend that keeps subscriptions, %q does not", cfg.StorageBackend)
	}
	verifiers, err := purchaseVerifiers(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pc := handlers.PurchaseConfig{Verifiers: verifiers, Grace: cfg.HandleGrace, GooglePackage: cfg.Google.PackageName}
	if cfg.Google.PubSubAudience != "" {
		v, err := idtoken.NewValidator(ctx)
		if err != nil {
			return nil, fmt.Errorf("pub/sub token validator: %w", err)
		}
		pc.PushAuth = &pubsubAuth{validator: v, audience: cfg.Google.PubSubAudience, email: cfg.Google.PubSubServiceAccount}
	}
	srv.EnablePurchases(pc, subs)
	return srv.PurchaseRoutes(), nil
}
