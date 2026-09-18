// Package handles holds the pure rules for paymail-style handles: what is a
// valid handle, which handles are reserved, and the skeleton that decides
// whether two handles look alike.
package handles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// BRFC ids of the two capabilities this server publishes. BRFCID derives them.
const (
	BRFCLookup        = "0ace65da5987"
	BRFCReverseLookup = "43dcf83ddc5f"
)

// BRFCID is the paymail BRFC id: double SHA-256 of title+author+version,
// byte-reversed, hex, first 12 characters.
func BRFCID(title, author, version string) string {
	s := strings.TrimSpace(title) + strings.TrimSpace(author) + strings.TrimSpace(version)
	h1 := sha256.Sum256([]byte(s))
	h2 := sha256.Sum256(h1[:])
	for i, j := 0, len(h2)-1; i < j; i, j = i+1, j-1 {
		h2[i], h2[j] = h2[j], h2[i]
	}
	return hex.EncodeToString(h2[:])[:12]
}

var (
	ErrInvalidHandle  = errors.New("invalid handle")
	ErrReservedHandle = errors.New("reserved handle")
)

var handleRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,30}[a-z0-9]$`)

var reserved = []string{
	"admin", "administrator", "root", "support", "help", "postmaster", "abuse",
	"security", "info", "available", "api", "bsvalias", "system", "official", "noreply",
}

var reservedSkeletons = func() map[string]bool {
	m := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		m[Skeleton(r)] = true
	}
	return m
}()

// Validate reports whether handle may be registered at all. It does not look
// at what is already registered.
func Validate(handle string) error {
	if !handleRE.MatchString(handle) {
		return ErrInvalidHandle
	}
	if reservedSkeletons[Skeleton(handle)] {
		return ErrReservedHandle
	}
	return nil
}

var (
	separators = strings.NewReplacer(".", "", "_", "", "-", "")
	multiFold  = strings.NewReplacer("rn", "m", "vv", "w")
	singleFold = strings.NewReplacer("0", "o", "1", "l", "i", "l", "5", "s", "2", "z", "8", "b")
)

// Skeleton folds s to the form under which look-alike handles collide. It
// accepts any string, including a partial search query, and may return "".
// It is a fold, not a sanitiser: every character it does not fold away is
// preserved, so a caller that builds a regex from the result must escape it
// with regexp.QuoteMeta first. Folding is ASCII-only, so a non-ASCII query
// yields a skeleton that matches nothing stored (Validate rejects non-ASCII
// handles outright).
func Skeleton(s string) string {
	s = strings.ToLower(s)
	s = separators.Replace(s)
	s = multiFold.Replace(s)
	s = singleFold.Replace(s)
	var b strings.Builder
	var last rune = -1
	for _, r := range s {
		if r != last {
			b.WriteRune(r)
		}
		last = r
	}
	return b.String()
}
