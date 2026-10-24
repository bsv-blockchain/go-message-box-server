package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// maxCooldownDays caps HANDLE_COOLDOWN_DAYS: time.Duration is int64
// nanoseconds, so beyond ~106751 days the multiply below wraps negative and a
// negative cooldown would silently disable the guard.
const maxCooldownDays = 3650

// Config holds the application configuration loaded from environment variables.
type Config struct {
	NodeEnv          string
	Port             int
	RoutingPrefix    string
	ServerPrivateKey string
	EnableWebsockets bool

	// Storage
	StorageBackend string // "sql" or "mongo"

	// SQL storage backend
	DBDriver string // "sqlite3" or "postgres"
	DBSource string // DSN or file path

	// Mongo storage backend
	MongoURI      string
	MongoDatabase string

	// Firebase (optional)
	FirebaseProjectID          string
	FirebaseServiceAccountJSON string
	FirebaseServiceAccountPath string

	// Wallet
	WalletStorageURL string
	BSVNetwork       string

	// Paymail profile lookup (optional)
	PaymailDomain     string
	PaymailHost       string
	HandleCooldown    time.Duration
	AdminIdentityKeys []string
	LookupRatePerMin  int
	TrustProxy        bool
	TrustedProxyHops  int
	// ClientIPHeader, when set, names a single-valued header a proxy sets to the
	// real client address (e.g. "CF-Connecting-IP"); it takes precedence over
	// TrustProxy/TrustedProxyHops in RateLimiter.clientIP. Only set it once the
	// origin is reachable exclusively through the proxy that sets the header:
	// anywhere it can be sent directly, a fresh value per request is a fresh
	// bucket per request and the limit stops applying at all — see the caveat on
	// handlers.RateLimiter and README.md's Configuration section. It is not an
	// authentication signal.
	ClientIPHeader string

	// Paid handles (optional; needs the paymail profile lookup). With
	// HandlePurchasesRequired every handle the wallet does not generate for
	// free needs a verified store subscription. A store left unconfigured is
	// refused, never waved through.
	HandlePurchasesRequired bool
	HandleGrace             time.Duration
	Apple                   AppleStoreConfig
	Google                  GooglePlayConfig

	// POST /listMessages pagination bounds. Defaults match the TS reference
	// server's "standard" resource profile. -1 means no cap on any of the four,
	// including ListDefaultLimit: handlers.parseListPagination substitutes the
	// JS safe-integer maximum for a -1 default, mirroring the TS reference
	// server exactly.
	ListDefaultLimit     int
	ListMaxLimit         int
	ListMaxOffset        int
	ListMaxResponseBytes int
}

// AppleStoreConfig is an App Store Connect in-app purchase key. Configured
// reports whether App Store purchases are accepted at all.
type AppleStoreConfig struct {
	IssuerID       string
	KeyID          string
	PrivateKey     string // PEM contents of the .p8 file
	PrivateKeyPath string // or a path to it
	BundleID       string
	Environments   []string // Production, Sandbox; tried in order
}

// Configured reports whether App Store purchases are accepted.
func (a AppleStoreConfig) Configured() bool { return a.BundleID != "" }

// GooglePlayConfig is a Play Developer API service account and the Pub/Sub
// push authentication for real-time developer notifications.
type GooglePlayConfig struct {
	PackageName          string
	ServiceAccountJSON   string
	ServiceAccountPath   string
	AllowTestPurchases   bool
	PubSubAudience       string // the push subscription's OIDC audience; empty skips the check
	PubSubServiceAccount string // the push subscription's service account email
}

// Configured reports whether Google Play purchases are accepted.
func (g GooglePlayConfig) Configured() bool { return g.PackageName != "" }

// maxGraceDays caps HANDLE_GRACE_DAYS for the same reason as maxCooldownDays.
const maxGraceDays = 3650

// loadPurchases reads the paid-handle settings.
func loadPurchases(cfg *Config) error {
	cfg.HandlePurchasesRequired = strings.EqualFold(strings.TrimSpace(os.Getenv("HANDLE_PURCHASES_REQUIRED")), "true")
	graceDays := min(max(getEnvInt("HANDLE_GRACE_DAYS", 14), 0), maxGraceDays)
	cfg.HandleGrace = time.Duration(graceDays) * 24 * time.Hour

	cfg.Apple = AppleStoreConfig{
		IssuerID:       strings.TrimSpace(os.Getenv("APPLE_ISSUER_ID")),
		KeyID:          strings.TrimSpace(os.Getenv("APPLE_KEY_ID")),
		PrivateKey:     os.Getenv("APPLE_PRIVATE_KEY"),
		PrivateKeyPath: strings.TrimSpace(os.Getenv("APPLE_PRIVATE_KEY_PATH")),
		BundleID:       strings.TrimSpace(os.Getenv("APPLE_BUNDLE_ID")),
	}
	for _, env := range strings.Split(getEnv("APPLE_ENVIRONMENTS", "Production"), ",") {
		switch env = strings.TrimSpace(env); env {
		case "":
		case "Production", "Sandbox":
			cfg.Apple.Environments = append(cfg.Apple.Environments, env)
		default:
			return fmt.Errorf("APPLE_ENVIRONMENTS entry %q is not Production or Sandbox", env)
		}
	}
	if a := cfg.Apple; a.Configured() {
		if a.IssuerID == "" || a.KeyID == "" || (a.PrivateKey == "" && a.PrivateKeyPath == "") {
			return fmt.Errorf("APPLE_BUNDLE_ID needs APPLE_ISSUER_ID, APPLE_KEY_ID and APPLE_PRIVATE_KEY or APPLE_PRIVATE_KEY_PATH")
		}
	}

	cfg.Google = GooglePlayConfig{
		PackageName:          strings.TrimSpace(os.Getenv("GOOGLE_PLAY_PACKAGE_NAME")),
		ServiceAccountJSON:   os.Getenv("GOOGLE_PLAY_SERVICE_ACCOUNT_JSON"),
		ServiceAccountPath:   strings.TrimSpace(os.Getenv("GOOGLE_PLAY_SERVICE_ACCOUNT_PATH")),
		AllowTestPurchases:   strings.EqualFold(strings.TrimSpace(os.Getenv("GOOGLE_PLAY_ALLOW_TEST_PURCHASES")), "true"),
		PubSubAudience:       strings.TrimSpace(os.Getenv("GOOGLE_PUBSUB_AUDIENCE")),
		PubSubServiceAccount: strings.TrimSpace(os.Getenv("GOOGLE_PUBSUB_SERVICE_ACCOUNT")),
	}
	if g := cfg.Google; g.Configured() && g.ServiceAccountJSON == "" && g.ServiceAccountPath == "" {
		return fmt.Errorf("GOOGLE_PLAY_PACKAGE_NAME needs GOOGLE_PLAY_SERVICE_ACCOUNT_JSON or GOOGLE_PLAY_SERVICE_ACCOUNT_PATH")
	}

	if cfg.HandlePurchasesRequired {
		if cfg.PaymailDomain == "" {
			return fmt.Errorf("HANDLE_PURCHASES_REQUIRED needs the handle registry (PAYMAIL_DOMAIN)")
		}
		if !cfg.Apple.Configured() && !cfg.Google.Configured() {
			return fmt.Errorf("HANDLE_PURCHASES_REQUIRED needs APPLE_BUNDLE_ID or GOOGLE_PLAY_PACKAGE_NAME: with neither store configured no paid handle could ever be registered")
		}
	}
	return nil
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	cfg := &Config{
		NodeEnv:          getEnv("NODE_ENV", "development"),
		RoutingPrefix:    getEnv("ROUTING_PREFIX", ""),
		ServerPrivateKey: os.Getenv("SERVER_PRIVATE_KEY"),
		EnableWebsockets: getEnv("ENABLE_WEBSOCKETS", "true") == "true",
		StorageBackend:   getEnv("STORAGE_BACKEND", "sql"),
		MongoURI:         getEnv("MONGO_URI", "mongodb://localhost:27017"),
		MongoDatabase:    getEnv("MONGO_DATABASE", "messagebox"),

		DBDriver:         getEnv("DB_DRIVER", "sqlite3"),
		DBSource:         getEnv("DB_SOURCE", "messagebox.db"),
		BSVNetwork:       getEnv("BSV_NETWORK", "mainnet"),
		WalletStorageURL: os.Getenv("WALLET_STORAGE_URL"),

		FirebaseProjectID:          os.Getenv("FIREBASE_PROJECT_ID"),
		FirebaseServiceAccountJSON: os.Getenv("FIREBASE_SERVICE_ACCOUNT_JSON"),
		FirebaseServiceAccountPath: os.Getenv("FIREBASE_SERVICE_ACCOUNT_PATH"),
	}

	if cfg.ServerPrivateKey == "" {
		return nil, fmt.Errorf("SERVER_PRIVATE_KEY is not defined in environment variables")
	}

	cfg.PaymailDomain = strings.ToLower(strings.TrimSpace(os.Getenv("PAYMAIL_DOMAIN")))
	cfg.PaymailHost = strings.TrimRight(strings.TrimSpace(os.Getenv("PAYMAIL_HOST")), "/")
	if cfg.PaymailDomain != "" {
		// A scheme, port, user part or space would leave every certificate's
		// paymail field unmatchable and the well-known document malformed.
		if strings.ContainsAny(cfg.PaymailDomain, "/:@ \t") {
			return nil, fmt.Errorf("PAYMAIL_DOMAIN must be a bare domain such as example.com, got %q", cfg.PaymailDomain)
		}
		if cfg.PaymailHost == "" {
			return nil, fmt.Errorf("PAYMAIL_HOST is required when PAYMAIL_DOMAIN is set")
		}
	}
	cooldownDays := getEnvInt("HANDLE_COOLDOWN_DAYS", 30)
	if cooldownDays > maxCooldownDays {
		cooldownDays = maxCooldownDays
	}
	cfg.HandleCooldown = time.Duration(cooldownDays) * 24 * time.Hour
	cfg.LookupRatePerMin = getEnvInt("LOOKUP_RATE_PER_MIN", 60)
	cfg.TrustProxy = strings.EqualFold(strings.TrimSpace(os.Getenv("TRUST_PROXY")), "true")
	// How many proxies of your own sit in front of this process. Each appends
	// the address it accepted the connection from to X-Forwarded-For, so the
	// client's own address is that many entries from the right — everything to
	// the left of it is whatever the client chose to send. Counting from the
	// left instead would let a client pick its own rate-limit bucket.
	cfg.TrustedProxyHops = getEnvInt("TRUSTED_PROXY_HOPS", 1)
	if cfg.TrustedProxyHops < 1 {
		cfg.TrustedProxyHops = 1
	}
	// Stored as trimmed but not canonicalised: whoever reads it (RateLimiter.
	// clientIP) looks it up with http.Header.Get/Values, which canonicalises the
	// name it is given, so the case written here never has to match the case a
	// proxy sends the header in.
	cfg.ClientIPHeader = strings.TrimSpace(os.Getenv("CLIENT_IP_HEADER"))
	if cfg.ClientIPHeader != "" && !isValidHTTPHeaderName(cfg.ClientIPHeader) {
		return nil, fmt.Errorf("CLIENT_IP_HEADER %q is not a valid HTTP header field name", cfg.ClientIPHeader)
	}
	seenAdmin := make(map[string]bool)
	for _, k := range strings.Split(os.Getenv("ADMIN_IDENTITY_KEYS"), ",") {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if !isCompressedPubKeyHex(k) {
			return nil, fmt.Errorf("ADMIN_IDENTITY_KEYS entry %q is not a compressed public key in hex", k)
		}
		if seenAdmin[k] {
			continue
		}
		seenAdmin[k] = true
		cfg.AdminIdentityKeys = append(cfg.AdminIdentityKeys, k)
	}

	if err := loadPurchases(cfg); err != nil {
		return nil, err
	}

	listDefaultLimit, err := getEnvListBound("LIST_DEFAULT_LIMIT", 1000)
	if err != nil {
		return nil, err
	}
	listMaxLimit, err := getEnvListBound("LIST_MAX_LIMIT", 1000)
	if err != nil {
		return nil, err
	}
	listMaxOffset, err := getEnvListBound("LIST_MAX_OFFSET", 100_000)
	if err != nil {
		return nil, err
	}
	listMaxResponseBytes, err := getEnvListBound("LIST_MAX_RESPONSE_BYTES", 8*1024*1024)
	if err != nil {
		return nil, err
	}
	if listMaxLimit != -1 && listDefaultLimit > listMaxLimit {
		return nil, fmt.Errorf("LIST_DEFAULT_LIMIT (%d) must not exceed LIST_MAX_LIMIT (%d)", listDefaultLimit, listMaxLimit)
	}
	cfg.ListDefaultLimit = listDefaultLimit
	cfg.ListMaxLimit = listMaxLimit
	cfg.ListMaxOffset = listMaxOffset
	cfg.ListMaxResponseBytes = listMaxResponseBytes

	port := getEnv("PORT", "")
	if port == "" {
		port = getEnv("HTTP_PORT", "")
	}
	if port == "" {
		if cfg.NodeEnv != "development" {
			cfg.Port = 3000
		} else {
			cfg.Port = 8080
		}
	} else {
		p, err := strconv.Atoi(port)
		if err != nil || p <= 0 {
			cfg.Port = 8080
		} else {
			cfg.Port = p
		}
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// isCompressedPubKeyHex reports whether s is a 33-byte compressed public key in
// lowercase hex, the form a BRC-104 identity key takes. Anything else can never
// match a caller, so it is a typo rather than an admin.
func isCompressedPubKeyHex(s string) bool {
	if len(s) != 66 || (!strings.HasPrefix(s, "02") && !strings.HasPrefix(s, "03")) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// isValidHTTPHeaderName reports whether s is a syntactically valid HTTP header
// field name: an RFC 7230 token, one or more tchars. Anything else could never
// arrive as an actual header, so CLIENT_IP_HEADER would silently do nothing —
// the same reasoning that makes ADMIN_IDENTITY_KEYS and PAYMAIL_DOMAIN fail
// fast on nonsense rather than accept it.
func isValidHTTPHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTokenChar(s[i]) {
			return false
		}
	}
	return true
}

// isTokenChar reports whether b is an RFC 7230 tchar.
func isTokenChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	switch b {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

func getEnvInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v >= 0 {
		return v
	}
	return fallback
}

// getEnvListBound reads a POST /listMessages pagination bound: a positive
// integer, or -1 meaning no cap, mirroring the TS reference server's resource
// profile knobs (LIST_MAX_LIMIT, LIST_MAX_OFFSET and LIST_MAX_RESPONSE_BYTES
// all accept -1 there). Anything else is a startup error rather than a
// silently-applied fallback: a typo here would otherwise silently remove the
// limit entirely.
func getEnvListBound(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || (v != -1 && v <= 0) {
		return 0, fmt.Errorf("%s must be -1 or a positive integer, got %q", key, raw)
	}
	return v, nil
}
