package handlers

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimiter is a per-IP fixed-window limiter for the unauthenticated lookup
// routes. It is in-memory, so each replica counts on its own.
type RateLimiter struct {
	perMinute  int
	trustProxy bool
	now        func() time.Time

	mu     sync.Mutex
	window int64 // unix minute the counts belong to
	counts map[string]int
}

// NewRateLimiter returns a limiter; perMinute <= 0 disables it.
func NewRateLimiter(perMinute int, trustProxy bool) *RateLimiter {
	return &RateLimiter{perMinute: perMinute, trustProxy: trustProxy, now: time.Now, counts: map[string]int{}}
}

func (l *RateLimiter) clientIP(r *http.Request) string {
	if l.trustProxy {
		// The header is client-controlled and becomes a map key, so it is only
		// honoured when it parses as an address: that caps the key at 45 bytes
		// and folds the textual forms of one IPv6 address into one bucket.
		// Anything else falls through to the connection address.
		if first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); first != "" {
			if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (l *RateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Dropping the whole map each minute is the sweep: memory is bounded by one
	// minute of distinct clients.
	if w := l.now().Unix() / 60; w != l.window {
		l.window, l.counts = w, map[string]int{}
	}
	l.counts[ip]++
	return l.counts[ip] <= l.perMinute
}

// Wrap applies the limit to next.
func (l *RateLimiter) Wrap(next http.Handler) http.Handler {
	if l.perMinute <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(l.clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "ERR_RATE_LIMITED", "Too many requests; retry in a minute.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
