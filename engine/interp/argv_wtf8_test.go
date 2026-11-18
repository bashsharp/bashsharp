// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"math/rand/v2"
	"runtime"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// Sprint 245, story 682 (argv byte fidelity): `recho $'ab\xde'` must reach
// a Windows child as the byte 0xDE. The parent spells an invalid byte as
// the WTF-8 form of the Cygwin/MSYS lone surrogate U+DC00+b, which Go's
// syscall layer turns into that surrogate on the UTF-16 command line; a Go
// receiver decodes it back. Both transforms are pure and tested here on
// every host; the Windows-only round trip through syscall is in
// argv_wtf8_windows_test.go.

func TestEncodeWindowsArg(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"héllo wörld", "héllo wörld"}, // valid multi-byte stays
		{"\ufffd", "\ufffd"},           // a literal U+FFFD is valid UTF-8
		{"ab\xde", "ab\xed\xb3\x9e"},   // nquote4: 0xDE -> U+DCDE
		{"\x80", "\xed\xb2\x80"},       // 0x80 -> U+DC80
		{"\xff\xfe", "\xed\xb3\xbf\xed\xb3\xbe"},
		{"\xe2\x82", "\xed\xb3\xa2\xed\xb2\x82"}, // a truncated sequence is two invalid bytes
		// The three bytes of a WTF-8 surrogate are three invalid bytes:
		// each is encoded, so the child gets exactly those bytes back.
		{"ab\xed\xb3\x9e", "ab\xed\xb3\xad\xed\xb2\xb3\xed\xb2\x9e"},
		{"\xed\xa0\x80", "\xed\xb3\xad\xed\xb2\xa0\xed\xb2\x80"},
		{"x\xdey\xdfz", "x\xed\xb3\x9ey\xed\xb3\x9fz"},
	}
	for _, c := range cases {
		if got := encodeWindowsArg(c.in); got != c.want {
			t.Errorf("encodeWindowsArg(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := decodeWindowsArg(c.want); got != c.in {
			t.Errorf("decodeWindowsArg(%q) = %q, want %q", c.want, got, c.in)
		}
	}
}

func TestDecodeWindowsArg(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"ab\xed\xb3\x9e", "ab\xde"},
		{"\xed\xb2\x80\xed\xb3\xbf", "\x80\xff"},
		{"\xed\xa0\x80", "\xed\xa0\x80"}, // U+D800: not a byte surrogate, kept
		{"\xed\xb1\xbf", "\xed\xb1\xbf"}, // U+DC7F: below the byte range, kept
		{"\xed\xb3", "\xed\xb3"},         // truncated: kept
		{"ab\xde", "ab\xde"},             // already raw
	}
	for _, c := range cases {
		if got := decodeWindowsArg(c.in); got != c.want {
			t.Errorf("decodeWindowsArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every byte string round-trips, and the encoded form is what Go's WTF-16
// encoder will emit as a lone surrogate: exactly one UTF-16 unit
// 0xDC00+b per invalid byte, with valid text untouched.
func TestWindowsArgRoundTrip(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(682, 245))
	for n := 0; n < 2000; n++ {
		b := make([]byte, rng.IntN(12))
		for i := range b {
			switch rng.IntN(4) {
			case 0:
				b[i] = byte(0x80 + rng.IntN(0x80)) // often invalid
			case 1:
				b[i] = 0xED
			default:
				b[i] = byte('a' + rng.IntN(26))
			}
		}
		in := string(b)
		enc := encodeWindowsArg(in)
		if !utf8.ValidString(enc) && !wtf8Valid(enc) {
			t.Fatalf("encodeWindowsArg(%q) = %q is not WTF-8", in, enc)
		}
		if got := decodeWindowsArg(enc); got != in {
			t.Fatalf("round trip of %q: encoded %q, decoded %q", in, enc, got)
		}
		// What the UTF-16 command line carries, unit by unit.
		var want []uint16
		for i := 0; i < len(in); {
			r, size := utf8.DecodeRuneInString(in[i:])
			if r == utf8.RuneError && size == 1 {
				want = append(want, 0xDC00+uint16(in[i]))
				i++
				continue
			}
			want = utf16.AppendRune(want, r)
			i += size
		}
		got := wtf16Encode(enc)
		if len(got) != len(want) {
			t.Fatalf("UTF-16 of %q: got %x, want %x", in, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("UTF-16 of %q: got %x, want %x", in, got, want)
			}
		}
	}
}

func TestEncodeDecodeWindowsArgsSlices(t *testing.T) {
	t.Parallel()
	clean := []string{"recho", "plain", "héllo"}
	if got := EncodeWindowsArgs(clean); &got[0] != &clean[0] {
		t.Error("EncodeWindowsArgs copied a slice that needed no change")
	}
	if got := DecodeWindowsArgs(clean); &got[0] != &clean[0] {
		t.Error("DecodeWindowsArgs copied a slice that needed no change")
	}
	args := []string{"recho", "ab\xde", "ok", "\xff"}
	enc := EncodeWindowsArgs(args)
	if enc[0] != "recho" || enc[1] != "ab\xed\xb3\x9e" || enc[2] != "ok" || enc[3] != "\xed\xb3\xbf" {
		t.Errorf("EncodeWindowsArgs = %q", enc)
	}
	if args[1] != "ab\xde" {
		t.Error("EncodeWindowsArgs modified its input")
	}
	dec := DecodeWindowsArgs(enc)
	for i := range args {
		if dec[i] != args[i] {
			t.Errorf("DecodeWindowsArgs[%d] = %q, want %q", i, dec[i], args[i])
		}
	}
	if len(EncodeWindowsArgs(nil)) != 0 || len(DecodeWindowsArgs(nil)) != 0 {
		t.Error("nil argv must stay empty")
	}
}

// isWTF8Surrogate reports whether s starts with the WTF-8 encoding of a
// surrogate code point (U+D800..U+DFFF): ED A0..BF 80..BF.
func isWTF8Surrogate(s string) bool {
	return len(s) >= 3 && s[0] == 0xED && 0xA0 <= s[1] && s[1] <= 0xBF && 0x80 <= s[2] && s[2] <= 0xBF
}

// wtf8Valid reports whether s is well-formed WTF-8: UTF-8 plus the
// three-byte encodings of surrogate code points.
func wtf8Valid(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if !isWTF8Surrogate(s[i:]) {
				return false
			}
			i += 3
			continue
		}
		i += size
	}
	return true
}

// wtf16Encode mirrors syscall's encodeWTF16 on Windows (which is not
// available on other hosts): a WTF-8 surrogate becomes one UTF-16 unit.
func wtf16Encode(s string) []uint16 {
	var buf []uint16
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && isWTF8Surrogate(s[i:]) {
			buf = append(buf, uint16(rune(s[i]&0x0F)<<12|rune(s[i+1]&0x3F)<<6|rune(s[i+2]&0x3F)))
			i += 3
			continue
		}
		buf = utf16.AppendRune(buf, r)
		i += size
	}
	return buf
}

// The exec handler's hook: the encoding on Windows, the identity elsewhere
// (execve takes argv as bytes; Unix behaviour must not move).
func TestExecArgsForOS(t *testing.T) {
	t.Parallel()
	args := []string{"recho", "ab\xde"}
	got := execArgsForOS(args)
	if runtime.GOOS == "windows" {
		if got[1] != "ab\xed\xb3\x9e" {
			t.Fatalf("execArgsForOS on Windows = %q", got)
		}
		return
	}
	if &got[0] != &args[0] || got[1] != "ab\xde" {
		t.Fatalf("execArgsForOS off Windows must be the identity, got %q", got)
	}
}
