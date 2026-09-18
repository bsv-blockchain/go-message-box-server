package handles

import (
	"errors"
	"testing"
)

func TestBRFCID(t *testing.T) {
	if got := BRFCID("public profile lookup", "Deggen", "v1.0.0"); got != BRFCLookup {
		t.Errorf("lookup id = %s, want %s", got, BRFCLookup)
	}
	if got := BRFCID("public profile reverse lookup", "Deggen", "v1.0.0"); got != BRFCReverseLookup {
		t.Errorf("reverse id = %s, want %s", got, BRFCReverseLookup)
	}
	// Vectors from go-paymail's own brfc_test.go.
	if got := BRFCID("BRFC Specifications", "andy (nChain)", "1"); got != "57dd1f54fc67" {
		t.Errorf("paymail vector = %s, want 57dd1f54fc67", got)
	}
	if got := BRFCID("  bsvalias Integration with Simplified Payment Protocol  ", "  andy (nChain)  ", "1"); got != "0036f9b8860f" {
		t.Errorf("trimmed vector = %s, want 0036f9b8860f", got)
	}
}

func TestSkeleton(t *testing.T) {
	cases := map[string]string{
		"deggen":    "degen",
		"d.e-g_gen": "degen",
		"degg3n":    "deg3n",
		"paypa1":    "paypal",
		"paypai":    "paypal",
		"paypal":    "paypal",
		"g00gle":    "gogle",
		"google":    "gogle",
		"rnike":     "mlke",
		"mike":      "mlke",
		"vvill":     "wl",
		"will":      "wl",
		"5am":       "sam",
		"2ed":       "zed",
		"8ob":       "bob",
		"--":        "",
	}
	for in, want := range cases {
		if got := Skeleton(in); got != want {
			t.Errorf("Skeleton(%q) = %q, want %q", in, got, want)
		}
	}
}

// Skeleton folds, it does not sanitise: metacharacters survive it, so callers
// that build a regex from a skeleton must escape it themselves.
func TestSkeletonKeepsRegexMetacharacters(t *testing.T) {
	cases := map[string]string{
		"a$b":   "a$b",
		"^ad":   "^ad",
		"a|b":   "a|b",
		"a(b)c": "a(b)c",
		`a\b`:   `a\b`,
		"a.*b":  "a*b", // '.' is a separator, '*' is not.
	}
	for in, want := range cases {
		if got := Skeleton(in); got != want {
			t.Errorf("Skeleton(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := []string{"deg", "deggen", "a.b-c_d", "abc123", "a23456789012345678901234567890bc"}
	for _, h := range ok {
		if err := Validate(h); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", h, err)
		}
	}
	// The last three are non-ASCII look-alikes of "admin"; the regex is
	// ASCII-only, so they are rejected before the reserved check ever folds
	// them (Skeleton("admİn") is "admln", the skeleton of "admin").
	invalid := []string{"", "ab", ".abc", "abc.", "Deggen", "deg gen", "deg+gen", "dég", "a23456789012345678901234567890bcd",
		"admın", "admİn", "ＡＤＭＩＮ"}
	for _, h := range invalid {
		if err := Validate(h); !errors.Is(err, ErrInvalidHandle) {
			t.Errorf("Validate(%q) = %v, want ErrInvalidHandle", h, err)
		}
	}
	reserved := []string{"admin", "adm1n", "a.d.m.i.n", "support", "r00t", "available"}
	for _, h := range reserved {
		if err := Validate(h); !errors.Is(err, ErrReservedHandle) {
			t.Errorf("Validate(%q) = %v, want ErrReservedHandle", h, err)
		}
	}
}
