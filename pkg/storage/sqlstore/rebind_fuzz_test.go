package sqlstore

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FuzzRebind checks the placeholder rewrite every query passes through. On
// PostgreSQL each ? must become the next $n, numbered from 1 with none skipped
// or repeated, and nothing else may change, or a value would be bound to the
// wrong column. Every other driver must get the query back untouched.
func FuzzRebind(f *testing.F) {
	for _, seed := range []string{
		"",
		"SELECT 1",
		"?",
		"??",
		`SELECT messageBoxId FROM messageBox WHERE identityKey = ? AND type = ?`,
		insertBoxWideSQL,
		updateBoxWideFeeSQL,
		"INSERT INTO t (a, b) VALUES (?, ?) ON CONFLICT(a) DO UPDATE SET b = ?",
		"SELECT '?' FROM t WHERE x = ?",
		"héllo ? wörld ?",
		"\xff?\xfe",
	} {
		f.Add(seed)
	}

	postgres := &Store{driver: "postgres"}
	sqlite := &Store{driver: "sqlite3"}

	f.Fuzz(func(t *testing.T, query string) {
		require.Equal(t, query, sqlite.rebind(query), "sqlite3 queries must pass through unchanged")

		out := postgres.rebind(query)
		require.NotContains(t, out, "?", "a placeholder was left unrewritten")

		// A literal $ in the input would be indistinguishable from a rewritten
		// placeholder below; queries never carry one.
		if strings.Contains(query, "$") {
			return
		}

		// Undo the rewrite, checking the numbering as we go, and expect the
		// original query back.
		var undone strings.Builder
		next := 1
		for i := 0; i < len(out); i++ {
			if out[i] != '$' {
				undone.WriteByte(out[i])
				continue
			}
			// Match the expected number as a prefix rather than reading digits
			// greedily: "?1" legitimately rewrites to "$11".
			num := strconv.Itoa(next)
			require.True(t, strings.HasPrefix(out[i+1:], num), "placeholder out of sequence at %d in %q, want $%s", i, out, num)
			undone.WriteByte('?')
			next++
			i += len(num)
		}
		assert.Equal(t, query, undone.String())
		assert.Equal(t, strings.Count(query, "?"), next-1)
	})
}
