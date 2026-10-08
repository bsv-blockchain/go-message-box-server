package handlers

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// FuzzBoundedInteger checks boundedInteger, the port of the TS reference
// server's isBoundedInteger that gates limit and offset on POST /listMessages,
// against an independent statement of the rule: a whole number within
// JavaScript's safe-integer range, at least minValue and, unless maxValue is
// -1, at most maxValue. It must agree on every float64, including NaN, the
// infinities, and values beyond int64 whose conversion Go leaves to the
// platform.
func FuzzBoundedInteger(f *testing.F) {
	for _, seed := range []struct {
		v        float64
		min, max int
	}{
		{0, 0, 1000},
		{1, 1, 1000},
		{1000, 1, 1000},
		{1001, 1, 1000},
		{-1, 0, -1},
		{1.5, 0, 1000},
		{safeIntegerLimit, 0, -1},
		{safeIntegerLimit + 1, 0, -1},
		{-safeIntegerLimit, math.MinInt, -1},
		{math.MaxInt64, 0, -1},
		{1e300, 0, -1},
		{math.NaN(), 0, -1},
		{math.Inf(1), 0, -1},
		{math.Inf(-1), 0, -1},
		{math.Copysign(0, -1), 0, 10},
	} {
		f.Add(seed.v, seed.min, seed.max)
	}

	f.Fuzz(func(t *testing.T, v float64, minValue, maxValue int) {
		got, ok := boundedInteger(v, minValue, maxValue)

		want := false
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v == math.Trunc(v) && math.Abs(v) <= safeIntegerLimit {
			// v is exact in int64 here, so compare there: float64(minValue)
			// would round for bounds beyond 2^53.
			n := int64(v)
			want = n >= int64(minValue) && (maxValue == -1 || n <= int64(maxValue))
		}

		require.Equal(t, want, ok, "boundedInteger(%v, %d, %d)", v, minValue, maxValue)
		if ok {
			require.InDelta(t, v, float64(got), 0, "boundedInteger(%v) returned %d", v, got)
		} else {
			require.Zero(t, got)
		}

		// Any JSON value that is not a number is rejected, as Number.isSafeInteger does.
		for _, notNumber := range []any{nil, "1", true, int(v), map[string]any{}} {
			n, accepted := boundedInteger(notNumber, minValue, maxValue)
			require.False(t, accepted, "accepted %T %v", notNumber, notNumber)
			require.Zero(t, n)
		}
	})
}
