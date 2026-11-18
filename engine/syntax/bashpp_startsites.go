// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file is the Bash++ start-site DECISION TABLE, extracted from the parser
// rather than embedded in it.
//
// What it is: the predicate the command-position dispatch consults to decide
// whether a Go region opens here. Keeping it separate is not tidiness. It makes
// the decision unit-testable against the measured corpus in
// bashpp-tests/tools/startsites WITHOUT a parser, and it keeps the edit to
// certification-owned parser.go down to the dispatch calls that ask it.
//
// What it is NOT: the dispatch itself. RecognizeStartSite answers "does a Go
// region open here"; whether a particular body is one Bash++ supports is a
// SECOND question, decided where the completed command is reclassified — see
// bashppUntypedDecl/bashppTypeDecl in bashpp_decl.go, bashppShortDecl in
// bashpp_short.go, and bashppFuncForm/bashppDeferForm in bashpp_func.go, each
// of which consults this table and then applies its own body gate. Because the
// P1 sites are now wired, LangBashPP diverges from LangBash exactly at the
// published Class E rows and nowhere else (TestBashPPMatchesBash).
//
// THE BOUNDED-LOOKAHEAD PROPERTY, and why it is load-bearing.
//
// sh's parser is streaming and non-backtracking: it reads the input in chunks
// and cannot rewind. An earlier attempt to fix an unrelated defect by scanning
// ahead for a matching bracket failed three times for exactly this reason —
// the scan ran off the end of the buffered chunk, and the conservative answer
// at a chunk boundary silently restored the old behaviour, which is the worst
// possible failure because it looks like success.
//
// So every recognizer below is required to decide within maxLookahead bytes of
// the command position, and a test asserts it. A site that cannot is not
// "harder to implement" — it is UNIMPLEMENTABLE in this parser without a
// different mechanism, and must be identified now rather than discovered at
// the third failed attempt.
//
// StartGoIf is the known exception and is deliberately excluded from the
// bounded set: a shell `if` may legally carry `{` as the last word of its
// condition and continue with `then`, so only the absence of `then` after the
// MATCHING brace commits. That is unbounded by construction. It is recorded
// here as owed design work, not scheduled as ordinary implementation.

// maxLookahead is the byte budget a start-site recognizer may examine past the
// command position. It is deliberately small: the whole point is that the
// decision fits comfortably inside one buffered chunk.
const maxLookahead = 64

// StartSiteMatch is the verdict for one command position.
type StartSiteMatch struct {
	Site  StartSite
	Class SiteClass

	// Bounded reports whether the verdict was reached inside maxLookahead.
	// A false here on a supposedly-bounded site is a bug in the table, not a
	// property of the input, and the test suite treats it as one.
	Bounded bool
}

// noMatch is the fail-safe verdict: stay shell. Every early return uses it, so
// that a recognizer which falls through cannot accidentally claim a shape.
var noMatch = StartSiteMatch{Site: StartNone}

// RecognizeStartSite decides whether a Bash++ region opens at the start of src,
// which the caller supplies as the source text from the command position.
//
// It answers only for the P1 (Day-1) sites. Every later phase's shape returns
// noMatch, which is the correct answer for an unimplemented phase: a Class E
// shape must keep running as the shell command it is today, and a Class R
// shape is a bash syntax error either way.
//
// The caller must not treat a match as permission to parse: the shell escapes
// (`command var …`, `"var" …`) are handled BEFORE this is consulted, because
// they work by making the position not a Bash++ command position at all.
func RecognizeStartSite(src string) StartSiteMatch {
	// The source fence is recognized on the WHOLE opener word before the
	// bounded truncation below: an opener of 64 or more tildes carries its
	// language past maxLookahead, so a truncated copy reads as bare tildes
	// and wrongly falls back to shell. This keeps the bound that matters —
	// the caller passes the already-lexed word, so no unbuffered input is
	// scanned — while every other site below keeps the byte budget.
	// Unlike ordinary shell commands, indentation is itself an escape for a
	// naked source fence; that check lives inside recognizeSourceFence and
	// runs before the general command-position trim below.
	if recognizeSourceFence(src) {
		return StartSiteMatch{Site: StartSource, Class: ClassE, Bounded: true}
	}
	if len(src) > maxLookahead {
		src = src[:maxLookahead]
	}
	s := strings.TrimLeft(src, " \t")
	if s == "" {
		return noMatch
	}
	if rest, ok := cutKeyword(s, "agentic"); ok {
		if strings.HasPrefix(rest, "{") && (len(rest) == 1 || strings.ContainsRune(" \t\r\n;", rune(rest[1]))) {
			return StartSiteMatch{Site: StartAgentic, Class: ClassE, Bounded: true}
		}
		if recognizeFuncDecl(rest) {
			return StartSiteMatch{Site: StartAgentic, Class: ClassR, Bounded: true}
		}
		if tail, ok := cutKeyword(rest, "func"); ok && strings.HasPrefix(tail, "(") {
			return StartSiteMatch{Site: StartAgentic, Class: ClassR, Bounded: true}
		}
		if tail, ok := cutKeyword(rest, "function"); ok {
			name := leadingIdent(tail)
			if name != "" && strings.HasPrefix(strings.TrimLeft(tail[len(name):], " \t"), "(") {
				return StartSiteMatch{Site: StartAgentic, Class: ClassR, Bounded: true}
			}
		}
		return noMatch
	}

	if m := recognizeKeywordDecl(s); m.Site != StartNone {
		return m
	}
	if recognizeImportPrefix(s) {
		return StartSiteMatch{Site: StartImport, Class: ClassE, Bounded: true}
	}
	if recognizeFuncDecl(s) {
		return StartSiteMatch{Site: StartFunc, Class: ClassR, Bounded: true}
	}
	if recognizeFuncLit(s) {
		return StartSiteMatch{Site: StartFuncLit, Class: ClassR, Bounded: true}
	}
	if recognizeDefer(s) {
		return StartSiteMatch{Site: StartDefer, Class: ClassR, Bounded: true}
	}
	if m := recognizeShortDecl(s); m.Site != StartNone {
		return m
	}
	if m := recognizeGoCall(s); m.Site != StartNone {
		return m
	}
	return noMatch
}

func recognizeSourceFence(src string) bool {
	if src == "" || src[0] == ' ' || src[0] == '\t' {
		return false
	}
	line, _, _ := strings.Cut(src, "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	opener := fields[0]
	n := 0
	for n < len(opener) && opener[n] == '~' {
		n++
	}
	if n < 3 || n == len(opener) || !bashppValidSourceLanguage(opener[n:]) {
		return false
	}
	_, _, ok := bashppSourceFenceTail(opener[n:], fields[1:])
	return ok
}

// bashppSourceFenceTail reads what follows the opener word:
//
//	[as ALIAS] [!RUNNER]        as !RUNNER is short for as RUNNER !RUNNER
//
// It returns the alias and runner, or ok=false when the words are not that
// shape — a Class E near miss that stays ordinary shell text.
func bashppSourceFenceTail(language string, fields []string) (alias, runner string, ok bool) {
	if len(fields) > 0 && fields[0] == "as" {
		if len(fields) < 2 {
			return "", "", false
		}
		if strings.HasPrefix(fields[1], "!") {
			runner = fields[1][1:]
			alias = runner
			if !bashppValidSourceRunner(runner) || !bashppValidSourceAlias(language, alias) {
				return "", "", false
			}
			return alias, runner, len(fields) == 2
		}
		alias = fields[1]
		if !bashppValidSourceAlias(language, alias) {
			return "", "", false
		}
		fields = fields[2:]
	}
	if len(fields) > 0 {
		if len(fields) != 1 || !strings.HasPrefix(fields[0], "!") {
			return "", "", false
		}
		runner = fields[0][1:]
		if !bashppValidSourceRunner(runner) {
			return "", "", false
		}
	}
	return alias, runner, true
}

// A runner names a function or a registered command, so it may carry the
// dashes command names do; it never carries a path separator or a glob.
func bashppValidSourceRunner(runner string) bool {
	if runner == "" || runner[0] == '-' || runner[len(runner)-1] == '-' {
		return false
	}
	for i := 0; i < len(runner); i++ {
		c := runner[i]
		if c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			continue
		}
		return false
	}
	return true
}

func recognizeImportPrefix(s string) bool {
	rest, ok := cutKeyword(s, "import")
	if !ok {
		return false
	}
	if strings.HasPrefix(rest, `"`) {
		return true
	}
	if strings.HasPrefix(rest, "_") || strings.HasPrefix(rest, ".") {
		rest = strings.TrimLeft(rest[1:], " \t")
		return strings.HasPrefix(rest, `"`)
	}
	alias := leadingIdent(rest)
	if alias == "" || isGoReservedWord(alias) {
		return false
	}
	rest = strings.TrimLeft(rest[len(alias):], " \t")
	return strings.HasPrefix(rest, `"`)
}

// recognizeFuncDecl handles a Go-form function declaration: `func name(`.
//
// Always Class R. `func` is an ordinary command word in bash, so `func name (`
// is two words before a `(`, which is only legal as the function-definition
// form `name ()` where the name is the FIRST word — it is not here. A NAME is
// required between the keyword and the `(`: `func(` with no name is the bash
// function definition `func() { … }` and must stay shell, exactly as `f()`
// does at [recognizeGoCall].
func recognizeFuncDecl(s string) bool {
	rest, ok := cutKeyword(s, "func")
	if !ok {
		return false
	}
	name := leadingIdent(rest)
	if name == "" || isGoReservedWord(name) {
		return false
	}
	rest = strings.TrimLeft(rest[len(name):], " \t")
	return strings.HasPrefix(rest, "(")
}

// recognizeDefer handles the Go-call form of defer: `defer f(` / `defer x.y(`.
//
// `defer` splits like `:=` does, but only one half is claimable. `defer f(x)`
// is Class R — the parenthesis after `defer f` is a bash syntax error, as it is
// after any two command words — so claiming it takes nothing away. `defer
// cleanup` and bare `defer` are Class E commands bash runs today and must keep
// running, so the signal is precisely the trailing `(` on a selector callee.
func recognizeDefer(s string) bool {
	rest, ok := cutKeyword(s, "defer")
	if !ok {
		return false
	}
	// `defer func(…) { … }()` defers a literal instead of a named call. The
	// EMPTY parameter list is claimable here although it is not at a command
	// position: `defer func()` puts the parenthesis after two words, which
	// bash rejects however the list is spelled, so the shape that forces
	// unbounded lookahead at a command position — the bash function
	// definition `func() { … }` — cannot occur after `defer`.
	if lit, _ := bashppFuncLitPrefix(rest); lit {
		return true
	}
	ident := leadingSelector(rest)
	if ident == "" {
		return false
	}
	head, _, _ := strings.Cut(ident, ".")
	if isGoReservedWord(head) {
		return false
	}
	rest = rest[len(ident):]
	return strings.HasPrefix(rest, "(")
}

// recognizeFuncLit handles a function literal at a COMMAND position, which is
// the immediately-invoked form: `func(n int) { … }(1)`.
//
// The empty parameter list is deliberately excluded, and the exclusion is a
// compatibility requirement rather than a simplification. `func() { … }` is
// the bash definition of a function NAMED `func`, which stock bash accepts
// today, so the two shapes are told apart only by the `(` after the matching
// `}` — unbounded lookahead of exactly the kind this file exists to forbid.
// A literal with a parameter — `func(n int)` — is a bash syntax error whatever
// follows, so it commits on the prefix alone. A parameterless invocation is
// written `_ := func() { … }()` or bound first, where the `:=` has already
// opened the region; that is also why the result type is not consulted here,
// since the parser holds only the one byte past the parenthesis and could not
// reach a `func() int` verdict even if the table offered one.
func recognizeFuncLit(s string) bool {
	lit, empty := bashppFuncLitPrefix(s)
	return lit && !empty
}

// bashppFuncLitPrefix reports whether s opens a function literal, and whether
// its parameter list is empty. The two answers are separate because the empty
// list is claimable at some sites and not at others; see [recognizeFuncLit].
func bashppFuncLitPrefix(s string) (isLit, emptyParams bool) {
	if !strings.HasPrefix(s, "func") {
		return false, false
	}
	rest := strings.TrimLeft(s[len("func"):], " \t")
	if !strings.HasPrefix(rest, "(") {
		return false, false
	}
	rest = strings.TrimLeft(rest[1:], " \t")
	if rest == "" {
		// The list is still open at the lookahead budget; the fail-safe answer
		// is shell.
		return false, false
	}
	if !strings.HasPrefix(rest, ")") {
		return true, false
	}
	return true, true
}

// recognizeKeywordDecl handles var, const and type.
//
// All three are Class E — `var`, `const` and `type` are all ordinary command
// words in bash today (`type` is even a builtin), so each carries a published
// table row and a `command …` escape. The signal is the keyword FOLLOWED BY a
// blank and an identifier: `var x` commits, bare `var` does not, because a
// script calling a program named `var` with no arguments must keep working.
func recognizeKeywordDecl(s string) StartSiteMatch {
	for kw, site := range map[string]StartSite{
		"var":   StartVar,
		"const": StartConst,
		"type":  StartTypeDecl,
	} {
		rest, ok := cutKeyword(s, kw)
		if !ok {
			continue
		}
		// An identifier must follow. Without one this is a plain command
		// invocation and must stay shell. A Go keyword is not an identifier,
		// so `var if = 1` and `const return = 1` are refused here rather than
		// downstream: no phase of Bash++ can ever declare a name Go reserves,
		// while bash runs both today as ordinary commands.
		name := leadingIdent(rest)
		if name == "" || isGoReservedWord(name) {
			continue
		}
		return StartSiteMatch{Site: site, Class: ClassE, Bounded: true}
	}
	return noMatch
}

// recognizeShortDecl handles the := sites, which SPLIT across both classes.
//
// This split is the single most useful thing the measured corpus produced, and
// it is invisible to inspection: `x := 42` is Class E (bash runs a command `x`
// with arguments `:=` and `42`), while `x := f()` is Class R (the parenthesis
// after a word is already a syntax error). One start site, two rows, opposite
// compatibility risk. The class is recorded on the node so no later phase has
// to re-derive it.
func recognizeShortDecl(s string) StartSiteMatch {
	rest, ok := cutShortDeclLhs(s)
	if !ok {
		return noMatch
	}
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		// `x :=` with nothing after it. Bash runs command `x` with argument
		// `:=`, so the fail-safe answer is shell.
		return noMatch
	}
	// A call on the right-hand side makes the whole shape a bash syntax error,
	// hence Class R. Anything else (a scalar, a composite literal, a tuple) is
	// a shape bash accepts today, hence Class E.
	class := ClassE
	if hasCallShape(rest) {
		class = ClassR
	}
	return StartSiteMatch{Site: StartShortDecl, Class: class, Bounded: true}
}

// recognizeGoCall handles a bare call in command position: f(1, 2), x.y.z().
//
// Always Class R. Bash's only `word (` production is the function definition
// `name ()`, so a call with arguments, or with a dotted selector, is already a
// parse error. An empty argument list is excluded precisely BECAUSE `f ()` is
// the function-definition form and is legal shell.
func recognizeGoCall(s string) StartSiteMatch {
	ident := leadingSelector(s)
	if ident == "" {
		return noMatch
	}
	// A callee is an identifier too, so a Go keyword cannot head one: `if(x)`
	// is not a Go call and must not be claimed as one.
	head, _, _ := strings.Cut(ident, ".")
	if isGoReservedWord(head) {
		return noMatch
	}
	rest := s[len(ident):]
	if !strings.HasPrefix(rest, "(") {
		return noMatch
	}
	// `f()` with a dot is a Go selector call and cannot be a bash function
	// definition, so it commits. `f()` without one is exactly the shape of a
	// bash function definition and must stay shell.
	if strings.HasPrefix(rest, "()") && !strings.Contains(ident, ".") {
		return noMatch
	}
	return StartSiteMatch{Site: StartGoCall, Class: ClassR, Bounded: true}
}

// cutKeyword returns the remainder after kw, but only when kw stands as a
// complete word. `variable=1` must not match the `var` keyword.
func cutKeyword(s, kw string) (string, bool) {
	if !strings.HasPrefix(s, kw) {
		return "", false
	}
	rest := s[len(kw):]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", false
	}
	return strings.TrimLeft(rest, " \t"), true
}

// cutShortDeclLhs matches `name :=` or `a, b :=` and returns the remainder.
func cutShortDeclLhs(s string) (string, bool) {
	rest := s
	for {
		name := leadingIdent(rest)
		// Same rule as the keyword declarations: a Go keyword cannot be a
		// declaration target, so `if := 1` is a shell command and nothing else.
		if name == "" || isGoReservedWord(name) {
			return "", false
		}
		rest = strings.TrimLeft(rest[len(name):], " \t")
		if strings.HasPrefix(rest, ",") {
			rest = strings.TrimLeft(rest[1:], " \t")
			continue
		}
		if strings.HasPrefix(rest, ":=") {
			return rest[2:], true
		}
		return "", false
	}
}

// hasCallShape reports whether the right-hand side opens with a call, which is
// what makes a := shape Class R rather than Class E.
func hasCallShape(s string) bool {
	ident := leadingSelector(s)
	if ident == "" {
		return false
	}
	return strings.HasPrefix(s[len(ident):], "(")
}

func leadingIdent(s string) string {
	i, first := 0, true
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 || !bashppIdentRune(r, first) {
			break
		}
		i += size
		first = false
	}
	return s[:i]
}

// leadingSelector matches an identifier possibly followed by .field chains.
func leadingSelector(s string) string {
	i := 0
	for {
		part := leadingIdent(s[i:])
		if part == "" {
			return ""
		}
		i += len(part)
		if i < len(s) && s[i] == '.' {
			i++
			continue
		}
		return s[:i]
	}
}

func bashppIdentRune(r rune, first bool) bool {
	return r == '_' || unicode.IsLetter(r) || !first && unicode.IsDigit(r)
}

// BashPPValidIdent reports whether s is a complete Go identifier. It is
// intentionally separate from ValidName: POSIX shell names remain ASCII-only,
// while committed Bash++ grammar follows Go's Unicode letter/digit rules.
func BashPPValidIdent(s string) bool {
	if s == "" || isGoReservedWord(s) {
		return false
	}
	first := true
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 || !bashppIdentRune(r, first) {
			return false
		}
		s = s[size:]
		first = false
	}
	return true
}

// goReservedWords is the complete set of Go keywords, as of the Go 1.x
// specification's "Keywords" section. All 25 are listed rather than only the
// ones a Day-1 shape could plausibly collide with, because the closed set is
// the specification's and a hand-picked subset would be a second, drifting
// answer to a question Go has already answered.
//
// WHY THE RECOGNIZER CARES. A Go keyword is not an identifier, so `var if = 1`
// is not a declaration in any phase of Bash++ — it is not a Go region that
// happens to be unsupported, it is not a Go region at all. Bash, meanwhile,
// runs it today as a three-argument command. Letting the recognizer fire there
// would put a shape that can never be claimed onto the Class E ledger, and the
// dispatch would have to refuse it a second time to avoid changing what a
// working script does. One refusal, at the point where the name is required to
// be an identifier, is the whole of it.
var goReservedWords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// isGoReservedWord reports whether s is a Go keyword and therefore may never
// be used as a declared name, a short-declaration target or a callee.
func isGoReservedWord(s string) bool { return goReservedWords[s] }
