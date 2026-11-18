// Copyright (c) 2026, the sh authors.
// See LICENSE for licensing information

package internal

import (
	"strings"
	"unicode/utf8"
)

// SingleByteLocale reports whether locale names an LC_CTYPE charset where
// MB_CUR_MAX==1, i.e. every byte is its own character: this covers C/POSIX,
// an unset locale, ISO-8859-* variants, and anything else not recognized as
// UTF-8 or a legacy multi-byte charset (Big5, Shift-JIS — see
// syntax.mbCharset). For a UTF-8 or legacy multi-byte locale, a byte that
// utf8.DecodeRune rejects may still be part of a wider character and must be
// left untouched rather than reinterpreted one byte at a time.
func SingleByteLocale(locale string) bool {
	v := strings.ToLower(locale)
	switch {
	case strings.Contains(v, "utf-8"), strings.Contains(v, "utf8"),
		strings.Contains(v, "big5"),
		strings.Contains(v, "sjis"), strings.Contains(v, "shift_jis"), strings.Contains(v, "shift-jis"):
		return false
	}
	return true
}

// WidenLatin1 rewrites the bytes of s that utf8.DecodeRune rejects into
// their Unicode Latin-1 Supplement rune equivalents, leaving any already
// -valid UTF-8 runs untouched. It returns s unchanged when s is already
// valid UTF-8.
//
// This matters for single-byte locales such as C/POSIX or an ISO-8859-1
// variant (e.g. de_DE.ISO-8859-1): every byte 0-255 is one character there,
// and for ISO-8859-1 specifically a byte's value equals its Unicode
// codepoint. Widening such text into runes lets it flow through the regexp
// -based pattern matcher — with its bracket-expression, POSIX class, and
// range logic — instead of silently downgrading to the byte-blind matcher
// that cannot see "[...]" at all.
func WidenLatin1(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && w <= 1 {
			// Not a byte utf8.DecodeRune could parse: treat it as the
			// single Latin-1 character with that codepoint, rather than
			// leaving it as an undecodable byte.
			sb.WriteRune(rune(s[i]))
			i++
			continue
		}
		// Already a validly-encoded rune (e.g. embedded UTF-8 text):
		// preserve it as-is rather than reinterpreting its bytes.
		sb.WriteRune(r)
		i += w
	}
	return sb.String()
}
