package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bsv-blockchain/go-bsv-middleware/pkg/middleware"
	_ "github.com/bsv-blockchain/go-message-box-server/docs"
	"github.com/bsv-blockchain/go-message-box-server/internal/firebase"
	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/config"
	"github.com/bsv-blockchain/go-message-box-server/pkg/handlers"
	mbstorage "github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/mongostore"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/sqlstore"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/defs"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/services"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/storage"
	toolboxwallet "github.com/bsv-blockchain/go-wallet-toolbox/pkg/wallet"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wdk"
	httpSwagger "github.com/swaggo/http-swagger/v2"
)

// @title           MessageBox Server API
// @version         1.0.0
// @description     API for message delivery, retrieval, acknowledgement and permissions. Uses BRC-31/BRC-104 mutual authentication.
// @host      localhost:8080
// @BasePath  /

// @securityDefinitions.apikey  BSVAuth
// @in header
// @name x-bsv-auth-identity-key
// @description BRC-31/BRC-104 mutual authentication. Requires multiple x-bsv-auth-* headers (identity-key, nonce, signature, etc.)

// openStore builds the storage backend selected by STORAGE_BACKEND.
func openStore(cfg *config.Config) (mbstorage.Store, error) {
	switch cfg.StorageBackend {
	case "sql", "":
		return sqlstore.New(cfg.DBDriver, cfg.DBSource)
	case "mongo":
		return mongostore.New(context.Background(), cfg.MongoURI, cfg.MongoDatabase)
	default:
		return nil, fmt.Errorf("unknown STORAGE_BACKEND %q (want \"sql\" or \"mongo\")", cfg.StorageBackend)
	}
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if cfg.NodeEnv == "development" {
		logger.Enable()
	}

	// Open the storage backend and bring its schema up to date
	store, err := openStore(cfg)
	if err != nil {
		slog.Error("failed to open storage backend", "backend", cfg.StorageBackend, "error", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.EnsureSchema(context.Background()); err != nil {
		slog.Error("failed to prepare storage schema", "error", err)
		os.Exit(1)
	}

	// initalize firebase
	if err := firebase.Initialize(firebase.Config{
		ProjectID:          cfg.FirebaseProjectID,
		ServiceAccountJSON: cfg.FirebaseServiceAccountJSON,
		ServiceAccountPath: cfg.FirebaseServiceAccountPath,
	}); err != nil {
		slog.Warn("Firebase initialization failed, FCM disabled", "error", err)
	} else if firebase.IsEnabled() {
		logger.Log("Firebase initialized successfully")
	}

	// Create production wallet using go-wallet-toolbox
	w, walletCleanup, err := createWallet(cfg)
	if err != nil {
		slog.Error("failed to create wallet", "error", err)
		os.Exit(1)
	}
	defer walletCleanup()

	srv := handlers.NewServer(store, w)

	// Build router
	mux := http.NewServeMux()

	prefix := cfg.RoutingPrefix

	// All routes require auth (postAuth in the original)
	mux.HandleFunc("POST "+prefix+"/sendMessage", srv.SendMessage)
	mux.HandleFunc("POST "+prefix+"/listMessages", srv.ListMessages)
	mux.HandleFunc("POST "+prefix+"/acknowledgeMessage", srv.AcknowledgeMessage)
	mux.HandleFunc("POST "+prefix+"/registerDevice", srv.RegisterDevice)
	mux.HandleFunc("GET "+prefix+"/devices", srv.ListDevices)
	mux.HandleFunc("POST "+prefix+"/permissions/set", srv.SetPermission)
	mux.HandleFunc("GET "+prefix+"/permissions/get", srv.GetPermission)
	mux.HandleFunc("GET "+prefix+"/permissions/list", srv.ListPermissions)
	mux.HandleFunc("GET "+prefix+"/permissions/quote", srv.GetQuote)

	// Auth middleware
	authMiddleware := middleware.NewAuth(w)

	// Payment middleware (returns 0 for now, matching the original)
	paymentMiddleware := middleware.NewPayment(w, middleware.WithRequestPriceCalculator(func(r *http.Request) (int, error) {
		return 0, nil
	}))

	// create root mux for swagger to avoid auth
	rootMux := http.NewServeMux()
	rootMux.Handle("GET /swagger/", httpSwagger.Handler(
		httpSwagger.URL("/swagger/doc.json"),
	))

	rootMux.Handle("/", authMiddleware.HTTPHandler(
		paymentMiddleware.HTTPHandler(mux),
	))

	// Stack: CORS -> rootMux -> Auth -> Payment -> Routes
	handler := &corsHandler{
		next: rootMux,
	}

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Log("MessageBox listening", "port", cfg.Port)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Log("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}

func createWallet(cfg *config.Config) (sdk.Interface, func(), error) {
	network := defs.NetworkMainnet
	if cfg.BSVNetwork == "testnet" {
		network = defs.NetworkTestnet
	}

	if cfg.WalletStorageURL != "" {
		return createWalletWithRemoteStorage(cfg, network)
	}

	return createWalletWithLocalStorage(cfg, network)
}

func createWalletWithRemoteStorage(cfg *config.Config, network defs.BSVNetwork) (sdk.Interface, func(), error) {
	logger.Log("Initializing wallet with remote storage", "url", cfg.WalletStorageURL)

	key, err := ec.PrivateKeyFromHex(cfg.ServerPrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse server private key: %w", err)
	}

	protoWallet, err := sdk.NewCompletedProtoWallet(key)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create proto wallet: %w", err)
	}

	storageClient, storageCleanup, err := storage.NewClient(cfg.WalletStorageURL, protoWallet)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create remote storage client: %w", err)
	}

	w, err := toolboxwallet.New(network, key, storageClient)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create wallet with remote storage: %w", err)
	}

	// check if storage is up and running before continuing
	if _, err := storageClient.MakeAvailable(context.Background()); err != nil {
		storageCleanup()
		return nil, nil, fmt.Errorf("failed to connect to remote storage: %w", err)
	}

	logger.Log("Wallet initialized successfully with remote storage")

	return w, storageCleanup, nil
}

func createWalletWithLocalStorage(cfg *config.Config, network defs.BSVNetwork) (sdk.Interface, func(), error) {
	logger.Log("Initializing wallet with local SQLite storage")

	svcConfig := defs.DefaultServicesConfig(network)
	walletServices := services.New(slog.Default(), svcConfig)

	// Use local SQLite storage
	dbConfig := defs.DefaultDBConfig()
	dbConfig.SQLite.ConnectionString = "wallet-storage.sqlite"

	activeStorage, err := storage.NewGORMProvider(network, walletServices,
		storage.WithDBConfig(dbConfig),
		storage.WithLogger(slog.Default()),
		storage.WithBackgroundBroadcasterContext(context.Background()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create local storage: %w", err)
	}

	storageIdentityKey, err := wdk.IdentityKey(cfg.ServerPrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to derive storage identity key: %w", err)
	}

	_, err = activeStorage.Migrate(context.Background(), "messagebox-storage", storageIdentityKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to migrate storage: %w", err)
	}

	w, err := toolboxwallet.New(network, cfg.ServerPrivateKey, activeStorage)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create wallet: %w", err)
	}

	logger.Log("Wallet initialized successfully with local storage")

	return w, func() {
		activeStorage.Stop()
	}, nil
}

type corsHandler struct {
	next http.Handler
}

func (h *corsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Allow-Methods", "*")
	w.Header().Set("Access-Control-Expose-Headers", "*")
	w.Header().Set("Access-Control-Allow-Private-Network", "true")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	h.next.ServeHTTP(w, r)
}
