package handlers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// countingDestinations counts the reads that reach the store.
type countingDestinations struct {
	storage.P2PDestinationStore

	gets atomic.Int32
}

func (c *countingDestinations) GetDestination(ctx context.Context, ref string, now time.Time) (*storage.P2PDestination, error) {
	c.gets.Add(1)
	return c.P2PDestinationStore.GetDestination(ctx, ref, now)
}

func TestP2PMalformedReferenceNeverReachesStore(t *testing.T) {
	e := newP2PEnv(t, true)
	e.register("rosa")
	d := e.issue("rosa", 1000)
	pay := newPayment(t, d.script, d.sats)
	counter := &countingDestinations{P2PDestinationStore: e.srv.destinations}
	e.srv.destinations = counter

	bad := map[string]string{
		"uppercase": "ABCDEF0123456789ABCDEF0123456789",
		"31 chars":  "0123456789abcdef0123456789abcde",
		"33 chars":  "0123456789abcdef0123456789abcdef0",
		"non-hex":   "0123456789abcdef0123456789abcdeg",
		"operator":  `{"$ne":""}`,
		"empty":     "",
	}
	for name, ref := range bad {
		t.Run(name, func(t *testing.T) {
			w := e.post(beefPath, beefBody(t, pay.v1(t), ref, nil))
			assert.Equal(t, 404, w.Code)
			assert.Contains(t, w.Body.String(), "ERR_UNKNOWN_REFERENCE")
			w = e.post(rawPath, rawBody(t, rawHex(pay.child), ref, nil))
			assert.Equal(t, 404, w.Code)
			assert.Contains(t, w.Body.String(), "ERR_UNKNOWN_REFERENCE")
		})
	}
	require.Zero(t, counter.gets.Load(), "a malformed reference must not be looked up")

	// A well-formed reference still goes to the store.
	w := e.post(beefPath, beefBody(t, pay.v1(t), d.ref, nil))
	assert.Equal(t, 200, w.Code)
	assert.Positive(t, counter.gets.Load())
}
