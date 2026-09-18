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

func getEnvInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v >= 0 {
		return v
	}
	return fallback
}
