package handlers

import "strings"

// jsTrimEqual reports whether s has no leading or trailing JS-whitespace
// character, the same definition list_messages.go's isJSWhitespace uses.
// strings.TrimSpace is not equivalent here: Go's unicode.IsSpace (which
// TrimSpace trims by) includes U+0085 (NEL) but excludes U+FEFF (BOM/
// ZWNBSP), while JS's String.prototype.trim() is the other way around — it
// strips U+FEFF but leaves U+0085 untouched. A messageBox or messageId with a
// leading/trailing BOM must fail isExactBoundedText exactly as it would fail
// TS's `value.trim() === value`, not be silently accepted because Go's
// TrimSpace doesn't consider U+FEFF whitespace.
func jsTrimEqual(s string) bool {
	return strings.TrimFunc(s, isJSWhitespace) == s
}

// Field bounds mirrored from the TS reference server's
// src/security/messageFields.ts, which POST /listMessages validates against.
const (
	maxMessageBoxBytes = 128
	maxMessageIDBytes  = 256
)

// containsControlCharacter reports whether s contains a C0 control character
// or a C1 control character (the U+007F-U+009F range), matching the TS
// reference server's containsControlCharacter. Both ranges would otherwise let
// a messageBox or messageId smuggle formatting characters (newlines, NEL, …)
// through what looks like a plain, printable name.
func containsControlCharacter(s string) bool {
	for _, r := range s {
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}

// isExactBoundedText mirrors the TS reference server's isExactBoundedText: a
// non-empty string with no leading or trailing whitespace, at most maxBytes
// UTF-8 bytes, and free of control characters. len(s) is a byte count in Go,
// the same unit Buffer.byteLength(s, 'utf8') reports in TS.
func isExactBoundedText(s string, maxBytes int) bool {
	if s == "" || !jsTrimEqual(s) {
		return false
	}
	if len(s) > maxBytes {
		return false
	}
	return !containsControlCharacter(s)
}

// isCanonicalMessageBox mirrors the TS reference server's isCanonicalMessageBox.
func isCanonicalMessageBox(s string) bool { return isExactBoundedText(s, maxMessageBoxBytes) }

// isCanonicalMessageID mirrors the TS reference server's isCanonicalMessageId.
func isCanonicalMessageID(s string) bool { return isExactBoundedText(s, maxMessageIDBytes) }
