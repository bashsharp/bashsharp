// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import "unicode/utf8"

// Argument byte fidelity on Windows.
//
// A shell word is a byte string: `recho $'ab\xde'` must reach the child
// with the raw byte 0xDE (nquote4.tests). A Windows command line is
// UTF-16, so a byte that is not valid UTF-8 has no direct spelling there.
// Cygwin and MSYS solve this with the convention Python and Rust also use
// for undecodable bytes: byte b becomes the lone surrogate U+DC00+b on the
// way into UTF-16, and a lone surrogate in that range becomes the byte
// again on the way out.
//
// Go on Windows converts between UTF-16 and its strings with WTF-8
// (syscall.UTF16FromString / UTF16ToString since Go 1.21): a lone surrogate
// is preserved as its three-byte WTF-8 sequence, never replaced with
// U+FFFD. So the whole convention is two string transforms, without a
// hand-built CreateProcess command line:
//
//   - the parent encodes each invalid byte as the WTF-8 form of U+DC00+b
//     before handing argv to os/exec; syscall's encodeWTF16 turns that into
//     the lone surrogate MSYS programs expect, and a plain Go receiver
//     gets the same three WTF-8 bytes back in os.Args;
//   - the receiver decodes WTF-8 U+DC80..U+DCFF back to the raw byte
//     ([DecodeWindowsArgs]); bashy and its applets do this at startup.
//
// Every byte that is not part of a valid UTF-8 sequence is encoded — the
// three bytes of a WTF-8 surrogate included, so a shell string that
// happens to hold them arrives as those three bytes, not as one. The
// receiver's ambiguity is Cygwin's too: a lone U+DC80..U+DCFF on the
// command line is read as a byte, whoever put it there.

// EncodeWindowsArgs returns args with every byte that is not part of a
// valid UTF-8 sequence spelled as the WTF-8 form of the lone surrogate
// U+DC00+byte, so that os/exec's UTF-16 command line carries the byte the
// way Cygwin/MSYS programs expect. It is the transform the interpreter
// applies to a child's argv on Windows; args that need no change are
// returned as they are, and on other platforms nothing calls it.
func EncodeWindowsArgs(args []string) []string {
	var out []string
	for i, a := range args {
		enc := encodeWindowsArg(a)
		if enc == a {
			if out != nil {
				out = append(out, a)
			}
			continue
		}
		if out == nil {
			out = append(make([]string, 0, len(args)), args[:i]...)
		}
		out = append(out, enc)
	}
	if out == nil {
		return args
	}
	return out
}

// encodeWindowsArg is [EncodeWindowsArgs] for one argument.
func encodeWindowsArg(s string) string {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != utf8.RuneError || size != 1 {
			i += size
			continue
		}
		// An invalid byte. Encode this and every following one; a valid
		// sequence is copied through.
		buf := make([]byte, 0, len(s)+2*4)
		buf = append(buf, s[:i]...)
		for i < len(s) {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r != utf8.RuneError || size != 1 {
				buf = append(buf, s[i:i+size]...)
				i += size
				continue
			}
			buf = appendWTF8Surrogate(buf, s[i])
			i++
		}
		return string(buf)
	}
	return s
}

// DecodeWindowsArgs returns args with every WTF-8 encoded lone surrogate
// U+DC80..U+DCFF replaced by the byte it stands for — the inverse of
// [EncodeWindowsArgs], and of the Cygwin/MSYS convention for undecodable
// bytes on a UTF-16 command line. A program built with this module calls
// it on os.Args at startup on Windows (Go has already decoded the UTF-16
// command line with WTF-8, so the surrogates arrive as three-byte
// sequences). Surrogates outside that range and every valid sequence are
// left alone; args that need no change are returned as they are.
func DecodeWindowsArgs(args []string) []string {
	var out []string
	for i, a := range args {
		dec := decodeWindowsArg(a)
		if dec == a {
			if out != nil {
				out = append(out, a)
			}
			continue
		}
		if out == nil {
			out = append(make([]string, 0, len(args)), args[:i]...)
		}
		out = append(out, dec)
	}
	if out == nil {
		return args
	}
	return out
}

// decodeWindowsArg is [DecodeWindowsArgs] for one argument.
func decodeWindowsArg(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != 0xED || !isWTF8ByteSurrogate(s[i:]) {
			continue
		}
		buf := make([]byte, 0, len(s))
		buf = append(buf, s[:i]...)
		for i < len(s) {
			if s[i] == 0xED && isWTF8ByteSurrogate(s[i:]) {
				buf = append(buf, wtf8SurrogateByte(s[i:]))
				i += 3
				continue
			}
			buf = append(buf, s[i])
			i++
		}
		return string(buf)
	}
	return s
}

// isWTF8ByteSurrogate reports whether s starts with the WTF-8 encoding of
// U+DC80..U+DCFF — a lone surrogate standing for one raw byte.
func isWTF8ByteSurrogate(s string) bool {
	return len(s) >= 3 && s[0] == 0xED && (s[1] == 0xB2 || s[1] == 0xB3) && 0x80 <= s[2] && s[2] <= 0xBF
}

// appendWTF8Surrogate appends the WTF-8 encoding of U+DC00+b for an
// invalid byte b (0x80..0xFF, so the code point is in U+DC80..U+DCFF).
func appendWTF8Surrogate(buf []byte, b byte) []byte {
	r := rune(0xDC00) + rune(b)
	return append(buf, 0xE0|byte(r>>12), 0x80|byte(r>>6)&0x3F, 0x80|byte(r)&0x3F)
}

// wtf8SurrogateByte returns the byte a WTF-8 U+DC80..U+DCFF sequence at the
// start of s stands for; s must satisfy [isWTF8ByteSurrogate].
func wtf8SurrogateByte(s string) byte {
	r := rune(s[0]&0x0F)<<12 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F)
	return byte(r - 0xDC00)
}
