// Package firebase delivers push notifications for new messages through
// Firebase Cloud Messaging (FCM) to the devices registered for a recipient.
package firebase

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	firebaseSDK "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

var (
	client     *messaging.Client
	clientOnce sync.Once
	errInit    error
)

// Config holds Firebase configuration.
type Config struct {
	ProjectID          string
	ServiceAccountJSON string
	ServiceAccountPath string
}

// Initialize sets up the Firebase Admin SDK.
// If ProjectID is empty, Firebase is disabled (not an error).
func Initialize(cfg Config) error {
	if cfg.ProjectID == "" {
		// Firebase not configured, disable silently
		// should we throw an error here?
		slog.Warn("skipping Firebase initialization ProjectID not provided for firebase config")
		return nil
	}

	clientOnce.Do(func() {
		ctx := context.Background()

		var opt option.ClientOption
		if cfg.ServiceAccountJSON != "" {
			opt = option.WithAuthCredentialsJSON(option.ServiceAccount, []byte(cfg.ServiceAccountJSON))
		} else if cfg.ServiceAccountPath != "" {
			opt = option.WithAuthCredentialsFile(option.ServiceAccount, cfg.ServiceAccountPath)
		} else {
			errInit = fmt.Errorf("firebase: no credentials provided (need FIREBASE_SERVICE_ACCOUNT_JSON or FIREBASE_SERVICE_ACCOUNT_PATH)")
			return
		}

		app, err := firebaseSDK.NewApp(ctx, &firebaseSDK.Config{
			ProjectID: cfg.ProjectID,
		}, opt)
		if err != nil {
			errInit = err
			return
		}

		client, err = app.Messaging(ctx)
		if err != nil {
			errInit = fmt.Errorf("firebase: failed to get messaging client: %w", err)
			return
		}
	})

	return errInit
}

// IsEnabled returns true if Firebase is configured and initialized.
func IsEnabled() bool {
	return client != nil
}

// Client returns the FCM messaging client (may be nil if not enabled)
func Client() *messaging.Client {
	return client
}
