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
	l := NewRateLimiter(2, false, 1)
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

	trusted := NewRateLimiter(1, true, 1)
	trusted.now = func() time.Time { return clock }
	th := trusted.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	// perMinute is 1, so each case below is the first hit on its bucket (204) or
	// a repeat of an earlier one (429). One proxy is trusted, so the bucket is
	// the last X-Forwarded-For entry — the address that proxy appended. Every
	// case is chosen so that keying on the leftmost entry instead, which is
	// whatever the client chose to send, flips the expectation.
	for _, c := range []struct {
		name, remote, xff string
		want              int
	}{
		{"last entry 7.7.7.7", "10.0.0.1:1", "9.9.9.9, 7.7.7.7", 204},
		// The bypass: a fresh leftmost entry every request would be a fresh
		// bucket every request, and the limiter would never refuse anything.
		{"fresh leftmost entry, same client behind the proxy", "10.0.0.1:2", "8.8.8.8, 7.7.7.7", 429},
		{"another leftmost entry, still that client", "10.0.0.1:3", "1.2.3.4, 5.6.7.8, 7.7.7.7", 429},
		// The other half: naming a victim must not spend the victim's budget.
		{"a client naming 7.7.7.7 leftmost has its own bucket", "10.0.0.1:4", "7.7.7.7, 6.6.6.6", 204},
		{"no header falls back to RemoteAddr", "10.0.0.3:1", "", 204},
		{"fallback repeats on the same RemoteAddr", "10.0.0.3:2", "", 429},
		{"unparseable last entry falls back to RemoteAddr", "10.0.0.4:1", "not-an-ip", 204},
		{"a different junk header shares that fallback bucket", "10.0.0.4:2", "10.0.0.4, junk", 429},
		{"IPv6 loopback", "10.0.0.5:1", "::1", 204},
		{"the same IPv6 address spelled out", "10.0.0.5:2", "0:0:0:0:0:0:0:1", 429},
	} {
		if got := serve(th, c.remote, c.xff).Code; got != c.want {
			t.Fatalf("trusted %s = %d, want %d", c.name, got, c.want)
		}
	}

	// Two proxies of our own: the client is two entries from the right, and the
	// entry the nearer proxy appended is our own load balancer, not a client.
	twoHops := NewRateLimiter(1, true, 2)
	twoHops.now = func() time.Time { return clock }
	tw := twoHops.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for _, c := range []struct {
		name, remote, xff string
		want              int
	}{
		{"second from the right is the client", "10.0.0.6:1", "9.9.9.9, 7.7.7.7, 172.16.0.1", 204},
		{"same client, different spoofed prefix", "10.0.0.6:2", "1.1.1.9, 7.7.7.7, 172.16.0.1", 429},
		{"a different client behind the same proxies", "10.0.0.6:3", "9.9.9.9, 7.7.7.8, 172.16.0.1", 204},
		// Too few entries to contain a trusted hop: fall back rather than trust
		// the one entry a client could have written itself.
		{"one entry with two hops falls back", "10.0.0.7:1", "7.7.7.7", 204},
		{"and that fallback is the connection address", "10.0.0.7:2", "9.9.9.9", 429},
	} {
		if got := serve(tw, c.remote, c.xff).Code; got != c.want {
			t.Fatalf("two hops %s = %d, want %d", c.name, got, c.want)
		}
	}

	off := NewRateLimiter(0, false, 1)
	oh := off.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		oh.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 204 {
			t.Fatal("disabled limiter must pass everything")
		}
	}
}
