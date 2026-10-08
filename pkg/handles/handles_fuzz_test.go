package handles

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handleSeeds are inputs worth starting from: real handles, every fold and
// separator rule, reserved names and their look-alikes, and text that is not
// ASCII or not UTF-8 at all.
var handleSeeds = []string{
	"", "a", "deggen", "degen", "Deg-Gen", "d.e_g-g.e.n", "deg__gen",
	"rn", "rnvv", "rrnn", "vvv", "m", "w",
	"0123456789", "i1l", "s5", "z2", "b8",
	"admin", "adm1n", "Adm-In", "support", "5upp0rt", "postmaster",
	"a.b", "ab-", "-ab", strings.Repeat("a", 32), strings.Repeat("a", 33),
	"İstanbul", "\u4e16\u754c", "\xff\xfe", "\xe4-\x80\x80",
}

// FuzzSkeleton checks the properties the look-alike registry relies on. A
// stored skeleton must fold to itself, or a lookup by skeleton would disagree
// with the claim that stored it; and a skeleton must carry nothing a fold rule
// exists to remove, or two handles a user cannot tell apart would not collide.
func FuzzSkeleton(f *testing.F) {
	for _, s := range handleSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		sk := Skeleton(s)

		require.Equal(t, sk, Skeleton(sk), "Skeleton must be idempotent")
		require.True(t, utf8.ValidString(sk), "Skeleton must return valid UTF-8")

		assert.False(t, strings.ContainsAny(sk, "._-"), "separators survive in %q", sk)
		assert.False(t, strings.ContainsAny(sk, "01258i"), "fold sources survive in %q", sk)
		assert.NotContains(t, sk, "rn")
		assert.NotContains(t, sk, "vv")
		for i := 0; i < len(sk); i++ {
			assert.False(t, sk[i] >= 'A' && sk[i] <= 'Z', "upper-case ASCII survives in %q", sk)
		}
		var last rune = -1
		for _, r := range sk {
			assert.NotEqual(t, last, r, "doubled rune %q survives in %q", r, sk)
			last = r
		}

		// Separators never distinguish handles. Only for valid UTF-8: removing
		// a byte from an invalid sequence can make it decode as a real rune.
		if utf8.ValidString(s) {
			bare := strings.NewReplacer(".", "", "_", "", "-", "").Replace(s)
			assert.Equal(t, sk, Skeleton(bare), "separators changed the skeleton of %q", s)
		}
		// Nor does ASCII case. Beyond ASCII, upper-casing does not round-trip.
		if isASCII(s) {
			assert.Equal(t, sk, Skeleton(strings.ToUpper(s)), "case changed the skeleton of %q", s)
		}
	})
}

// FuzzValidate checks that whatever Validate accepts is a syntactically valid,
// unreserved handle, and that it fails only with its two documented errors.
func FuzzValidate(f *testing.F) {
	for _, s := range handleSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, handle string) {
		err := Validate(handle)
		if err != nil {
			require.True(t, errors.Is(err, ErrInvalidHandle) || errors.Is(err, ErrReservedHandle),
				"unexpected error %v for %q", err, handle)
			if errors.Is(err, ErrReservedHandle) {
				// Only a syntactically valid handle reaches the reserved check.
				assert.True(t, handleRE.MatchString(handle))
				assert.True(t, reservedSkeletons[Skeleton(handle)])
			}
			return
		}

		require.GreaterOrEqual(t, len(handle), 3)
		require.LessOrEqual(t, len(handle), 32)
		for i := 0; i < len(handle); i++ {
			c := handle[i]
			ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
			require.True(t, ok, "accepted %q with byte %q", handle, c)
		}
		require.True(t, isAlnum(handle[0]) && isAlnum(handle[len(handle)-1]),
			"accepted %q, which does not start and end with a letter or digit", handle)
		require.False(t, reservedSkeletons[Skeleton(handle)], "accepted %q, a look-alike of a reserved name", handle)
	})
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}
