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
	l := NewRateLimiter(2, false, 1, "")
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

	trusted := NewRateLimiter(1, true, 1, "")
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
	twoHops := NewRateLimiter(1, true, 2, "")
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

	off := NewRateLimiter(0, false, 1, "")
	oh := off.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		oh.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 204 {
			t.Fatal("disabled limiter must pass everything")
		}
	}
}

// TestRateLimiter_ClientIPHeader covers CLIENT_IP_HEADER: when configured it
// takes precedence over trustProxy/hops, falls back to the existing logic on
// anything that is not exactly one parseable address, and is never consulted
// at all when unset — even if a client sends the header anyway.
func TestRateLimiter_ClientIPHeader(t *testing.T) {
	clock := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	// serve runs one request carrying RemoteAddr, an optional X-Forwarded-For,
	// and zero or more values for the header named by headerName (zero means
	// the header is absent; more than one exercises the duplicated-header case).
	serve := func(h http.Handler, remote, xff, headerName string, headerValues ...string) int {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		for _, v := range headerValues {
			r.Header.Add(headerName, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	newHandler := func(perMinute int, trustProxy bool, hops int, clientIPHeader string) http.Handler {
		l := NewRateLimiter(perMinute, trustProxy, hops, clientIPHeader)
		l.now = func() time.Time { return clock }
		return l.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	}

	t.Run("buckets keyed by the header", func(t *testing.T) {
		h := newHandler(1, false, 1, "CF-Connecting-IP")
		if got := serve(h, "10.0.1.1:1", "", "CF-Connecting-IP", "5.5.5.5"); got != 204 {
			t.Fatalf("first hit = %d, want 204", got)
		}
		// Same header value, a different RemoteAddr: still one bucket.
		if got := serve(h, "10.0.1.2:1", "", "CF-Connecting-IP", "5.5.5.5"); got != 429 {
			t.Fatalf("same header value, different RemoteAddr = %d, want 429", got)
		}
		// A different header value gets its own bucket, RemoteAddr aside.
		if got := serve(h, "10.0.1.1:1", "", "CF-Connecting-IP", "6.6.6.6"); got != 204 {
			t.Fatalf("different header value = %d, want 204", got)
		}
	})

	t.Run("surrounding whitespace is not part of the address", func(t *testing.T) {
		h := newHandler(1, false, 1, "CF-Connecting-IP")
		if got := serve(h, "10.0.1.3:1", "", "CF-Connecting-IP", "  5.5.5.6\t"); got != 204 {
			t.Fatalf("padded value = %d, want 204", got)
		}
		// Unpadded, from elsewhere: the same client, so the same bucket. Left
		// untrimmed the padded value would not parse and would have keyed on
		// RemoteAddr instead, leaving this request a bucket of its own.
		if got := serve(h, "10.0.1.4:1", "", "CF-Connecting-IP", "5.5.5.6"); got != 429 {
			t.Fatalf("same address unpadded = %d, want 429", got)
		}
	})

	t.Run("spoofed X-Forwarded-For ignored when the header is valid, even with trustProxy", func(t *testing.T) {
		h := newHandler(1, true, 1, "CF-Connecting-IP")
		if got := serve(h, "10.0.2.1:1", "9.9.9.9", "CF-Connecting-IP", "7.7.7.7"); got != 204 {
			t.Fatalf("first hit = %d, want 204", got)
		}
		// Same header value but a wildly different spoofed XFF and RemoteAddr:
		// still one bucket, because the header wins over trustProxy/hops.
		if got := serve(h, "10.0.2.2:1", "1.2.3.4, 8.8.8.8", "CF-Connecting-IP", "7.7.7.7"); got != 429 {
			t.Fatalf("header must win over trustProxy XFF = %d, want 429", got)
		}
	})

	t.Run("header absent falls back", func(t *testing.T) {
		untrusted := newHandler(1, false, 1, "CF-Connecting-IP")
		if got := serve(untrusted, "10.0.3.1:1", "", "CF-Connecting-IP"); got != 204 {
			t.Fatalf("first (RemoteAddr fallback) = %d, want 204", got)
		}
		if got := serve(untrusted, "10.0.3.1:2", "", "CF-Connecting-IP"); got != 429 {
			t.Fatalf("second on same RemoteAddr = %d, want 429", got)
		}

		trusted := newHandler(1, true, 1, "CF-Connecting-IP")
		if got := serve(trusted, "10.0.3.2:1", "9.9.9.9, 7.7.7.7", "CF-Connecting-IP"); got != 204 {
			t.Fatalf("first (XFF hop fallback) = %d, want 204", got)
		}
		if got := serve(trusted, "10.0.3.3:1", "1.1.1.1, 7.7.7.7", "CF-Connecting-IP"); got != 429 {
			t.Fatalf("second sharing the XFF hop = %d, want 429", got)
		}
	})

	t.Run("garbage header value falls back to RemoteAddr", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			values []string
		}{
			{"not an IP", []string{"not-an-ip"}},
			{"empty value", []string{""}},
			{"comma-separated list", []string{"1.1.1.1, 2.2.2.2"}},
			// Two different values, so a limiter that dropped the "exactly one"
			// rule and took whichever value it found first still keys both
			// requests below alike and fails the second assertion.
			{"header sent twice", []string{"5.5.5.5", "6.6.6.6"}},
		} {
			t.Run(c.name, func(t *testing.T) {
				h := newHandler(1, false, 1, "CF-Connecting-IP")
				// Two connections from different hosts carrying the same rejected
				// header. Both pass only if the key came from RemoteAddr: anything
				// that keyed on the header value — the raw string, or the first of
				// two — would put them in one bucket and refuse the second. Varying
				// the port alone would not tell the two apart, since SplitHostPort
				// drops it.
				if got := serve(h, "10.0.4.1:1", "", "CF-Connecting-IP", c.values...); got != 204 {
					t.Fatalf("first host = %d, want 204", got)
				}
				if got := serve(h, "10.0.4.2:1", "", "CF-Connecting-IP", c.values...); got != 204 {
					t.Fatalf("second host, same rejected header = %d, want 204", got)
				}
				// And the bucket the first request landed in is exactly the one a
				// request with no header at all lands in: the fallback is complete,
				// not a variant keyed on the header too.
				if got := serve(h, "10.0.4.1:2", "", "CF-Connecting-IP"); got != 429 {
					t.Fatalf("headerless repeat of the first host = %d, want 429", got)
				}
			})
		}
	})

	t.Run("IPv6 accepted, equivalent spellings share a bucket", func(t *testing.T) {
		h := newHandler(1, false, 1, "CF-Connecting-IP")
		if got := serve(h, "10.0.5.1:1", "", "CF-Connecting-IP", "::1"); got != 204 {
			t.Fatalf("first = %d, want 204", got)
		}
		if got := serve(h, "10.0.5.2:1", "", "CF-Connecting-IP", "0:0:0:0:0:0:0:1"); got != 429 {
			t.Fatalf("equivalent spelling = %d, want 429", got)
		}
	})

	t.Run("header name matches regardless of the case it is configured or sent in", func(t *testing.T) {
		h := newHandler(1, false, 1, "cf-connecting-ip")
		if got := serve(h, "10.0.6.1:1", "", "CF-Connecting-IP", "5.5.5.5"); got != 204 {
			t.Fatalf("first = %d, want 204", got)
		}
		if got := serve(h, "10.0.6.2:1", "", "Cf-Connecting-Ip", "5.5.5.5"); got != 429 {
			t.Fatalf("differently-cased header name = %d, want 429", got)
		}
	})

	t.Run("disabled limiter unaffected", func(t *testing.T) {
		h := newHandler(0, false, 1, "CF-Connecting-IP")
		for i := 0; i < 10; i++ {
			if got := serve(h, "10.0.7.1:1", "", "CF-Connecting-IP", "5.5.5.5"); got != 204 {
				t.Fatalf("disabled limiter must pass everything, got %d", got)
			}
		}
	})

	t.Run("header not configured: a client-sent CF-Connecting-IP is ignored entirely", func(t *testing.T) {
		h := newHandler(1, false, 1, "")
		if got := serve(h, "10.0.8.1:1", "", "CF-Connecting-IP", "5.5.5.5"); got != 204 {
			t.Fatalf("first = %d, want 204", got)
		}
		// Same header value, different RemoteAddr: must NOT share a bucket, since
		// the header is never consulted when it is not configured.
		if got := serve(h, "10.0.8.2:1", "", "CF-Connecting-IP", "5.5.5.5"); got != 204 {
			t.Fatalf("header ignored, different RemoteAddr = %d, want 204", got)
		}
	})
}
