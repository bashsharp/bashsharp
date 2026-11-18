// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package expand

import (
	"fmt"
	"iter"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Braces performs brace expansion on a word, given that it contains any
// [syntax.BraceExp] parts. For example, the word with a brace expansion
// "foo{bar,baz}" will return two literal words, "foobar" and "foobaz".
//
// Note that the resulting words may share word parts.
//
// Deprecated: use [BracesSeq], which yields words lazily and reports an
// error rather than letting a large sequence allocate huge amounts.
func Braces(word *syntax.Word) []*syntax.Word {
	var all []*syntax.Word
	bracesSeqRec(word, func(w *syntax.Word) bool {
		all = append(all, w)
		return true
	})
	return all
}

// BracesSeq performs brace expansion on a word, given that it contains any
// [syntax.BraceExp] parts. For example, the word with a brace expansion
// "foo{bar,baz}" will return two literal words, "foobar" and "foobaz".
//
// The iteration yields an error and stops if the total expansion is too
// large, including combinatorial blow-ups across multiple brace expansions
// like {1..100}{1..100}{1..100}. This may be configurable with cfg in the
// future; the parameter is entirely unused for now.
//
// Note that the resulting words may share word parts.
func BracesSeq(cfg *Config, word *syntax.Word) iter.Seq2[*syntax.Word, error] {
	return func(yield func(*syntax.Word, error) bool) {
		// 16Ki expanded elements is more than any script should need in practice,
		// but it's small enough where we don't waste too much memory and CPU.
		const limit = 16 << 10
		count := 0
		bracesSeqRec(word, func(w *syntax.Word) bool {
			count++
			if count > limit {
				yield(nil, fmt.Errorf("brace expansion would exceed %d elements", limit))
				return false
			}
			if text, ok := generatedBackquoteWord(w); ok {
				yield(nil, fmt.Errorf("bad substitution: no closing \"`\" in %s", text))
				return false
			}
			return yield(w, nil)
		})
	}
}

func generatedBackquoteWord(word *syntax.Word) (string, bool) {
	var text string
	for _, part := range word.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		text += lit.Value
	}
	if i := strings.IndexByte(text, '`'); i >= 0 && i+1 < len(text) {
		return text[i:], true
	}
	return "", false
}

// bracesSeqRec yields each fully-expanded word descended from word.
// It returns false if iteration should stop.
func bracesSeqRec(word *syntax.Word, yield func(*syntax.Word) bool) bool {
	var left []syntax.WordPart
	for i, wp := range word.Parts {
		br, ok := wp.(*syntax.BraceExp)
		if !ok {
			left = append(left, wp)
			continue
		}
		rest := word.Parts[i+1:]
		// Yield each word produced by recursing on `next`,
		// after prepending `left` to its Parts.
		expand := func(next *syntax.Word) bool {
			return bracesSeqRec(next, func(w *syntax.Word) bool {
				w.Parts = mergeAdjacentLits(append(append([]syntax.WordPart(nil), left...), w.Parts...))
				return yield(w)
			})
		}
		if br.Sequence {
			fromLit := br.Elems[0].Lit()
			toLit := br.Elems[1].Lit()
			// Bash zero-pads the output to the width of the longest
			// operand when *either* operand has a leading zero. So
			// {01..100} → 001..100, but {1..100} → 1..100. The width
			// includes any leading "-" because bash's "-05..05" stays
			// 3 chars wide on both sides.
			padWidth := 0
			if hasLeadingZero(fromLit) || hasLeadingZero(toLit) {
				padWidth = max(len(fromLit), len(toLit))
			}

			chars := false
			// ParseInt with bit size 64 to ensure consistent behavior on 32-bit platforms.
			from, err1 := strconv.ParseInt(fromLit, 10, 64)
			to, err2 := strconv.ParseInt(toLit, 10, 64)
			if err1 != nil || err2 != nil {
				chars = true
				from = int64(fromLit[0])
				to = int64(toLit[0])
			}
			upward := from <= to
			incr := int64(1)
			if !upward {
				incr = -1
			}
			if len(br.Elems) > 2 {
				// Bash 5.3 treats the step as abs(N) with sign matching
				// the range direction: {10..1..2}, {10..1..-2},
				// {-1..-10..2} and {-1..-10..-2} all use |step|=2 with
				// the implied direction.
				n, _ := strconv.ParseInt(br.Elems[2].Lit(), 10, 64)
				if n != 0 {
					if n < 0 {
						n = -n
					}
					if !upward {
						n = -n
					}
					incr = n
				}
			}
			for n := from; (upward && n <= to) || (!upward && n >= to); n += incr {
				next := *word
				lit := &syntax.Lit{}
				switch {
				case chars:
					// bash 5.3 skips the backslash character
					// in `{X..Y}` char ranges — likely because
					// emitting a literal `\` would behave like
					// a line continuation in subsequent shell
					// processing. The generated arg is the
					// empty string at that position.
					if rune(n) == '\\' {
						next.Parts = append([]syntax.WordPart{&syntax.SglQuoted{}}, rest...)
						if !expand(&next) {
							return false
						}
						if n == to {
							break
						}
						continue
					} else {
						lit.Value = string(rune(n))
					}
				case padWidth > 0:
					lit.Value = fmt.Sprintf("%0*d", padWidth, n)
				default:
					lit.Value = strconv.FormatInt(n, 10)
				}
				next.Parts = append([]syntax.WordPart{lit}, rest...)
				if !expand(&next) {
					return false
				}
				if n == to {
					break
				}
			}
			return true
		}
		onlyEmptyElems := true
		for _, elem := range br.Elems {
			if hasMeaningfulParts(elem.Parts) {
				onlyEmptyElems = false
				break
			}
		}
		emptyElemYielded := false
		for _, elem := range br.Elems {
			// Bash 5.3 treats an empty brace-list element as zero text.
			// When the entire resulting word would be empty and this list
			// also has non-empty alternatives, the empty results are elided:
			// `{X,,Y,}` becomes `X Y`. If every alternative is empty,
			// bash still leaves one empty word, so collapse duplicates.
			if !hasMeaningfulParts(elem.Parts) && !hasMeaningfulParts(left) && !hasMeaningfulParts(rest) {
				if !onlyEmptyElems || emptyElemYielded {
					continue
				}
				emptyElemYielded = true
			}
			next := *word
			next.Parts = append(append([]syntax.WordPart(nil), elem.Parts...), rest...)
			if !expand(&next) {
				return false
			}
		}
		return true
	}
	return yield(&syntax.Word{Parts: mergeAdjacentLits(left)})
}

// hasLeadingZero reports whether the literal carries a leading zero
// after any sign, signaling bash's zero-pad mode for brace sequences.
// "01" → true, "-01" → true, "0" → false (single zero is not padding),
// "10" → false.
func hasLeadingZero(s string) bool {
	if len(s) == 0 {
		return false
	}
	if s[0] == '-' || s[0] == '+' {
		s = s[1:]
	}
	return len(s) > 1 && s[0] == '0'
}

func mergeAdjacentLits(parts []syntax.WordPart) []syntax.WordPart {
	var out []syntax.WordPart
	for _, part := range parts {
		lit, ok := part.(*syntax.Lit)
		if !ok || len(out) == 0 {
			out = append(out, part)
			continue
		}
		prev, ok := out[len(out)-1].(*syntax.Lit)
		if !ok {
			out = append(out, part)
			continue
		}
		merged := *prev
		merged.Value += lit.Value
		out[len(out)-1] = &merged
	}
	return out
}

func hasMeaningfulParts(parts []syntax.WordPart) bool {
	for _, part := range parts {
		lit, ok := part.(*syntax.Lit)
		if !ok || lit.Value != "" {
			return true
		}
	}
	return false
}

// hasQuotedPart reports whether the word contains an explicitly quoted
// part. bash keeps a brace-expanded word that produces an empty field
// only when it carries such quoting (a quoted null like a brace list
// followed by an empty single-quoted string); otherwise the empty field
// is elided. An unquoted parameter that expands to nothing (a brace list
// followed by an unset $var) is dropped, so the presence of a
// non-literal part alone is not enough.
func hasQuotedPart(parts []syntax.WordPart) bool {
	for _, part := range parts {
		switch part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		}
	}
	return false
}
