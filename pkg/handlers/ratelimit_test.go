package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	clock := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	l := NewRateLimiter(2, false)
	l.now = func() time.Time { return clock }
	h := l.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	// serve runs one request against h and hands back the whole recorder, so
	// callers can assert on the 429 body as well as the status.
	serve := func(h http.Handler, remote, xff string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	hit := func(remote, xff string) int { return serve(h, remote, xff).Code }

	if hit("1.1.1.1:1000", "") != 204 || hit("1.1.1.1:2000", "") != 204 {
		t.Fatal("first two requests must pass")
	}
	over := serve(h, "1.1.1.1:3000", "")
	if over.Code != 429 {
		t.Fatalf("third request = %d, want 429", over.Code)
	}
	// The 429 body is the contract clients branch on, not just the status.
	if ct := over.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("429 Content-Type = %q, want application/json", ct)
	}
	var errBody ErrorResponse
	if err := json.Unmarshal(over.Body.Bytes(), &errBody); err != nil ||
		errBody.Status != "error" || errBody.Code != "ERR_RATE_LIMITED" {
		t.Fatalf("429 body = %s, err %v", over.Body.String(), err)
	}
	if hit("2.2.2.2:1000", "") != 204 {
		t.Fatal("another IP must pass")
	}
	// X-Forwarded-For is ignored unless the proxy is trusted.
	if got := hit("1.1.1.1:4000", "9.9.9.9"); got != 429 {
		t.Fatalf("spoofed XFF = %d, want 429", got)
	}
	clock = clock.Add(time.Minute)
	if hit("1.1.1.1:5000", "") != 204 {
		t.Fatal("new window must pass")
	}

	trusted := NewRateLimiter(1, true)
	trusted.now = func() time.Time { return clock }
	th := trusted.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	// perMinute is 1, so each case below is the first hit on its bucket (204) or
	// a repeat of an earlier one (429). The cases are chosen so that keying on
	// anything other than the leftmost X-Forwarded-For entry — RemoteAddr, or
	// the last entry — flips at least one expectation.
	for _, c := range []struct {
		name, remote, xff string
		want              int
	}{
		{"leftmost entry 9.9.9.9", "10.0.0.1:1", "9.9.9.9, 10.0.0.1", 204},
		{"same leftmost entry, fresh RemoteAddr and last entry", "10.0.0.2:1", "9.9.9.9, 10.0.0.2", 429},
		{"fresh leftmost entry, reused RemoteAddr and last entry", "10.0.0.1:1", "8.8.8.8, 10.0.0.1", 204},
		{"no header falls back to RemoteAddr", "10.0.0.3:1", "", 204},
		{"fallback repeats on the same RemoteAddr", "10.0.0.3:2", "", 429},
		{"unparseable header falls back to RemoteAddr", "10.0.0.4:1", "not-an-ip", 204},
		{"a different junk header shares that fallback bucket", "10.0.0.4:2", "junk, 10.0.0.4", 429},
		{"IPv6 loopback", "10.0.0.5:1", "::1", 204},
		{"the same IPv6 address spelled out", "10.0.0.5:2", "0:0:0:0:0:0:0:1", 429},
	} {
		if got := serve(th, c.remote, c.xff).Code; got != c.want {
			t.Fatalf("trusted %s = %d, want %d", c.name, got, c.want)
		}
	}

	off := NewRateLimiter(0, false)
	oh := off.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		oh.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 204 {
			t.Fatal("disabled limiter must pass everything")
		}
	}
}
