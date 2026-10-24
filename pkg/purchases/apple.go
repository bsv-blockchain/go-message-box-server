package purchases

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// The App Store Server API's two environments, as Apple spells them in
// transactions and as the base URLs below are keyed.
const (
	AppleProduction = "Production"
	AppleSandbox    = "Sandbox"
)

// AppleBaseURLs are the App Store Server API hosts.
var AppleBaseURLs = map[string]string{
	AppleProduction: "https://api.storekit.itunes.apple.com",
	AppleSandbox:    "https://api.storekit-sandbox.itunes.apple.com",
}

// AppleConfig holds an App Store Connect in-app purchase key.
type AppleConfig struct {
	IssuerID   string // App Store Connect → Users and Access → Integrations → In-App Purchase
	KeyID      string
	PrivateKey []byte // the .p8 file's PEM
	BundleID   string
	// Environments are tried in order for a purchase not yet on file.
	// Production alone refuses sandbox (TestFlight, App Review) purchases.
	Environments []string
	// BaseURLs overrides AppleBaseURLs (tests).
	BaseURLs   map[string]string
	HTTPClient *http.Client
	Now        func() time.Time
}

// Apple verifies App Store subscriptions with the App Store Server API.
type Apple struct {
	cfg AppleConfig
	key *ecdsa.PrivateKey
}

// NewApple checks the configuration and parses the key.
func NewApple(cfg AppleConfig) (*Apple, error) {
	if cfg.IssuerID == "" || cfg.KeyID == "" || cfg.BundleID == "" {
		return nil, errors.New("apple: issuer id, key id and bundle id are required")
	}
	if len(cfg.Environments) == 0 {
		cfg.Environments = []string{AppleProduction}
	}
	for _, env := range cfg.Environments {
		if _, ok := AppleBaseURLs[env]; !ok {
			return nil, fmt.Errorf("apple: unknown environment %q (want %s or %s)", env, AppleProduction, AppleSandbox)
		}
	}
	if cfg.BaseURLs == nil {
		cfg.BaseURLs = AppleBaseURLs
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	block, _ := pem.Decode(cfg.PrivateKey)
	if block == nil {
		return nil, errors.New("apple: private key is not PEM (expected the .p8 file)")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apple: private key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apple: private key is not an EC key")
	}
	return &Apple{cfg: cfg, key: ec}, nil
}

// token is the API's bearer: an ES256 JWT signed with the in-app purchase key.
func (a *Apple) token() (string, error) {
	now := a.cfg.Now()
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": a.cfg.KeyID, "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": a.cfg.IssuerID, "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		"aud": "appstoreconnect-v1", "bid": a.cfg.BundleID,
	})
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// errNotFound is a 404 from one environment: the id belongs to another
// environment, or to nobody.
var errNotFound = errors.New("not found in this environment")

func (a *Apple) get(ctx context.Context, env, path string, out any) error {
	tok, err := a.token()
	if err != nil {
		return unavailable("apple: sign request: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.BaseURLs[env]+path, nil)
	if err != nil {
		return unavailable("apple: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return unavailable("apple: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return unavailable("apple: read response: %v", err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		if err := json.Unmarshal(body, out); err != nil {
			return unavailable("apple: decode response: %v", err)
		}
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode == http.StatusBadRequest:
		// Apple's 400s here are a malformed transaction id: the client's fault.
		return invalid("apple: %s", strings.TrimSpace(string(body)))
	default:
		// 401 is this server's key, 429 and 5xx are Apple's: all retryable once
		// someone or something fixes them.
		return unavailable("apple: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// appleTransaction is the payload of a JWSTransaction. The API hands it over
// TLS from Apple's own host, so its signature is not checked again here.
type appleTransaction struct {
	TransactionID         string `json:"transactionId"`
	OriginalTransactionID string `json:"originalTransactionId"`
	BundleID              string `json:"bundleId"`
	ProductID             string `json:"productId"`
	ExpiresDate           int64  `json:"expiresDate"`
	RevocationDate        int64  `json:"revocationDate"`
	Environment           string `json:"environment"`
}

type appleRenewal struct {
	AutoRenewStatus int `json:"autoRenewStatus"`
}

// DecodeJWSPayload returns the payload of a compact JWS without checking its
// signature. Callers use it only on JWS Apple served them directly, or to read
// an id they then look up with Apple.
func DecodeJWSPayload(jws string, out any) error {
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		return errors.New("not a compact JWS")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("JWS payload: %w", err)
	}
	return json.Unmarshal(b, out)
}

// lookupTransaction finds a transaction in the configured environments, in
// order, and returns it with the environment it was found in.
func (a *Apple) lookupTransaction(ctx context.Context, transactionID string) (*appleTransaction, string, error) {
	for _, env := range a.cfg.Environments {
		var resp struct {
			SignedTransactionInfo string `json:"signedTransactionInfo"`
		}
		err := a.get(ctx, env, "/inApps/v1/transactions/"+url.PathEscape(transactionID), &resp)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		var tx appleTransaction
		if err := DecodeJWSPayload(resp.SignedTransactionInfo, &tx); err != nil {
			return nil, "", unavailable("apple: transaction info: %v", err)
		}
		return &tx, env, nil
	}
	return nil, "", invalid("apple: no transaction %s in %s", transactionID, strings.Join(a.cfg.Environments, " or "))
}

// status reads a subscription's current state with Get All Subscription
// Statuses, keyed on its original transaction.
func (a *Apple) status(ctx context.Context, env, originalTransactionID string) (*Verified, error) {
	var resp struct {
		BundleID string `json:"bundleId"`
		Data     []struct {
			LastTransactions []struct {
				OriginalTransactionID string `json:"originalTransactionId"`
				Status                int    `json:"status"`
				SignedTransactionInfo string `json:"signedTransactionInfo"`
				SignedRenewalInfo     string `json:"signedRenewalInfo"`
			} `json:"lastTransactions"`
		} `json:"data"`
	}
	err := a.get(ctx, env, "/inApps/v1/subscriptions/"+url.PathEscape(originalTransactionID), &resp)
	if errors.Is(err, errNotFound) {
		return nil, invalid("apple: no subscription %s in %s", originalTransactionID, env)
	}
	if err != nil {
		return nil, err
	}
	if resp.BundleID != a.cfg.BundleID {
		return nil, invalid("apple: subscription is for %q, not %q", resp.BundleID, a.cfg.BundleID)
	}
	for _, group := range resp.Data {
		for _, last := range group.LastTransactions {
			if last.OriginalTransactionID != originalTransactionID {
				continue
			}
			var tx appleTransaction
			if err := DecodeJWSPayload(last.SignedTransactionInfo, &tx); err != nil {
				return nil, unavailable("apple: transaction info: %v", err)
			}
			var renewal appleRenewal
			if last.SignedRenewalInfo != "" {
				if err := DecodeJWSPayload(last.SignedRenewalInfo, &renewal); err != nil {
					return nil, unavailable("apple: renewal info: %v", err)
				}
			}
			tier := AppleProductTier(tx.ProductID)
			if tier == 0 || tx.BundleID != a.cfg.BundleID {
				return nil, invalid("apple: %s is not a handle subscription of %s", tx.ProductID, a.cfg.BundleID)
			}
			st := storage.SubscriptionStatus{
				ProductID: tx.ProductID, Tier: tier, Environment: env,
				ExpiresAt: time.UnixMilli(tx.ExpiresDate).UTC(),
			}
			switch last.Status {
			case 1:
				st.State = storage.SubActive
				if last.SignedRenewalInfo != "" && renewal.AutoRenewStatus == 0 {
					st.State = storage.SubCanceled
				}
			case 2:
				st.State = storage.SubExpired
			case 3:
				st.State = storage.SubOnHold
			case 4:
				st.State = storage.SubGrace
			case 5:
				st.State = storage.SubRevoked
				if tx.RevocationDate != 0 {
					st.ExpiresAt = time.UnixMilli(tx.RevocationDate).UTC()
				}
			default:
				return nil, unavailable("apple: unknown subscription status %d", last.Status)
			}
			return &Verified{StoreID: originalTransactionID, SubscriptionStatus: st}, nil
		}
	}
	return nil, invalid("apple: subscription %s not in the status response", originalTransactionID)
}

// Verify implements Verifier. The transaction id comes from the request, or
// failing that from the signed transaction the wallet holds; either way Apple
// is asked about it rather than the signed transaction being believed.
func (a *Apple) Verify(ctx context.Context, p Purchase) (*Verified, error) {
	id := p.TransactionID
	if p.Token != "" {
		var tx appleTransaction
		if err := DecodeJWSPayload(p.Token, &tx); err != nil {
			return nil, invalid("apple: token is not a signed transaction: %v", err)
		}
		if id == "" {
			id = tx.TransactionID
		}
		if tx.TransactionID != id {
			return nil, invalid("apple: token is for transaction %s, not %s", tx.TransactionID, id)
		}
	}
	if id == "" {
		return nil, invalid("apple: transactionId is required")
	}
	tx, env, err := a.lookupTransaction(ctx, id)
	if err != nil {
		return nil, err
	}
	if tx.BundleID != a.cfg.BundleID {
		return nil, invalid("apple: transaction is for %q, not %q", tx.BundleID, a.cfg.BundleID)
	}
	if p.ProductID != "" && tx.ProductID != p.ProductID {
		return nil, invalid("apple: transaction is for %s, not %s", tx.ProductID, p.ProductID)
	}
	if AppleProductTier(tx.ProductID) == 0 {
		return nil, invalid("apple: %s is not a handle subscription", tx.ProductID)
	}
	return a.status(ctx, env, tx.OriginalTransactionID)
}

// Refresh implements Verifier.
func (a *Apple) Refresh(ctx context.Context, sub storage.Subscription) (*Verified, error) {
	env := sub.Environment
	if _, ok := a.cfg.BaseURLs[env]; !ok {
		env = AppleProduction
	}
	return a.status(ctx, env, sub.StoreID)
}

// OriginalTransactionID reads the subscription an App Store Server
// Notification V2 is about. The notification's signature is not checked:
// nothing in it is believed, it only says which subscription to ask Apple
// about again. Empty means the notification names none (a TEST notification).
func OriginalTransactionID(signedPayload string) (string, error) {
	var n struct {
		NotificationType string `json:"notificationType"`
		Data             struct {
			SignedTransactionInfo string `json:"signedTransactionInfo"`
		} `json:"data"`
	}
	if err := DecodeJWSPayload(signedPayload, &n); err != nil {
		return "", err
	}
	if n.Data.SignedTransactionInfo == "" {
		return "", nil
	}
	var tx appleTransaction
	if err := DecodeJWSPayload(n.Data.SignedTransactionInfo, &tx); err != nil {
		return "", err
	}
	return tx.OriginalTransactionID, nil
}

var _ Verifier = (*Apple)(nil)
