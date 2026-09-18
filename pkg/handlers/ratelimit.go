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
//
// clientIPHeader, when set, takes precedence over trustProxy/hops (see
// clientIP): a header a proxy sets untouched is a better signal than a hop
// count guessed against X-Forwarded-For, and the two exist for different
// topologies — trustProxy for a proxy that appends to X-Forwarded-For, the
// header for one that doesn't (see cmd/server and README.md for the Traefik
// case this was added for). Trusting the header is only sound when every
// request that reaches this process passed through the proxy that sets it. If
// this process, or a load balancer in front of it, is also reachable directly,
// a client can set the header itself — and since every distinct value is its
// own bucket, one sending a fresh value per request lands in a fresh bucket per
// request: it is not limited at all, and counts gains an entry per request
// until the window rolls. That is weaker than leaving clientIPHeader empty,
// where the same traffic shares the one bucket its connection address keys, so
// only set it once the origin refuses connections that did not come through
// the proxy. It bounds fairness between clients that do arrive through the
// proxy; it is never an authentication signal.
type RateLimiter struct {
	perMinute      int
	trustProxy     bool
	hops           int    // trusted proxies in front of this process, counted from the right
	clientIPHeader string // single-valued header trusted ahead of trustProxy/hops; "" disables it
	now            func() time.Time

	mu     sync.Mutex
	window int64 // unix minute the counts belong to
	counts map[string]int
}

// NewRateLimiter returns a limiter; perMinute <= 0 disables it. hops is how
// many trusted proxies sit in front of this process when trustProxy is set;
// fewer than one is read as one. clientIPHeader, when non-empty, names a
// single-valued header (e.g. "CF-Connecting-IP") that wins over trustProxy/
// hops whenever it is present and parses as an address — see the caveat on
// RateLimiter above.
func NewRateLimiter(perMinute int, trustProxy bool, hops int, clientIPHeader string) *RateLimiter {
	if hops < 1 {
		hops = 1
	}
	return &RateLimiter{
		perMinute:      perMinute,
		trustProxy:     trustProxy,
		hops:           hops,
		clientIPHeader: clientIPHeader,
		now:            time.Now,
		counts:         map[string]int{},
	}
}

func (l *RateLimiter) clientIP(r *http.Request) string {
	if l.clientIPHeader != "" {
		// Header.Values canonicalises the name we were configured with, so
		// neither the case CLIENT_IP_HEADER was set in nor the case the proxy
		// happens to send it in matters. Values, not Get: a proxy that sets this
		// header sets it exactly once, so a request carrying it more than once
		// (a client appending its own, say) is treated as unparseable and falls
		// through below — the same as no header at all.
		if vs := r.Header.Values(l.clientIPHeader); len(vs) == 1 {
			if ip := net.ParseIP(strings.TrimSpace(vs[0])); ip != nil {
				return ip.String()
			}
		}
	}
	if l.trustProxy {
		// Proxies append to X-Forwarded-For, they do not rewrite it: whatever the
		// client sent stays leftmost and each hop adds the address it accepted the
		// connection from. So the only entry this server has any reason to believe
		// is hops from the right — taking the leftmost would let every request
		// nominate its own bucket, and let one client exhaust another's.
		//
		// The value becomes a map key, so it is only honoured when it parses as an
		// address: that caps the key at 45 bytes and folds the textual forms of one
		// IPv6 address into one bucket. Anything else, including a header with
		// fewer entries than there are hops, falls through to the connection
		// address — which is at worst the nearest proxy, so the limit over-counts
		// rather than letting traffic past.
		if parts := strings.Split(r.Header.Get("X-Forwarded-For"), ","); len(parts) >= l.hops {
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-l.hops])); ip != nil {
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
