package profilecert

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert/certtest"
)

// FuzzParse feeds Parse the request bodies an unauthenticated PUT /api/handle
// can carry. Whatever the body, Parse must not panic and must fail only with
// its documented errors; whatever it accepts must satisfy every rule the
// registry relies on, and its canonical JSON must parse back to the same
// profile, since that JSON is what the registry stores and serves.
func FuzzParse(f *testing.F) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	key, _ := ec.PrivateKeyFromBytes([]byte(strings.Repeat("\x01", 32)))

	valid := certtest.Profile(f, key, "deggen", domain, now, nil)
	for _, seed := range [][]byte{
		valid,
		certtest.Profile(f, key, "deggen", domain, now.Add(-time.Hour), map[string]string{"released": "true"}),
		certtest.Profile(f, key, "deggen", "other.example", now, nil),
		certtest.Profile(f, key, "deggen", domain, now.Add(time.Hour), nil),
		valid[:len(valid)/2],
		[]byte(`{}`),
		[]byte(`null`),
		[]byte(`{"type":"` + Type + `"}`),
		[]byte(`{"type":"` + Type + `","serialNumber":"","subject":null,"certifier":null}`),
		[]byte(`{"revocationOutpoint":null,"fields":{"paymail":"Deggen@Example.com"}}`),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		p, err := Parse(context.Background(), body, domain, now)
		if err != nil {
			require.Nil(t, p)
			require.True(t, errors.Is(err, ErrInvalid) || errors.Is(err, ErrWrongDomain),
				"undocumented error: %v", err)
			return
		}

		require.NotNil(t, p)
		assert.Equal(t, p.Handle+"@"+domain, p.Paymail)
		assert.Equal(t, strings.ToLower(p.Paymail), p.Paymail)
		assert.NotEmpty(t, p.Handle)
		assert.NotEmpty(t, p.IdentityKey)
		assert.LessOrEqual(t, len(p.JSON), MaxBody)
		assert.Equal(t, time.UTC, p.IssuedAt.Location())
		assert.False(t, p.IssuedAt.After(now.Add(MaxClockSkew)), "issuedAt %v beyond the clock-skew bound", p.IssuedAt)
		assert.Equal(t, p.IssuedAt, p.IssuedAt.Truncate(time.Millisecond))

		again, err := Parse(context.Background(), []byte(p.JSON), domain, now)
		require.NoError(t, err, "canonical JSON no longer parses")
		assert.Equal(t, p, again, "canonical JSON does not round-trip")
	})
}
