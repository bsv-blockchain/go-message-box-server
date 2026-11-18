package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type feeFunc func() error

func (f feeFunc) GetServerDeliveryFee(context.Context, string) (int, error) { return 0, f() }

func TestReadyHandler(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"store answers", nil, 200, `{"status":"ready"}`},
		{"store down", errors.New("down"), 503, `{"status":"error","code":"ERR_NOT_READY","description":"Message Box dependencies are not ready."}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			readyHandler(feeFunc(func() error { return c.err })).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ready", nil))
			if w.Code != c.status || w.Body.String() != c.body || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("got %d %s (Cache-Control %q), want %d %s", w.Code, w.Body, w.Header().Get("Cache-Control"), c.status, c.body)
			}
		})
	}
}
