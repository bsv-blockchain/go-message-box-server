package config

import (
	"fmt"
	"os"
	"strconv"
)

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
