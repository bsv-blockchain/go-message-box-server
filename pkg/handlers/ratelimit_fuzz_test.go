package handlers

import (
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FuzzRateLimiterClientIP drives clientIP with the client-controlled inputs it
// keys buckets on: the connection address, X-Forwarded-For, a client-IP
// header, and the proxy settings that decide which of them to trust.
func FuzzRateLimiterClientIP(f *testing.F) {
	f.Add("10.0.0.1:1234", "", "", "", false, 1, false)
	f.Add("10.0.0.1:1234", "1.2.3.4", "9.9.9.9", "6.6.6.6", true, 1, false)
	f.Add("10.0.0.1:1234", "1.2.3.4, 5.6.7.8", "", "1.1.1.1", true, 2, false)
	f.Add("[::1]:443", "::1, 0:0:0:0:0:0:0:1", "", "", true, 1, false)
	f.Add("10.0.0.1:1", "", "", "  5.5.5.5\t", false, 1, true)
	f.Add("10.0.0.1:1", "1.2.3.4", "", "not-an-ip", true, 1, true)
	f.Add("garbage", "a,b,,c", "x", "y", true, 3, true)

	const header = "CF-Connecting-IP"

	f.Fuzz(func(t *testing.T, remoteAddr, xff, spoof, headerValue string, trustProxy bool, hops int, useHeader bool) {
		hops %= 8 // a realistic proxy depth; NewRateLimiter reads < 1 as 1
		clientIPHeader := ""
		if useHeader {
			clientIPHeader = header
		}
		l := NewRateLimiter(1, trustProxy, hops, clientIPHeader)

		ipFor := func(xff string) string {
			r := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
			r.RemoteAddr = remoteAddr
			if xff != "" {
				r.Header.Set("X-Forwarded-For", xff)
			}
			if headerValue != "" {
				r.Header.Set(header, headerValue)
			}
			return l.clientIP(r)
		}
		got := ipFor(xff)

		// The key is an address in canonical form, so it is bounded and one per
		// address, or else the connection's own.
		host, _, splitErr := net.SplitHostPort(remoteAddr)
		if ip := net.ParseIP(got); ip == nil || ip.String() != got {
			require.True(t, got == remoteAddr || (splitErr == nil && got == host),
				"clientIP = %q is neither a canonical IP nor the connection address %q", got, remoteAddr)
		}

		// A configured client-IP header that parses wins over everything else.
		if useHeader {
			if ip := net.ParseIP(strings.TrimSpace(headerValue)); ip != nil {
				assert.Equal(t, ip.String(), got)
				return
			}
		}

		// Without trustProxy, X-Forwarded-For is ignored entirely.
		if !trustProxy {
			assert.Equal(t, ipFor(""), got, "X-Forwarded-For moved the bucket with trustProxy off")
			return
		}

		// With it, entries a client prepends cannot move the bucket once the
		// proxies have appended theirs: only the entry hops from the right is read.
		effectiveHops := max(hops, 1)
		if xff != "" && len(strings.Split(xff, ",")) >= effectiveHops {
			assert.Equal(t, got, ipFor(spoof+","+xff), "a prepended X-Forwarded-For entry moved the bucket")
		}
	})
}
