package handlers

import "testing"

// bom is the BOM/ZWNBSP character, built from its rune value rather than
// written as a literal byte sequence, because Go's compiler rejects an
// embedded U+FEFF byte sequence anywhere but the very start of a source file.
var bom = string(rune(0xFEFF))

// TestIsExactBoundedText_BOM pins the fix for jsTrimEqual: Go's
// strings.TrimSpace (unicode.IsSpace) trims U+0085 (NEL) but not U+FEFF
// (BOM/ZWNBSP), while JS's String.prototype.trim() is the other way around —
// it strips U+FEFF but leaves U+0085 alone. A leading or trailing BOM must be
// rejected here exactly as it would fail TS's `value.trim() === value`, not
// silently accepted because Go's TrimSpace doesn't consider U+FEFF
// whitespace.
func TestIsExactBoundedText_BOM(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"leading BOM", bom + "inbox", false},
		{"trailing BOM", "inbox" + bom, false},
		{"BOM only", bom, false},
		{"plain", "inbox", true},
		// U+0085 (NEL) is JS-non-whitespace but is still rejected: it is a
		// control character, caught by containsControlCharacter regardless of
		// jsTrimEqual.
		{"embedded NEL", "in\u0085box", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isExactBoundedText(c.s, maxMessageBoxBytes); got != c.want {
				t.Errorf("isExactBoundedText(%q) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestJSTrimEqual(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"inbox", true},
		{bom + "inbox", false},
		{"inbox" + bom, false},
		{" inbox", false},
		{"inbox ", false},
		// NEL is not JS whitespace, so it does not break equality here (even
		// though isExactBoundedText separately rejects it as a control char).
		{"\u0085inbox\u0085", true},
	}
	for _, c := range cases {
		if got := jsTrimEqual(c.s); got != c.want {
			t.Errorf("jsTrimEqual(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
