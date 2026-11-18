// Copyright (c) 2024, the sh authors.
// See LICENSE for licensing information.

package syntax

import (
	"os"
	"strings"
)

// mbCharset identifies the LC_CTYPE charset that governs how the lexer
// advances over non-ASCII bytes. It mirrors GNU bash 5.3, whose lexer reads
// characters with the locale's MB_CUR_MAX via mbrtowc (see lib/sh/shmbchar.c
// and locale.c): a UTF-8 locale decodes UTF-8; a legacy multibyte locale such
// as Big5 or Shift-JIS reads its own double-byte characters; everything else
// (the C locale, ISO-8859, an unknown charset) runs in single-byte mode where
// every non-ASCII byte is one opaque character.
type mbCharset int

const (
	// mbSingleByte is MB_CUR_MAX == 1: pure byte mode. utf8.DecodeRune's
	// single-opaque-byte fallback already matches bash here, so no extra
	// multibyte handling is needed.
	mbSingleByte mbCharset = iota
	// mbUTF8 is a UTF-8 locale; utf8.DecodeRune handles it directly.
	mbUTF8
	// mbBig5 is the zh_TW.big5 family.
	mbBig5
	// mbShiftJIS is the ja_JP.sjis / Shift-JIS family.
	mbShiftJIS
)

// localeCharset resolves the active LC_CTYPE charset from the process
// environment, following bash's locale.c precedence of LC_ALL > LC_CTYPE >
// LANG. It is only consulted once a non-UTF-8 byte is encountered.
func localeCharset() mbCharset {
	var v string
	for _, name := range [...]string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if s := os.Getenv(name); s != "" {
			v = s
			break
		}
	}
	v = strings.ToLower(v)
	switch {
	case v == "" || v == "c" || v == "posix":
		return mbSingleByte
	case strings.Contains(v, "utf-8") || strings.Contains(v, "utf8"):
		return mbUTF8
	case strings.Contains(v, "big5"):
		return mbBig5
	case strings.Contains(v, "sjis") || strings.Contains(v, "shift_jis") || strings.Contains(v, "shift-jis"):
		return mbShiftJIS
	}
	return mbSingleByte
}

// multibyte reports whether the charset has characters wider than one byte
// that utf8.DecodeRune cannot decode, i.e. whether mbCharLen is meaningful.
func (cs mbCharset) multibyte() bool {
	return cs == mbBig5 || cs == mbShiftJIS
}

// mbCharLen returns the byte length (>1) of the legacy multibyte character at
// the start of bs, or 0 when bs does not begin a valid wide character of the
// charset. A 0 makes the caller advance a single opaque byte, exactly as bash
// does when mbrtowc reports an invalid or incomplete sequence (MB_INVALIDCH ->
// clen = 1). The lead/trail byte ranges match the respective encodings' double
// -byte character definitions.
func (cs mbCharset) mbCharLen(bs []byte) int {
	if len(bs) < 2 {
		return 0
	}
	b0, b1 := bs[0], bs[1]
	switch cs {
	case mbBig5:
		// Lead 0x81-0xFE; trail 0x40-0x7E or 0xA1-0xFE.
		if b0 >= 0x81 && b0 <= 0xfe &&
			((b1 >= 0x40 && b1 <= 0x7e) || (b1 >= 0xa1 && b1 <= 0xfe)) {
			return 2
		}
	case mbShiftJIS:
		// Lead 0x81-0x9F or 0xE0-0xFC; trail 0x40-0x7E or 0x80-0xFC.
		if ((b0 >= 0x81 && b0 <= 0x9f) || (b0 >= 0xe0 && b0 <= 0xfc)) &&
			((b1 >= 0x40 && b1 <= 0x7e) || (b1 >= 0x80 && b1 <= 0xfc)) {
			return 2
		}
	}
	return 0
}
