package main

import (
	"context"
	"net/http"
	"time"
)

// feeReader is the one store query readiness runs.
type feeReader interface {
	GetServerDeliveryFee(ctx context.Context, messageBox string) (int, error)
}

// readyHandler is the TS server's GET /ready: 200 once the store answers a
// query, 503 while it does not.
func readyHandler(store feeReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, err := store.GetServerDeliveryFee(ctx, "notifications"); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"error","code":"ERR_NOT_READY","description":"Message Box dependencies are not ready."}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
}
