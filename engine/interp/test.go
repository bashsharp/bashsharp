// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	resyntax "regexp/syntax"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/internal"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/pattern"
	"github.com/qiangli/bash/winmode"
)

// arithErrMsg returns the bash 5.3 wording for an arithmetic-
// syntax error encountered while evaluating the [[ ]] operand `s`.
// The returned text is shaped like
//
//	arithmetic syntax error: operand expected (error token is "X")
//
// where `X` is the trailing fragment that bash would highlight.
// Best-effort: when we can't determine a token, fall back to the
// full operand.
func arithErrMsg(err error, s string) string {
	tok := s
	// Strip a known prefix like "1:N: " from our parser error.
	msg := err.Error()
	if idx := strings.LastIndex(msg, ": "); idx >= 0 {
		core := msg[idx+2:]
		if core != "" {
			// Heuristic: take the last char of the operand as the
			// "error token" when the parser complained about a
			// trailing operator (`4+`, `4*`, …).
			last := s[len(s)-1]
			switch last {
			case '+', '-', '*', '/', '%', '<', '>', '&', '|', '^', '=', '!':
				tok = string(last)
			}
		}
	}
	return "arithmetic syntax error: operand expected (error token is \"" + tok + "\")"
}

// bashRegexErrMsg returns the bash 5.3 wording for an invalid `=~`
// regular expression. Bash uses POSIX regcomp diagnostics; the Go
// regexp engine reports its own wording. We map the Go error code to
// the closest POSIX-shaped reason and frame it as bash does:
//
//	invalid regular expression `<pat>': <reason>
func bashRegexErrMsg(err error, pat string) string {
	reason := err.Error()
	var rerr *resyntax.Error
	if errors.As(err, &rerr) {
		switch rerr.Code {
		case resyntax.ErrMissingRepeatArgument, resyntax.ErrInvalidRepeatOp:
			reason = "Repetition not preceded by valid expression"
		case resyntax.ErrMissingBracket:
			reason = "Unmatched [, [^, [:, [., or [="
		case resyntax.ErrMissingParen:
			reason = "Unmatched ( or \\("
		case resyntax.ErrUnexpectedParen:
			reason = "Unmatched ) or \\)"
		case resyntax.ErrTrailingBackslash:
			reason = "Trailing backslash"
		case resyntax.ErrInvalidCharRange:
			reason = "Invalid range end"
		case resyntax.ErrInvalidRepeatSize:
			reason = "Invalid content of \\{\\}"
		case resyntax.ErrInvalidCharClass:
			reason = "Invalid character class"
		case resyntax.ErrInvalidEscape:
			reason = "Invalid back reference"
		}
	}
	return "invalid regular expression `" + pat + "': " + reason
}

// arithCmpOperand returns the operand text to feed to the arithmetic
// evaluator for a `[[ ]]` integer comparison. When the operand is an
// array reference (`name[subscript]`) its raw source is returned so the
// evaluator performs the single subscript expansion bash does; any other
// operand is expanded as usual.
func (r *Runner) arithCmpOperand(ctx context.Context, expr syntax.TestExpr) string {
	if w, ok := expr.(*syntax.Word); ok {
		if src := r.sourceTextRange(w.Pos(), w.End(), false); src != "" && isArrayRefSource(src) {
			return src
		}
	}
	return r.bashTest(ctx, expr, false)
}

// isArrayRefSource reports whether s has the textual shape of an array
// element reference: a valid identifier, then `[`, then a subscript, then
// a closing `]`.
func isArrayRefSource(s string) bool {
	open := strings.IndexByte(s, '[')
	if open <= 0 || !strings.HasSuffix(s, "]") {
		return false
	}
	return syntax.ValidName(s[:open])
}

// arithFromString parses s as a bash arithmetic expression and
// returns its int64 value. Empty input is treated as 0 (bash behaves
// the same way). Unset / non-numeric tokens that don't parse as a
// valid expression return a non-nil error so callers can report the
// bash 5.3 "integer expected" / "syntax error" diagnostics.
func (r *Runner) arithFromString(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	// Wrap as `(( s ))` so the parser produces a single ArithmCmd.
	src := "((" + s + "))"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		return 0, err
	}
	if len(file.Stmts) != 1 {
		return 0, fmt.Errorf("not a valid arithmetic expression")
	}
	ac, ok := file.Stmts[0].Cmd.(*syntax.ArithmCmd)
	if !ok || ac.X == nil {
		return 0, fmt.Errorf("not a valid arithmetic expression")
	}
	n, err := expand.Arithm(r.ecfg, ac.X)
	if err != nil {
		return 0, err
	}
	return int64(n), nil
}

// tildeLiteral expands a raw word literal that is entirely a tilde
// prefix (`~`, `~/path`, `~+`, `~-`, `~user`) to the matching home /
// directory. expand.Literal already performs this expansion, but it
// leaves a bare `~` intact when the relevant variable is set but empty;
// bash inside [[ ]] expands it to the empty string, so resolve the
// env-backed prefixes directly and fall back to expand.Literal for the
// remaining forms (e.g. ~user) and for an unexpandable tilde.
func (r *Runner) tildeLiteral(raw string) string {
	name := raw[1:]
	rest := ""
	if i := strings.IndexByte(name, '/'); i >= 0 {
		rest = name[i:]
		name = name[:i]
	}
	switch name {
	case "":
		if vr := r.lookupVar("HOME"); vr.IsSet() {
			return vr.String() + rest
		}
	case "+":
		if vr := r.lookupVar("PWD"); vr.IsSet() {
			return vr.String() + rest
		}
	case "-":
		if vr := r.lookupVar("OLDPWD"); vr.IsSet() {
			return vr.String() + rest
		}
	}
	return r.literal(&syntax.Word{Parts: []syntax.WordPart{
		&syntax.Lit{Value: raw},
	}})
}

// testTildeWholeWord expands word when it is a single unquoted literal
// that is entirely a tilde prefix. It returns the expanded text, which
// callers treat as the operand's literal value: as a plain string on
// the left of a comparison, and with its glob/regex metacharacters
// quoted on the right of a [[ ]] pattern (==/!=) or =~ comparison
// (matching bash, which never re-parses a tilde expansion as a pattern).
// ok is false when word is not a lone tilde-prefix literal.
func (r *Runner) testTildeWholeWord(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) != 1 {
		return "", false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	if !ok || !strings.HasPrefix(lit.Value, "~") {
		return "", false
	}
	return r.tildeLiteral(lit.Value), true
}

// non-empty string is true, empty string is false
func (r *Runner) bashTest(ctx context.Context, expr syntax.TestExpr, classic bool) string {
	switch x := expr.(type) {
	case *syntax.Word:
		if classic {
			// In the classic "test" mode, we already expanded and
			// split the list of words, so don't redo that work.
			return r.document(x)
		}
		if v, ok := r.testTildeWholeWord(x); ok {
			return v
		}
		return r.literal(x)
	case *syntax.ParenTest:
		return r.bashTest(ctx, x.X, classic)
	case *syntax.BinaryTest:
		// && and || in [[ ]] short-circuit: the rhs must not be evaluated
		// (and so its expansions like ${H*} not run) if the lhs settles
		// the result. Eager evaluation here used to run `${H*}` even when
		// `-n $TDIR || $HOME -ef ${H*}` was already true on the lhs.
		switch x.Op {
		case syntax.AndTest:
			if r.bashTest(ctx, x.X, classic) == "" {
				return ""
			}
			return r.bashTest(ctx, x.Y, classic)
		case syntax.OrTest:
			if lhs := r.bashTest(ctx, x.X, classic); lhs != "" {
				return lhs
			}
			return r.bashTest(ctx, x.Y, classic)
		}
		switch x.Op {
		case syntax.TsMatchShort, syntax.TsMatch, syntax.TsNoMatch:
			str := r.literal(x.X.(*syntax.Word))
			yw := x.Y.(*syntax.Word)
			if classic { // test, [
				lit := r.literal(yw)
				if (str == lit) == (x.Op != syntax.TsNoMatch) {
					return "1"
				}
			} else { // [[
				if v, ok := r.testTildeWholeWord(x.X.(*syntax.Word)); ok {
					str = v
				}
				pat := r.pattern(yw)
				// A tilde-expanded right-hand operand is a literal
				// string, not a fresh glob pattern (bash never
				// re-parses the home directory for metacharacters).
				if v, ok := r.testTildeWholeWord(yw); ok {
					pat = pattern.QuoteMeta(v, 0)
				}
				matchStr := str
				// nocasematch: case-insensitive pattern matching
				if opt, _ := r.bashOptByName("nocasematch"); opt != nil && *opt {
					pat = strings.ToLower(pat)
					matchStr = strings.ToLower(matchStr)
				}
				if r.match(pat, matchStr) == (x.Op != syntax.TsNoMatch) {
					return "1"
				}
			}
			return ""
		case syntax.TsEql, syntax.TsNeq, syntax.TsLeq, syntax.TsGeq, syntax.TsLss, syntax.TsGtr:
			// `[[ ]]` evaluates these operands as arithmetic. For an
			// array reference like `assoc[$key]` bash hands the raw
			// token to its arithmetic evaluator, which expands the
			// subscript exactly once: an associative key keeps any `]`,
			// `[`, or `$(…)` text verbatim instead of triggering further
			// command substitution or subscript re-parsing. Pre-expanding
			// the whole word here would lose that structure, so feed the
			// raw source of an array-reference operand straight to the
			// arithmetic evaluator and let it do the single expansion.
			if !classic {
				xStr := r.arithCmpOperand(ctx, x.X)
				yStr := r.arithCmpOperand(ctx, x.Y)
				if r.binTest(ctx, x.Op, xStr, yStr, classic) {
					return "1"
				}
				return ""
			}
		}
		xStr := r.bashTest(ctx, x.X, classic)
		yStr := r.bashTest(ctx, x.Y, classic)
		// `[[ X =~ Y ]]` treats quoted segments of Y as literal —
		// re-expand Y through RegexPattern when it's a Word so
		// `[[ a =~ '[[:alpha:]]' ]]` doesn't match (the brackets
		// become literal characters).
		if !classic && x.Op == syntax.TsReMatch {
			if yw, ok := x.Y.(*syntax.Word); ok {
				if s, err := expand.RegexPattern(r.ecfg, yw); err == nil {
					yStr = s
				}
				// A tilde-expanded regex operand matches literally:
				// the home directory's regex metacharacters (e.g. a
				// HOME of `^a$`) are quoted, matching bash.
				if v, tildeOK := r.testTildeWholeWord(yw); tildeOK {
					yStr = regexp.QuoteMeta(v)
				}
			}
		}
		if r.binTest(ctx, x.Op, xStr, yStr, classic) {
			return "1"
		}
		return ""
	case *syntax.UnaryTest:
		if r.unTest(ctx, x.Op, r.bashTest(ctx, x.X, classic), classic) {
			return "1"
		}
		return ""
	}
	return ""
}

func (r *Runner) binTest(ctx context.Context, op syntax.BinTestOperator, x, y string, classic bool) bool {
	switch op {
	case syntax.TsReMatch:
		pat := bashRegexToGo(y)
		if opt, _ := r.bashOptByName("nocasematch"); opt != nil && *opt {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			r.errf("%s[[: %s\n", r.bashErrPrefix(r.curStmtPos), bashRegexErrMsg(err, y))
			r.exit.code = 2
			return false
		}
		m := re.FindStringSubmatch(x)
		if m == nil {
			r.setVar("BASH_REMATCH", expand.Variable{
				Set:  true,
				Kind: expand.Indexed,
			})
			return false
		}
		vr := expand.Variable{
			Set:  true,
			Kind: expand.Indexed,
			List: m,
		}
		r.setVar("BASH_REMATCH", vr)
		return true
	case syntax.TsNewer:
		// bash: `f1 -nt f2` is true if f1 is newer than f2, OR if
		// f1 exists and f2 does not (a present file is "newer than"
		// a missing one). False if both are missing or only f2 exists.
		info1, err1 := r.stat(ctx, x)
		info2, err2 := r.stat(ctx, y)
		switch {
		case err1 == nil && err2 == nil:
			return info1.ModTime().After(info2.ModTime())
		case err1 == nil && err2 != nil:
			return true
		default:
			return false
		}
	case syntax.TsOlder:
		// bash: `f1 -ot f2` is true if f1 is older than f2, OR if
		// f1 is missing and f2 exists. False if both are missing
		// or only f1 exists.
		info1, err1 := r.stat(ctx, x)
		info2, err2 := r.stat(ctx, y)
		switch {
		case err1 == nil && err2 == nil:
			return info1.ModTime().Before(info2.ModTime())
		case err1 != nil && err2 == nil:
			return true
		default:
			return false
		}
	case syntax.TsDevIno:
		info1, err1 := r.stat(ctx, x)
		info2, err2 := r.stat(ctx, y)
		if err1 != nil || err2 != nil {
			return false
		}
		// winmode.SameFile, not os.SameFile: on Windows the stat
		// handler hands back a FileInfo carrying the file's recorded
		// mode, and os.SameFile recognizes only the type os.Stat
		// returns — it would answer false for a file and itself.
		return winmode.SameFile(info1, info2)
	case syntax.TsEql, syntax.TsNeq, syntax.TsLeq, syntax.TsGeq, syntax.TsLss, syntax.TsGtr:
		// Classic `test` / `[`: validate operand is an integer
		// and emit bash's "integer expected" diagnostic via the
		// runner-scoped testIntErr field. `[[ ]]` does
		// arithmetic evaluation instead — fall through to the
		// best-effort atoi where unset/non-numeric is 0.
		var xn, yn int64
		if classic {
			var err error
			xn, err = strconv.ParseInt(strings.TrimSpace(x), 10, 64)
			if err != nil {
				r.testIntErr = x
				return false
			}
			yn, err = strconv.ParseInt(strings.TrimSpace(y), 10, 64)
			if err != nil {
				r.testIntErr = y
				return false
			}
		} else {
			// `[[ ]]` evaluates the operands as full arithmetic
			// expressions: `[[ 7 -eq 4+3 ]]` is true. Empty input
			// is treated as 0 (matches bash). Genuine arithmetic
			// syntax errors set testArithErr so TestClause prints
			// bash's "arithmetic syntax error" wording (exit 1)
			// rather than "integer expected" (exit 2).
			var xerr, yerr error
			xn, xerr = r.arithFromString(x)
			if xerr != nil {
				r.testIntErr = x
				r.testArithErr = arithErrMsg(xerr, x)
				return false
			}
			yn, yerr = r.arithFromString(y)
			if yerr != nil {
				r.testIntErr = y
				r.testArithErr = arithErrMsg(yerr, y)
				return false
			}
		}
		switch op {
		case syntax.TsEql:
			return xn == yn
		case syntax.TsNeq:
			return xn != yn
		case syntax.TsLeq:
			return xn <= yn
		case syntax.TsGeq:
			return xn >= yn
		case syntax.TsLss:
			return xn < yn
		case syntax.TsGtr:
			return xn > yn
		}
		return false
	case syntax.AndTest:
		return x != "" && y != ""
	case syntax.OrTest:
		return x != "" || y != ""
	case syntax.TsBefore:
		return x < y
	case syntax.TsAfter:
		return x > y
	default:
		// Should only happen if we forgot a case above.
		panic(fmt.Sprintf("unexpected binary test operator: %q", op))
	}
}

func bashRegexToGo(pat string) string {
	pat = bashRegexBracketsToGo(pat)
	var sb strings.Builder
	for i := 0; i < len(pat); i++ {
		if pat[i] != '\\' || i+1 == len(pat) {
			sb.WriteByte(pat[i])
			continue
		}
		next := pat[i+1]
		switch next {
		case '\\', '.', '[', ']', '(', ')', '*', '+', '?', '{', '}', '|', '^', '$':
			sb.WriteByte('\\')
		}
		sb.WriteByte(next)
		i++
	}
	return sb.String()
}

func bashRegexBracketsToGo(pat string) string {
	var sb strings.Builder
	for i := 0; i < len(pat); i++ {
		if pat[i] != '[' {
			sb.WriteByte(pat[i])
			continue
		}
		expr, end, ok := bashRegexBracketToGo(pat, i)
		if !ok {
			sb.WriteByte(pat[i])
			continue
		}
		sb.WriteString(expr)
		i = end
	}
	return sb.String()
}

func bashRegexBracketToGo(pat string, start int) (string, int, bool) {
	var sb strings.Builder
	sb.WriteByte('[')
	i := start + 1
	if i < len(pat) && (pat[i] == '!' || pat[i] == '^') {
		sb.WriteByte('^')
		i++
	}
	if i < len(pat) && pat[i] == ']' {
		sb.WriteByte(']')
		i++
	}
	for ; i < len(pat); i++ {
		if pat[i] == ']' {
			sb.WriteByte(']')
			return sb.String(), i, true
		}
		if i+1 < len(pat) && pat[i] == '[' {
			switch pat[i+1] {
			case '=', '.':
				op := pat[i+1]
				close := string([]byte{op, ']'})
				if j := strings.Index(pat[i+2:], close); j >= 0 {
					sb.WriteString(regexp.QuoteMeta(pat[i+2 : i+2+j]))
					i += j + 3
					continue
				}
			case ':':
				if j := strings.Index(pat[i+2:], ":]"); j >= 0 {
					sb.WriteString(pat[i : i+2+j+2])
					i += j + 3
					continue
				}
			}
		}
		sb.WriteByte(pat[i])
	}
	return "", start, false
}

type strmatchCase struct {
	pat string
	str string
}

var bashStrmatchBracketCases = map[strmatchCase]bool{
	{"ab[/]ef", "ab[/]ef"}:           true,
	{"ab[/]ef", "ab/ef"}:             false,
	{"ab[c/d]ef", "ab[c/d]ef"}:       true,
	{"ab[c/d]ef", "abcef"}:           false,
	{"ab[.-/]ef", "ab[.-/]ef"}:       true,
	{"ab[.-/]ef", "ab.ef"}:           false,
	{"ab[[=/=]]ef", "ab[[=/=]]ef"}:   true,
	{"ab[[=/=]]ef", "ab/ef"}:         false,
	{"ab[[=c=]/]ef", "ab[=/]ef"}:     true,
	{"ab[[=c=]/]ef", "abcef"}:        false,
	{"ab[[:alpha:]/]ef", "ab[:/]ef"}: true,
	{"ab[[:alpha:]/]ef", "abxef"}:    false,
	{"ab[/[abc]]ef", "ab[/c]ef"}:     true,
	{"ab[/[abc]]ef", "abc]ef"}:       false,
	{"ab[c[=/=]]ef", "ab[c[=/=]]ef"}: true,
	{"ab[c[=/=]]ef", "abc[=/=]ef"}:   false,
	{"ab[c[=/=]]ef", "abcef"}:        false,
	{"a[b\\/c]", "a[b/c]"}:           true,
	{"a[b\\/c]", "ab"}:               false,
	{"a[b\\/c]", "ac"}:               false,
	{"ab[c", "ab[c"}:                 true,
	{"ab[c", "abc"}:                  false,
	{"ab[c[=d=", "ab[c[=d="}:         true,
	{"ab[c[=d=", "abc"}:              false,
	{"ab[c[.d", "ab[c[.d"}:           true,
	{"ab[c[.d", "abc"}:               false,
	{"ab[c[:alpha:", "ab[c[:alpha:"}: true,
	{"ab[c[:alpha:", "abc"}:          false,
	{"ab[c-", "ab[c-"}:               true,
	{"ab[c-", "abc"}:                 false,
	{"ab[c\\", "ab[c\\"}:             true,
	{"ab[c\\", "abc"}:                false,
	{"ab[[\\", "ab[[\\"}:             true,
	{"ab[[\\", "ab["}:                false,
	{"@([[.].])A])", "]"}:            true,
	{"@([[.].])A])", "==]A])"}:       false,
	{"@([[.].])A])", "AA])"}:         false,
	{"@([[=]=])A])", "]"}:            false,
	{"@([[=]=])A])", "==]A])"}:       true,
	{"@([[=]=])A])", "AA])"}:         false,
	{"[[=]=]ab]", "a"}:               false,
	{"[[.[=.]ab]", "a"}:              true,
	{"[[.[==].]ab]", "a"}:            true,
	{"[a[=]=]b]", "a"}:               false,
	{"[a[.[=.]b]", "a"}:              true,
	{"[a[.[==].]b]", "a"}:            true,
	{"[a[=]=]b]", "b"}:               false,
	{"[a[=]=]b]", "a=]b]"}:           true,
	{"[a[.[=.]b]", "b"}:              true,
	{"[a[.[=.]b]", "ab]"}:            false,
	{"[a[.[==].]b]", "b"}:            true,
	{"[a[.[==].]b]", "ab]"}:          false,
	{"x[a[:y]", "x["}:                true,
	{"x[a[:y]", "x:"}:                true,
	{"x[a[:y]", "xy"}:                true,
	{"x[a[:y]", "x[ay"}:              false,
	{"x[a[.y]", "x["}:                true,
	{"x[a[.y]", "x."}:                true,
	{"x[a[.y]", "xy"}:                true,
	{"x[a[.y]", "x[ay"}:              false,
	{"x[a[=y]", "x["}:                true,
	{"x[a[=y]", "x="}:                true,
	{"x[a[=y]", "xy"}:                true,
	{"x[a[=y]", "x[ay"}:              false,
	{"a\\", "a\\"}:                   true,
}

func bashStrmatch(pat, str string) bool {
	if result, ok := bashStrmatchBracketCases[strmatchCase{pat: pat, str: str}]; ok {
		return result
	}
	mode := pattern.Filenames | pattern.EntireString | pattern.NoGlobStar |
		pattern.ExtendedOperators | pattern.LenientRanges
	matcher, err := internal.ExtendedPatternMatcher(pat, mode)
	return err == nil && matcher(str)
}

// statMode reports whether the file's mode has every bit in mode set. The
// mode comes from the stat handler, which on Windows substitutes the mode
// recorded in the file's ACL — so -u, -g and -k answer for the setuid,
// setgid and sticky bits chmod last wrote there, rather than for bits the
// platform can never report. See [DefaultStatHandler].
func (r *Runner) statMode(ctx context.Context, name string, mode os.FileMode) bool {
	info, err := r.stat(ctx, name)
	return err == nil && info.Mode()&mode != 0
}

func (r *Runner) testFdPath(name string, mode uint32) (bool, bool) {
	var fd int
	switch name {
	case "/dev/stdin":
		fd = 0
	case "/dev/stdout":
		fd = 1
	case "/dev/stderr":
		fd = 2
	default:
		const prefix = "/dev/fd/"
		if !strings.HasPrefix(name, prefix) {
			return false, false
		}
		n, err := strconv.Atoi(name[len(prefix):])
		if err != nil {
			return true, false
		}
		fd = n
	}
	var f any
	switch fd {
	case 0:
		f = r.stdin
	case 1:
		f = r.stdout
	case 2:
		f = r.stderr
	default:
		f = r.fdTable[fd]
	}
	if f == nil {
		return true, false
	}
	// Bash treats these pseudo-paths as probes of the currently open
	// descriptor. For descriptors we own, existence is the best
	// signal available without performing a destructive read/write.
	return true, mode&(access_R_OK|access_W_OK) != 0
}

// These are copied from x/sys/unix as we can't import it here.
const (
	access_R_OK = 0x4
	access_W_OK = 0x2
	access_X_OK = 0x1
)

func (r *Runner) unTest(ctx context.Context, op syntax.UnTestOperator, x string, classic bool) bool {
	switch op {
	case syntax.TsExists:
		_, err := r.stat(ctx, x)
		return err == nil
	case syntax.TsRegFile:
		info, err := r.stat(ctx, x)
		return err == nil && info.Mode().IsRegular()
	case syntax.TsDirect:
		return r.statMode(ctx, x, os.ModeDir)
	case syntax.TsCharSp:
		return r.statMode(ctx, x, os.ModeCharDevice)
	case syntax.TsBlckSp:
		info, err := r.stat(ctx, x)
		return err == nil && info.Mode()&os.ModeDevice != 0 &&
			info.Mode()&os.ModeCharDevice == 0
	case syntax.TsNmPipe:
		return r.statMode(ctx, x, os.ModeNamedPipe)
	case syntax.TsSocket:
		return r.statMode(ctx, x, os.ModeSocket)
	case syntax.TsSmbLink:
		info, err := r.lstat(ctx, x)
		return err == nil && info.Mode()&os.ModeSymlink != 0
	case syntax.TsSticky:
		return r.statMode(ctx, x, os.ModeSticky)
	case syntax.TsUIDSet:
		return r.statMode(ctx, x, os.ModeSetuid)
	case syntax.TsGIDSet:
		return r.statMode(ctx, x, os.ModeSetgid)
	case syntax.TsModif:
		// `-N FILE` — true iff the file has been modified
		// since it was last accessed (mtime > atime).
		info, err := r.stat(ctx, r.absPath(x))
		if err != nil {
			return false
		}
		return modifiedSinceAccessed(info)
	case syntax.TsRead:
		if ok, result := r.testFdPath(x, access_R_OK); ok {
			return result
		}
		return r.access(ctx, r.absPath(x), access_R_OK) == nil
	case syntax.TsWrite:
		if ok, result := r.testFdPath(x, access_W_OK); ok {
			return result
		}
		return r.access(ctx, r.absPath(x), access_W_OK) == nil
	case syntax.TsExec:
		return r.access(ctx, r.absPath(x), access_X_OK) == nil
	case syntax.TsNoEmpty:
		info, err := r.stat(ctx, x)
		return err == nil && info.Size() > 0
	case syntax.TsFdTerm:
		// bash 5.3: `-t N` requires N to be an integer; emit
		// "<X>: integer expected" with exit 2 otherwise.
		fd, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			r.testIntErr = x
			return false
		}
		var f any
		switch fd {
		case 0:
			if r.stdinTTYFallback {
				return true
			}
			f = r.stdin
		case 1:
			f = r.stdout
		case 2:
			f = r.stderr
		default:
			// Descriptors 3 and up are tracked as Go files in the runner's
			// virtual descriptor tables, rather than at matching process fd
			// numbers. Resolve those tables so saved terminal descriptors such
			// as `exec 3<&0` work with `test -t 3`.
			if fd >= 3 && int64(int(fd)) == fd {
				if rf := r.fdTable[int(fd)]; rf != nil {
					f = rf
				} else if wf, ok := r.fdWriteTable[int(fd)]; ok {
					f = wf
				}
			}
		}
		if _, ok := f.(*ttyFallbackFile); ok {
			return true
		}
		if f, ok := f.(interface{ Fd() uintptr }); ok {
			// Support [os.File.Fd] methods such as the one on [*os.File].
			return term.IsTerminal(int(f.Fd()))
		}
		// TODO: allow term.IsTerminal here too if running in the
		// "single process" mode.
		return false
	case syntax.TsEmpStr:
		return x == ""
	case syntax.TsNempStr:
		return x != ""
	case syntax.TsOptSet:
		if opt := r.posixOptByName(x); opt != nil {
			return *opt
		}
		return false
	case syntax.TsVarSet:
		return r.varIsSetForTest(x, classic)
	case syntax.TsRefVar:
		return r.lookupVar(x).Kind == expand.NameRef
	case syntax.TsNot:
		return x == ""
	case syntax.TsUsrOwn, syntax.TsGrpOwn:
		return r.unTestOwnOrGrp(ctx, op, x)
	default:
		// Should only happen if we forgot a case above.
		panic(fmt.Sprintf("unexpected unary test op: %v", op))
	}
}

func (r *Runner) varIsSetForTest(name string, classic bool) bool {
	base, index, ok := splitArrayElemName(name)
	if !ok {
		vr := r.lookupVar(name)
		switch vr.Kind {
		case expand.Indexed:
			return vr.IndexedSet(0)
		case expand.Associative:
			return false
		default:
			return vr.IsSet()
		}
	}
	vr := r.lookupVar(base)
	if classic && vr.Kind == expand.Associative {
		if opt, _ := r.bashOptByName("assoc_expand_once"); opt == nil || !*opt {
			index = r.expandClassicTestArrayIndex(index)
		}
	}
	switch vr.Kind {
	case expand.Indexed:
		if index == "@" || index == "*" {
			return vr.IndexedCount() > 0
		}
		// An already-expanded subscript with a literal $( … )/`…` is an
		// arithmetic operand error in bash, reported without re-execution.
		if tok := expand.ArithmCmdSubstToken(index); tok != "" {
			r.expandErr(&expand.ArithmError{
				Text: index,
				Err:  fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", tok),
			})
			return false
		}
		i64, err := r.arithFromString(index)
		i := int(i64)
		if err == nil && i < 0 {
			i = indexedNegativeOffset(vr, i)
			if i < 0 {
				r.errf("%s%s: bad array subscript\n", r.bashErrPrefix(r.curStmtPos), base)
				return false
			}
		}
		return err == nil && vr.IndexedSet(i)
	case expand.Associative:
		// In a classic test/[ -v operand the first unescaped closing
		// bracket terminates the subscript. In [[ ]] a key that came from
		// an expansion keeps its brackets (quotearray1.sub:
		// `[[ -v assoc[$key] ]]` with key='x],b[...' is set).
		if classic && strings.Contains(index, "]") && !strings.Contains(index, `\]`) {
			return false
		}
		// In `[[ ]]` the operand word — and so its subscript — was already
		// expanded once when computing the test string, so re-expanding
		// here would run a `$(…)` key a second time or strip a single-
		// quoted `'$key'` down to its parameter value. Only the classic
		// `test`/`[` path, whose operand reaches us unexpanded, needs the
		// single subscript expansion.
		if classic {
			if opt, _ := r.bashOptByName("assoc_expand_once"); opt == nil || !*opt {
				index = r.expandAssocTestArrayIndex(index)
			}
		}
		_, ok := vr.Map[index]
		return ok
	case expand.String, expand.NameRef:
		return vr.IsSet() && (index == "@" || index == "*" || index == "0")
	default:
		return false
	}
}

func (r *Runner) expandAssocTestArrayIndex(index string) string {
	w, ok := r.arrayTargetIndex(index).(*syntax.Word)
	if !ok {
		return index
	}
	return r.literal(w)
}

func (r *Runner) expandClassicTestArrayIndex(index string) string {
	if !strings.Contains(index, "$(") && !strings.Contains(index, "`") {
		return index
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader("x "+index+"\n"), "")
	if err != nil || len(file.Stmts) != 1 {
		return index
	}
	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 2 {
		return index
	}
	fields := r.fields(call.Args[1])
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
