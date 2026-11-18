// Copyright (c) 2016, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package syntax

import (
	"bytes"
	"fmt"
	"io"
	"iter"
	"math/bits"
	"slices"
	"strings"
	"unicode/utf8"
)

// ParserOption is a function which can be passed to NewParser
// to alter its behavior. To apply option to existing Parser
// call it directly, for example KeepComments(true)(parser).
type ParserOption func(*Parser)

// KeepComments makes the parser parse comments and attach them to
// nodes, as opposed to discarding them.
func KeepComments(enabled bool) ParserOption {
	return func(p *Parser) { p.keepComments = enabled }
}

// LangVariant describes a shell language variant to use when tokenizing and
// parsing shell code. The zero value is [LangBash].
//
// This type implements [flag.Value] so that it can be used as a CLI flag.
type LangVariant int

// TODO(v4): the zero value should be left as an unset and invalid value.
// TODO(v4): the type should be uint32 now that we use this as a bitset;
// an unsigned integer is clearer, and being agnostic to uint size avoids issues.

const (
	// LangBash corresponds to the GNU Bash language, as described in its
	// manual at https://www.gnu.org/software/bash/manual/bash.html.
	//
	// We currently follow Bash version 5.2.
	//
	// Its string representation is "bash".
	LangBash LangVariant = 1 << iota

	// LangPOSIX corresponds to the POSIX Shell language, as described at
	// https://pubs.opengroup.org/onlinepubs/9699919799/utilities/V3_chap02.html.
	//
	// Its string representation is "posix" or "sh".
	LangPOSIX

	// LangMirBSDKorn corresponds to the MirBSD Korn Shell, also known as
	// mksh, as described at http://www.mirbsd.org/htman/i386/man1/mksh.htm.
	// Note that it shares some features with Bash, due to the shared
	// ancestry that is ksh.
	//
	// We currently follow mksh version 59.
	//
	// Its string representation is "mksh".
	LangMirBSDKorn

	// LangBats corresponds to the Bash Automated Testing System language,
	// as described at https://github.com/bats-core/bats-core. Note that
	// it's just a small extension of the Bash language.
	//
	// Its string representation is "bats".
	LangBats

	// LangZsh corresponds to the Z shell, as described at https://www.zsh.org/.
	//
	// Note that its support in the syntax package is experimental and
	// incomplete for now. See https://github.com/mvdan/sh/issues/120.
	//
	// We currently follow Zsh version 5.9.
	//
	// Its string representation is "zsh".
	LangZsh

	// LangAuto corresponds to automatic language detection,
	// commonly used by end-user applications like shfmt,
	// which can guess a file's language variant given its filename or shebang.
	//
	// At this time, [Variant] does not support LangAuto.
	LangAuto

	// LangBashPP corresponds to bash++, a strict superset of [LangBash] used
	// by the bashy shell. Its grammar is Bash's, byte for byte: at this level
	// it adds no keywords, operators, or parse rules, so any input parses and
	// prints identically under LangBash and LangBashPP. It exists so that the
	// layers above the parser — [github.com/bashsharp/bashsharp/engine/expand] and
	// [github.com/bashsharp/bashsharp/engine/interp] — have a dialect to gate behavioral extensions
	// on, such as object-valued variables. Consumers which do not opt in stay
	// on [LangBash] and are unaffected.
	//
	// It is declared after [LangAuto] so that the pre-existing variant bits
	// keep their values.
	//
	// Its string representation is "bashpp".
	LangBashPP

	// langBashLegacy is what [LangBash] used to be, when it was zero.
	// We still support it for the sake of backwards compatibility.
	langBashLegacy LangVariant = 0

	// langResolvedVariants contains all known variants except [LangAuto],
	// which is meant to resolve to another variant.
	//
	// [LangBashPP] is deliberately absent: the table-driven tests iterate over
	// this set expecting one distinct grammar per bit, and bash++ shares
	// Bash's grammar exactly. TestBashPPMatchesBash covers it instead, by
	// asserting the two variants agree over the whole corpus.
	langResolvedVariants = LangBash | LangPOSIX | LangMirBSDKorn | LangBats | LangZsh

	// langResolvedVariantsCount is langResolvedVariants.count() as a constant.
	// TODO: Can we compute this as a constant expression somehow?
	// For example, if we had log2, we could do log2(LangAuto).
	langResolvedVariantsCount = 5

	// langBashLike contains Bash plus all variants which are extensions of it.
	langBashLike = LangBash | LangBats | LangBashPP

	// langBashExact contains the variants whose grammar is Bash's exactly.
	// It backs the parse rules which are Bash-only among the bash-like
	// variants, i.e. those which [LangBats] deliberately does not share.
	langBashExact = LangBash | LangBashPP
)

// Variant changes the shell language variant that the parser will
// accept.
//
// The passed language variant must be one of the constant values defined in
// this package.
func Variant(l LangVariant) ParserOption {
	switch l {
	case langBashLegacy:
		l = LangBash
	case LangBash, LangPOSIX, LangMirBSDKorn, LangBats, LangZsh, LangBashPP:
	case LangAuto:
		panic("LangAuto is not supported by the parser at this time")
	default:
		panic(fmt.Sprintf("unknown shell language variant: %#b", l))
	}
	return func(p *Parser) { p.lang = l }
}

// PosixMode applies POSIX behavioral parse rules on top of the chosen
// [Variant], mirroring bash's posixly_correct / `bash --posix`: bash syntax
// (arrays, ${v:off:len}, ${v^^}, …) is still parsed, but single quotes inside
// ${...} are treated literally and function names are validated strictly. Use
// this with [Variant]([LangBash]) to parse exactly what `bash --posix` parses
// — as opposed to [LangPOSIX], which is the stricter pure-POSIX grammar that
// rejects bash extensions outright.
func PosixMode(enabled bool) ParserOption {
	return func(p *Parser) { p.posixMode = enabled }
}

// posixBehavior reports whether POSIX behavioral parse rules apply: either the
// pure-POSIX grammar is selected, or [PosixMode] layered bash --posix rules on
// top of a bash-family grammar.
func (p *Parser) posixBehavior() bool {
	return p.lang.in(LangPOSIX) || p.posixMode
}

func (l LangVariant) String() string {
	switch l {
	case langBashLegacy, LangBash:
		return "bash"
	case LangPOSIX:
		return "posix"
	case LangMirBSDKorn:
		return "mksh"
	case LangBats:
		return "bats"
	case LangZsh:
		return "zsh"
	case LangBashPP:
		return "bashpp"
	case LangAuto:
		return "auto"
	}
	return "unknown shell language variant"
}

func (l *LangVariant) Set(s string) error {
	switch s {
	case "bash":
		*l = LangBash
	case "posix", "sh", "dash":
		*l = LangPOSIX
	case "mksh":
		*l = LangMirBSDKorn
	case "bats":
		*l = LangBats
	case "zsh":
		*l = LangZsh
	case "bashpp", "bash++":
		*l = LangBashPP
	case "auto":
		*l = LangAuto
	default:
		return fmt.Errorf("unknown shell language variant: %q", s)
	}
	return nil
}

func (l LangVariant) in(l2 LangVariant) bool {
	return l&l2 == l
}

func (l LangVariant) count() int {
	return bits.OnesCount32(uint32(l))
}

func (l LangVariant) index() int {
	return bits.TrailingZeros32(uint32(l))
}

func (l LangVariant) bits() iter.Seq[LangVariant] {
	return func(yield func(LangVariant) bool) {
		for n := LangVariant(1); n < langResolvedVariants; n <<= 1 {
			if l&n == 0 {
				continue
			}
			if !yield(n) {
				return
			}
		}
	}
}

// StopAt configures the lexer to stop at an arbitrary word, treating it
// as if it were the end of the input. It can contain any characters
// except whitespace, and cannot be over four bytes in size.
//
// This can be useful to embed shell code within another language, as
// one can use a special word to mark the delimiters between the two.
//
// As a word, it will only apply when following whitespace or a
// separating token. For example, StopAt("$$") will act on the inputs
// "foo $$" and "foo;$$", but not on "foo '$$'".
//
// The match is done by prefix, so the example above will also act on
// "foo $$bar".
func StopAt(word string) ParserOption {
	if len(word) > 4 {
		panic("stop word can't be over four bytes in size")
	}
	if strings.ContainsAny(word, " \t\n\r") {
		panic("stop word can't contain whitespace characters")
	}
	return func(p *Parser) { p.stopAt = []byte(word) }
}

// RecoverErrors allows the parser to skip up to a maximum number of
// errors in the given input on a best-effort basis.
// This can be useful to tab-complete an interactive shell prompt,
// or when providing diagnostics on slightly incomplete shell source.
//
// Currently, this only helps with mandatory tokens from the shell grammar
// which are not present in the input. They result in position fields
// or nodes whose position report [Pos.IsRecovered] as true.
//
// For example, given the input
//
//	(foo |
//
// the result will contain two recovered positions; first, the pipe requires
// a statement to follow, and as [Stmt.Pos] reports, the entire node is recovered.
// Second, the subshell needs to be closed, so [Subshell.Rparen] is recovered.
func RecoverErrors(maximum int) ParserOption {
	return func(p *Parser) { p.recoverErrorsMax = maximum }
}

// HeredocEOFWarning, when set, recovers from an unclosed here-document
// at end-of-file: instead of failing the parse, the parser treats the
// content read so far as the heredoc body and calls the provided
// callback with the warning details. This matches bash 5.3's behaviour
// of emitting `warning: here-document at line N delimited by
// end-of-file (wanted "STOP")` to stderr while continuing parsing.
//
// The callback's `startLine` is the line of the heredoc opener
// (`<<STOP`); `eofLine` is the line where EOF was reached; `stop` is
// the expected closing marker.
func HeredocEOFWarning(fn func(startLine, eofLine int, stop string)) ParserOption {
	return func(p *Parser) { p.heredocEOFWarning = fn }
}

// HeredocComsubWarning, when set, reports bash 5.3's parse-time
// `command substitution: %d unterminated here-document%s` warning: a
// `$(...)` was closed while here-documents opened inside it had not yet
// read their bodies. The callback receives the line of the closing
// parenthesis and the number of pending here-documents. Parsing
// continues either way: the bodies are read from the lines following
// the substitution, matching bash.
func HeredocComsubWarning(fn func(line, count int)) ParserOption {
	return func(p *Parser) { p.heredocComsubWarning = fn }
}

// NewParser allocates a new [Parser] and applies any number of options.
func NewParser(options ...ParserOption) *Parser {
	p := &Parser{
		lang: LangBash,
	}
	for _, opt := range options {
		opt(p)
	}
	return p
}

// Parse reads and parses a shell program with an optional name. It
// returns the parsed program if no issues were encountered. Otherwise,
// an error is returned. Reads from r are buffered.
//
// Parse can be called more than once, but not concurrently. That is, a
// Parser can be reused once it is done working.
func (p *Parser) Parse(r io.Reader, name string) (*File, error) {
	p.reset()
	p.f = &File{Name: name}
	p.src = r
	p.rune()
	p.next()
	p.f.Stmts, p.f.Last = p.stmtList()
	if p.err == nil {
		// EOF immediately after heredoc word so no newline to
		// trigger the parsing error.
		p.doHeredocs()
	}
	if p.err == nil && p.lang.in(LangBashPP) {
		p.bashppValidateLabels(p.f)
	}
	return p.f, p.err
}

// Stmts is a pre-iterators API which now wraps [Parser.StmtsSeq].
//
// Deprecated: use [Parser.StmtsSeq].
func (p *Parser) Stmts(r io.Reader, fn func(*Stmt) bool) error {
	for stmt, err := range p.StmtsSeq(r) {
		if err != nil {
			return err
		}
		if !fn(stmt) {
			break
		}
	}
	return nil
}

// StmtsSeq reads and parses statements one at a time via an iterator.
func (p *Parser) StmtsSeq(r io.Reader) iter.Seq2[*Stmt, error] {
	p.reset()
	p.f = &File{}
	p.src = r
	return func(yield func(*Stmt, error) bool) {
		var pending []*Stmt
		flush := func(err error) bool {
			for _, stmt := range pending {
				if !yield(stmt, err) {
					return false
				}
			}
			pending = pending[:0]
			return true
		}
		p.rune()
		p.next()
		p.stmts(func(stmt *Stmt, err error) bool {
			if stmt == nil {
				return flush(err) && yield(nil, err)
			}
			if len(p.heredocs) > p.buriedHdocs {
				pending = append(pending, stmt)
				return true
			}
			return flush(err) && yield(stmt, err)
		})
		if p.err == nil {
			// EOF immediately after heredoc word so no newline to
			// trigger the parsing error.
			p.doHeredocs()
		}
		if !flush(p.err) {
			return
		}
		if p.err != nil {
			// Yield any final error from the parser.
			yield(nil, p.err)
		}
	}
}

type wrappedReader struct {
	p  *Parser
	rd io.Reader

	lastLine    int64
	accumulated []*Stmt
	yield       func([]*Stmt, error) bool
}

func (w *wrappedReader) Read(p []byte) (n int, err error) {
	// If we lexed a newline for the first time, we just finished a line, so
	// we may need to give a callback for the edge cases below not covered
	// by [Parser.Stmts].
	if (w.p.r == '\n' || w.p.r == escNewl) && w.p.line > w.lastLine {
		if w.p.Incomplete() {
			// Incomplete statement; call back to print "> ".
			if !w.yield(w.accumulated, w.p.err) {
				return 0, io.EOF
			}
		} else if len(w.accumulated) == 0 {
			// Nothing was parsed; call back to print another "$ ".
			if !w.yield(nil, w.p.err) {
				return 0, io.EOF
			}
		}
		w.lastLine = w.p.line
	}
	return w.rd.Read(p)
}

// Interactive is a pre-iterators API which now wraps [Parser.InteractiveSeq].
//
// Deprecated: use [Parser.InteractiveSeq].
func (p *Parser) Interactive(r io.Reader, fn func([]*Stmt) bool) error {
	for stmts, err := range p.InteractiveSeq(r) {
		if err != nil {
			return err
		}
		if !fn(stmts) {
			break
		}
	}
	return nil
}

// InteractiveSeq implements what is necessary to parse statements in an
// interactive shell. The parser will call the given function under two
// circumstances outlined below.
//
// If a line containing any number of statements is parsed, the function will be
// called with said statements.
//
// If a line ending in an incomplete statement is parsed, the function will be
// called with any fully parsed statements, and [Parser.Incomplete] will return true.
//
// One can imagine a simple interactive shell implementation as follows:
//
//	fmt.Fprintf(os.Stdout, "$ ")
//	parser.Interactive(os.Stdin, func(stmts []*syntax.Stmt) bool {
//		if parser.Incomplete() {
//			fmt.Fprintf(os.Stdout, "> ")
//			return true
//		}
//		run(stmts)
//		fmt.Fprintf(os.Stdout, "$ ")
//		return true
//	}
//
// If the callback function returns false, parsing is stopped and the function
// is not called again.
func (p *Parser) InteractiveSeq(r io.Reader) iter.Seq2[[]*Stmt, error] {
	return func(yield func([]*Stmt, error) bool) {
		w := wrappedReader{p: p, rd: r, yield: yield}
		for stmts, err := range p.StmtsSeq(&w) {
			w.accumulated = append(w.accumulated, stmts)
			if err != nil {
				if !yield(w.accumulated, err) {
					break
				}
				// If the caller wishes, they can continue in the presence of parse errors.
				// TODO: does this even work? Write tests for it. This only came up
				continue
			}
			// We finished parsing a statement and we're at a newline token,
			// so we finished fully parsing a number of statements. Call
			// back to run the statements and print "$ ".
			if p.tok == _Newl {
				if !yield(w.accumulated, nil) {
					break
				}
				w.accumulated = w.accumulated[:0]
				// The callback above would already print "$ ", so we
				// don't want the subsequent wrappedReader.Read to cause
				// another "$ " print thinking that nothing was parsed.
				w.lastLine = w.p.line + 1
			}
		}
	}
}

// Words is a pre-iterators API which now wraps [Parser.WordsSeq].
//
// Deprecated: use [Parser.WordsSeq].
func (p *Parser) Words(r io.Reader, fn func(*Word) bool) error {
	for w, err := range p.WordsSeq(r) {
		if err != nil {
			return err
		}
		if !fn(w) {
			break
		}
	}
	return nil
}

// WordsSeq reads and parses a sequence of words alongside any error encountered.
//
// Newlines are skipped, meaning that multi-line input will work fine. If the
// parser encounters a token that isn't a word, such as a semicolon, an error
// will be returned.
//
// Note that the lexer doesn't currently tokenize spaces, so it may need to read
// a non-space byte such as a newline or a letter before finishing the parsing
// of a word. This will be fixed in the future.
func (p *Parser) WordsSeq(r io.Reader) iter.Seq2[*Word, error] {
	p.reset()
	p.f = &File{}
	p.src = r
	return func(yield func(*Word, error) bool) {
		p.rune()
		p.next()
		for {
			p.got(_Newl)
			w := p.getWord()
			if w == nil {
				if p.tok != _EOF {
					p.curErr("%#q is not a valid word", p.tok)
				}
				if p.err != nil {
					yield(nil, p.err)
				}
				return
			}
			if !yield(w, nil) {
				return
			}
		}
	}
}

// Document parses a single here-document word. That is, it parses the input as
// if they were lines following a <<EOF redirection.
//
// In practice, this is the same as parsing the input as if it were within
// double quotes, but without having to escape all double quote characters.
// Similarly, the here-document word parsed here cannot be ended by any
// delimiter other than reaching the end of the input.
func (p *Parser) Document(r io.Reader) (*Word, error) {
	p.reset()
	p.f = &File{}
	p.src = r
	p.rune()
	p.quote = hdocBody
	p.hdocStops = [][]byte{[]byte("MVDAN_CC_SH_SYNTAX_EOF")}
	p.parsingDoc = true
	p.next()
	w := p.getWord()
	return w, p.err
}

// Arithmetic parses a single arithmetic expression. That is, as if the input
// were within the $(( and )) tokens.
func (p *Parser) Arithmetic(r io.Reader) (ArithmExpr, error) {
	p.reset()
	p.f = &File{}
	p.src = r
	p.rune()
	p.quote = arithmExpr
	p.next()
	expr := p.arithmExpr(false)
	return expr, p.err
}

// Parser holds the internal state of the parsing mechanism of a
// program.
type Parser struct {
	src io.Reader
	bs  []byte // current chunk of read bytes
	bsp uint   // offset within [Parser.bs] for the rune after [Parser.r]
	r   rune   // next rune; [utf8.RuneSelf] when it went past EOF, or we stopped
	w   int    // width of [Parser.r]

	f *File

	spaced bool // whether [Parser.tok] has whitespace on its left

	err     error // lexer/parser error
	readErr error // got a read error, but bytes left
	readEOF bool  // [Parser.src] already gave us an [io.EOF] error

	tok token  // current token
	val string // current value (valid if tok is _Lit*)

	pendingTok  token
	pendingPos  Pos
	pendingTok2 token
	pendingPos2 Pos

	// position of [Parser.r], to be converted to [Parser.pos] later
	offs, line, col int64

	pos Pos // position of tok

	quote   quoteState // current lexer state
	eqlOffs int        // position of '=' in [Parser.val] when [Parser.tok].isLit is true

	keepComments bool
	lang         LangVariant
	// posixMode mirrors bash's posixly_correct: it applies POSIX *behavioral*
	// parse rules (single quotes literal inside ${...}, stricter function
	// names) ON TOP OF the current LangVariant, WITHOUT switching to the
	// stricter LangPOSIX grammar. This is how `bash --posix` works — it keeps
	// bash syntax (arrays, ${v:off:len}, ${v^^}, …) and only toggles those
	// targeted posix rules. Set via [PosixMode].
	posixMode bool

	// mb is the LC_CTYPE multibyte charset used to advance non-UTF-8
	// multibyte characters (e.g. Big5), mirroring bash's MB_CUR_MAX. It
	// is resolved lazily from the process locale the first time an
	// invalid-UTF-8 byte is seen (mbInit guards that one-shot lookup), so
	// ASCII/UTF-8 parsing pays no cost. See mbchar.go.
	mb     mbCharset
	mbInit bool

	stopAt []byte

	recoveredErrors  int
	recoverErrorsMax int

	// heredocEOFWarning, when non-nil, opts in to bash 5.3's behaviour
	// of treating an unclosed here-document at end-of-file as a
	// warning instead of a fatal parse error. See [HeredocEOFWarning].
	heredocEOFWarning func(startLine, eofLine int, stop string)

	// heredocComsubWarning, when non-nil, reports a `$(...)` closing
	// with here-documents opened inside it still pending. See
	// [HeredocComsubWarning].
	heredocComsubWarning func(line, count int)

	forbidNested bool

	// list of pending heredoc bodies
	buriedHdocs int
	heredocs    []*Redirect

	hdocStops      [][]byte // stack of end words for open heredocs
	hdocStartLines []int    // stack of gather lines for open heredocs
	hdocEOFLine    int      // last content line read when a heredoc body hit EOF

	parsingDoc bool // true if using [Parser.Document]

	// openNodes tracks how many entire statements or words we're currently parsing.
	// A non-zero number means that we require certain tokens or words before
	// reaching EOF, used for [Parser.Incomplete].
	openNodes int
	// bashppFuncDepth counts the Bash++ func bodies currently open. A `return`
	// is only a Go-form [BashPPReturn] while this is non-zero; everywhere else
	// it stays the ordinary shell builtin, which is Class E. It is saved and
	// zeroed across a nested shell function or subshell body so a `return` there
	// keeps its shell meaning.
	bashppFuncDepth int
	// bashppControls is the stack of committed typed control statements whose
	// bodies are being parsed. Bare branch words remain shell commands until a
	// typed for/range/switch/select has committed the surrounding region.
	bashppControls []bashppControlKind
	// bashppChanCopy keeps the already committed channel region recognizable
	// inside a shell copy. It deliberately does not keep bashppFuncDepth: P3
	// return/defer ownership stops at the copy boundary.
	bashppChanCopy bool
	// bashppFuncNames records declarations seen so far, allowing unambiguous
	// zero-argument calls (`f()`) without stealing classic `f() { ... }` shell
	// definitions from the parser.
	bashppFuncNames map[string]bool
	// openBquotes is how many levels of backquotes are open at the moment.
	openBquotes int
	// bquoteDblQuoted reports whether the innermost open backquote command
	// substitution was opened directly inside a double-quoted string. bash
	// then also removes a backslash before a `"` in the backquote body, so
	// `"`echo \"x\"`"` runs `echo "x"` (syntactic quotes) rather than
	// passing literal quotes through.
	bquoteDblQuoted bool
	// openComsubs is how many `$(...)` command substitutions are open at
	// the moment. Used to keep heredoc close-paren recovery (a body line
	// like `EOF)` standing in for the missing terminator) from firing
	// inside plain `( ... )` subshells, where bash instead reads the
	// body through to end-of-file.
	openComsubs int

	// paramExpExpDblQuoted records whether the current ${paramOPword}
	// substitute word was entered while parsing inside double quotes.
	paramExpExpDblQuoted bool
	paramExpExpOp        ParExpOperator
	paramExpOuterQuote   quoteState
	// paramExpReplFirst is set while the lexer reads the first token of a
	// ${var/pat/repl} pattern. Bash treats a `/` as the very first
	// character of the pattern as a literal, not the pattern/replacement
	// separator: `${a///a/}` parses as pattern `/a`, empty replacement.
	paramExpReplFirst bool
	assignIndexWords  bool
	rawAssignIndex    bool
	nakedAssignIndex  bool

	// lastBquoteEsc is how many times the last backquote token was escaped
	lastBquoteEsc int
	// bquoteEscapedByte reports whether the current byte had a legacy
	// backquote escape removed by the lexer.
	bquoteEscapedByte bool

	rxOpenParens int
	rxFirstPart  bool

	accComs []Comment
	curComs *[]Comment

	litBatch  []Lit
	wordBatch []wordAlloc

	readBuf [bufSize]byte
	litBuf  [bufSize]byte
	litBs   []byte
}

// Incomplete reports whether the parser needs more input bytes
// to finish properly parsing a statement or word.
//
// It is only safe to call while the parser is blocked on a read. For an example
// use case, see [Parser.Interactive].
func (p *Parser) Incomplete() bool {
	// If there are any open nodes, we need to finish them.
	// If we're constructing a literal, we need to finish it.
	return p.openNodes > 0 || len(p.litBs) > 0
}

const bufSize = 1 << 10

func (p *Parser) reset() {
	p.tok, p.val = illegalTok, ""
	p.pendingTok, p.pendingPos = illegalTok, Pos{}
	p.pendingTok2, p.pendingPos2 = illegalTok, Pos{}
	p.eqlOffs = 0
	p.bs, p.bsp = nil, 0
	p.offs, p.line, p.col = 0, 1, 1
	p.r, p.w = 0, 0
	p.err, p.readErr, p.readEOF = nil, nil, false
	p.quote, p.forbidNested = noState, false
	p.openNodes = 0
	p.bashppFuncNames = nil
	p.recoveredErrors = 0
	p.heredocs, p.buriedHdocs = p.heredocs[:0], 0
	p.hdocStops = nil
	p.hdocStartLines = nil
	p.hdocEOFLine = 0
	p.parsingDoc = false
	p.openBquotes = 0
	p.openComsubs = 0
	p.accComs = nil
	p.accComs, p.curComs = nil, &p.accComs
	p.litBatch = nil
	p.wordBatch = nil
	p.litBs = nil
}

// nextPos returns the position of the next rune, [Parser.r].
func (p *Parser) nextPos() Pos {
	// Basic protection against offset overflow;
	// note that an offset of 0 is valid, so we leave the maximum.
	offset := min(p.offs+int64(p.bsp)-int64(p.w), offsetMax)
	var line, col uint
	if p.line <= lineMax {
		line = uint(p.line)
	}
	if p.col <= colMax {
		col = uint(p.col)
	}
	return NewPos(uint(offset), line, col)
}

func (p *Parser) lit(pos Pos, val string) *Lit {
	if len(p.litBatch) == 0 {
		p.litBatch = make([]Lit, 32)
	}
	l := &p.litBatch[0]
	p.litBatch = p.litBatch[1:]
	l.ValuePos = pos
	l.ValueEnd = p.nextPos()
	l.Value = val
	return l
}

type wordAlloc struct {
	word  Word
	parts [1]WordPart
}

func (p *Parser) wordAnyNumber() *Word {
	if len(p.wordBatch) == 0 {
		p.wordBatch = make([]wordAlloc, 32)
	}
	alloc := &p.wordBatch[0]
	p.wordBatch = p.wordBatch[1:]
	w := &alloc.word
	w.Parts = p.wordParts(alloc.parts[:0])
	return w
}

// assignAsWord converts a Naked array assignment parse (from getAssign
// when needEqual=true and no = follows) back into a regular word token.
// The bracket content stays unquoted: for a non-assignment, brackets retain
// their ordinary glob meaning. Word boundaries are already represented by
// the AST, so spaces recovered inside the candidate do not split the word.
func (p *Parser) assignAsWord(as *Assign) *Word {
	if index, ok := as.Index.(*Word); ok {
		parts := []WordPart{
			as.Name,
			&Lit{Value: "[", ValuePos: as.Name.ValueEnd, ValueEnd: posAddCol(as.Name.ValueEnd, 1)},
		}
		parts = append(parts, index.Parts...)
		parts = append(parts, &Lit{Value: "]", ValuePos: index.End(), ValueEnd: posAddCol(index.End(), 1)})
		if as.Append {
			parts = append(parts, &Lit{Value: "+"})
		}
		return &Word{Parts: parts}
	}

	var src strings.Builder
	src.WriteString(as.Name.Value)
	if as.Index != nil {
		src.WriteByte('[')
		p.printArithmExpr(&src, as.Index)
		src.WriteByte(']')
	}
	if as.Append {
		src.WriteByte('+')
	}

	// Re-lex the recovered source as a word, not as an assignment candidate.
	// This restores the ordinary word-part semantics of expansions and glob
	// brackets. Spaces inside the candidate are represented by multiple Words
	// by WordsSeq; join those parts back into the single word Bash keeps here.
	wordParser := NewParser(Variant(p.lang), PosixMode(p.posixMode))
	var parts []WordPart
	for word, err := range wordParser.WordsSeq(strings.NewReader(src.String())) {
		if err != nil {
			break
		}
		if len(parts) > 0 {
			parts = append(parts, &Lit{Value: " "})
		}
		parts = append(parts, word.Parts...)
	}
	if len(parts) > 0 {
		return &Word{Parts: parts}
	}
	return p.wordOne(p.lit(as.Name.ValuePos, src.String()))
}

// printArithmExpr serializes an arithmetic expression back to source
// text using the shell syntax printer.
func (p *Parser) printArithmExpr(w io.Writer, expr ArithmExpr) {
	printer := NewPrinter()
	switch e := expr.(type) {
	case *Word:
		printer.Print(w, e)
	default:
		printer.Print(w, e)
	}
}

func (p *Parser) wordOne(part WordPart) *Word {
	if len(p.wordBatch) == 0 {
		p.wordBatch = make([]wordAlloc, 32)
	}
	alloc := &p.wordBatch[0]
	p.wordBatch = p.wordBatch[1:]
	w := &alloc.word
	w.Parts = alloc.parts[:1]
	w.Parts[0] = part
	return w
}

func (p *Parser) call(w *Word) *CallExpr {
	var alloc struct {
		ce CallExpr
		ws [4]*Word
	}
	ce := &alloc.ce
	ce.Args = alloc.ws[:1]
	ce.Args[0] = w
	return ce
}

type quoteState uint32

const (
	// The initial state of the parser.
	noState quoteState = 1 << iota

	// Used when parsing parameter expansions; use with [Parser.rune],
	// [Parser.next] always returns [illegalTok].
	runeByRune

	// unquotedWordCont exists purely so that the '#' in $foo#bar does not
	// get parsed as a comment; it's a tiny variation on [noState].
	unquotedWordCont

	subCmd
	// subCmdBrace is like subCmd but for bash 5.3's `${ cmd; }` funsub
	// and mksh's `${|cmd;}` valsub: the body is a list of statements,
	// but the closing delimiter is `}` rather than `)`, so the lexer
	// must treat `}` as a word boundary inside the body.
	subCmdBrace
	subCmdBckquo
	dblQuotes
	hdocWord
	hdocBody
	hdocBodyTabs
	arithmExpr
	arithmExprLet
	arithmExprCmd
	testExpr
	testExprRegexp
	switchCase
	paramExpArithm
	paramExpRepl
	paramExpExp
	arrayElems

	allKeepSpaces = runeByRune | paramExpRepl | dblQuotes | hdocBody |
		hdocBodyTabs | paramExpRepl | paramExpExp
	allRegTokens = noState | unquotedWordCont | subCmd | subCmdBrace | subCmdBckquo | hdocWord |
		switchCase | arrayElems | testExpr
	allArithmExpr = arithmExpr | arithmExprLet | arithmExprCmd | paramExpArithm
	allParamExp   = paramExpArithm | paramExpRepl | paramExpExp
)

type saveState struct {
	quote                quoteState
	buriedHdocs          int
	paramExpExpDblQuoted bool
	paramExpExpOp        ParExpOperator
	paramExpOuterQuote   quoteState
}

func (p *Parser) preNested(quote quoteState) (s saveState) {
	s.quote, s.buriedHdocs = p.quote, p.buriedHdocs
	s.paramExpExpDblQuoted = p.paramExpExpDblQuoted
	s.paramExpExpOp = p.paramExpExpOp
	s.paramExpOuterQuote = p.paramExpOuterQuote
	p.buriedHdocs, p.quote = len(p.heredocs), quote
	return s
}

func (p *Parser) postNested(s saveState) {
	p.quote, p.buriedHdocs = s.quote, s.buriedHdocs
	p.paramExpExpDblQuoted = s.paramExpExpDblQuoted
	p.paramExpExpOp = s.paramExpExpOp
	p.paramExpOuterQuote = s.paramExpOuterQuote
}

func (p *Parser) posixParamExpExpSingleQuotesLiteral() bool {
	if !p.posixBehavior() || !p.paramExpExpDblQuoted {
		return false
	}
	switch p.paramExpExpOp {
	case AlternateUnset, AlternateUnsetOrNull,
		DefaultUnset, DefaultUnsetOrNull,
		AssignUnset, AssignUnsetOrNull,
		ErrorUnset, ErrorUnsetOrNull:
		return true
	}
	return false
}

func (p *Parser) unquotedWordBytes(w *Word) ([]byte, bool) {
	buf := make([]byte, 0, 4)
	didUnquote := false
	for _, wp := range w.Parts {
		var quoted bool
		buf, quoted = p.unquotedWordPart(buf, wp, false)
		// POSIX: if ANY part of the here-document delimiter word is
		// quoted, the body is not expanded. Accumulate quotedness
		// across parts rather than letting a later unquoted part (e.g.
		// the "foo" in `<<''foo`) clobber an earlier quoted one.
		if quoted {
			didUnquote = true
		}
	}
	return buf, didUnquote
}

func (p *Parser) unquotedWordPart(buf []byte, wp WordPart, quotes bool) (_ []byte, quoted bool) {
	switch wp := wp.(type) {
	case *Lit:
		for i := 0; i < len(wp.Value); i++ {
			if b := wp.Value[i]; b == '\\' && !quotes {
				if i++; i < len(wp.Value) {
					buf = append(buf, wp.Value[i])
				}
				quoted = true
			} else {
				buf = append(buf, b)
			}
		}
	case *SglQuoted:
		buf = append(buf, []byte(wp.Value)...)
		quoted = true
	case *DblQuoted:
		for _, wp2 := range wp.Parts {
			buf, _ = p.unquotedWordPart(buf, wp2, true)
		}
		quoted = true
	}
	return buf, quoted
}

func (p *Parser) doHeredocs() {
	hdocs := p.heredocs[p.buriedHdocs:]
	if len(hdocs) == 0 {
		// Nothing do do; don't even issue a read.
		return
	}
	// Bash numbers here-document diagnostics from the line whose
	// newline triggered gathering the bodies (its line_number when
	// gather_here_documents runs), not from the line of the `<<`
	// opener — they differ when a command substitution sits between
	// the opener and the gathering newline.
	gatherLine := int(p.line)
	p.rune() // consume '\n', since we know p.tok == _Newl
	old := p.quote
	p.heredocs = p.heredocs[:p.buriedHdocs]
	for i, r := range hdocs {
		if p.err != nil {
			break
		}
		if i > 0 {
			gatherLine = int(p.line)
		}
		p.quote = hdocBody
		if r.Op == DashHdoc {
			p.quote = hdocBodyTabs
		}
		stop, quoted := p.unquotedWordBytes(r.Word)
		if r.Op == DashHdoc {
			stop = bytes.TrimLeft(stop, "\t")
		}
		p.hdocStops = append(p.hdocStops, stop)
		p.hdocStartLines = append(p.hdocStartLines, gatherLine)
		p.hdocEOFLine = 0
		if i > 0 && p.r == '\n' {
			p.rune()
		}
		if quoted {
			r.Hdoc = p.quotedHdocWord()
		} else {
			p.next()
			r.Hdoc = p.getWord()
		}
		if stop := p.hdocStops[len(p.hdocStops)-1]; stop != nil {
			if p.heredocEOFWarning != nil {
				// Bash 5.3 emits a warning and treats whatever was
				// read up to EOF as the heredoc body, then continues
				// parsing. r.Hdoc is already populated by getWord /
				// quotedHdocWord above. The reported eofLine is the
				// last line with content before EOF; with an empty
				// body that is the gather line itself.
				eofLine := p.hdocEOFLine
				if eofLine == 0 {
					eofLine = gatherLine
				}
				p.heredocEOFWarning(gatherLine, eofLine, string(stop))
			} else {
				p.posErr(r.Pos(), "unclosed here-document %#q", stop)
			}
		}
		p.hdocStops = p.hdocStops[:len(p.hdocStops)-1]
		p.hdocStartLines = p.hdocStartLines[:len(p.hdocStartLines)-1]
	}
	p.quote = old
}

func (p *Parser) warnHdocEOFAtComsubClose(stop []byte) {
	if p.heredocEOFWarning == nil || len(p.hdocStartLines) == 0 {
		return
	}
	startLine := p.hdocStartLines[len(p.hdocStartLines)-1]
	p.heredocEOFWarning(startLine, int(p.nextPos().Line()), string(stop))
}

func (p *Parser) got(tok token) bool {
	if p.tok == tok {
		p.next()
		return true
	}
	return false
}

func (p *Parser) gotRsrv(val string) (Pos, bool) {
	pos := p.pos
	if p.tok == _LitWord && p.val == val {
		p.next()
		return pos, true
	}
	return pos, false
}

func (p *Parser) recoverError() bool {
	if p.recoveredErrors < p.recoverErrorsMax {
		p.recoveredErrors++
		return true
	}
	return false
}

type noQuote string

func (s noQuote) Format(f fmt.State, verb rune) {
	f.Write([]byte(s))
}

func (t token) Format(f fmt.State, verb rune) {
	if t < _realTokenBoundary && verb == 'q' {
		// EOF, Lit and the others should not be quoted in error messages
		// as they are not real shell syntax like `if` or `{`.
		f.Write([]byte(t.String()))
	} else {
		fmt.Fprintf(f, fmt.FormatString(f, verb), t.String())
	}
}

func (p *Parser) followErr(pos Pos, left, right any) {
	p.posErr(pos, "%#q must be followed by %#q", left, right)
}

func (p *Parser) followErrExp(pos Pos, left any) {
	p.followErr(pos, left, noQuote("an expression"))
}

func (p *Parser) follow(lpos Pos, left string, tok token) {
	if !p.got(tok) {
		p.followErr(lpos, left, tok)
	}
}

func (p *Parser) followRsrv(lpos Pos, left, val string) Pos {
	pos, ok := p.gotRsrv(val)
	if !ok {
		if p.recoverError() {
			return recoveredPos
		}
		p.followErr(lpos, left, val)
	}
	return pos
}

func (p *Parser) followStmts(left string, lpos Pos, stops ...string) ([]*Stmt, []Comment) {
	// Language variants disallowing empty command lists:
	// * [LangPOSIX]: "A list is a sequence of one or more AND-OR lists...".
	// * [LangBash]: "A list is a sequence of one or more pipelines..."
	//
	// Language variants allowing empty command lists:
	// * [LangZsh]: "A list is a sequence of zero or more sublists...".
	// * [LangMirBSDKorn]: "Lists of commands can be created by separating pipelines...";
	//   note that the man page is not explicit, but the shell clearly allows e.g. `{ }`.
	if p.got(semicolon) {
		if p.lang.in(LangZsh | LangMirBSDKorn) {
			return nil, nil // allow an empty list
		}
		p.followErr(lpos, left, noQuote("a statement list"))
		return nil, nil
	}
	stmts, last := p.stmtList(stops...)
	if len(stmts) < 1 {
		if p.lang.in(LangZsh | LangMirBSDKorn) {
			return nil, nil // allow an empty list
		}
		if p.recoverError() {
			return []*Stmt{{Position: recoveredPos}}, nil
		}
		if left == "(" && p.tok == _EOF && p.lang.in(langBashExact) {
			// Bash 5.3 reports an empty subshell running into
			// end-of-file as an EOF error pointing back at the `(`
			// line, like the heredoc-EOF case in [Parser.subshell].
			p.posErr(p.nextPos(), "syntax error: unexpected end of file from `(' command on line %d", lpos.Line())
		}
		p.followErr(lpos, left, noQuote("a statement list"))
	}
	return stmts, last
}

func (p *Parser) followWordTok(tok token, pos Pos) *Word {
	w := p.getWord()
	if w == nil {
		if p.recoverError() {
			return p.wordOne(&Lit{ValuePos: recoveredPos})
		}
		p.followErr(pos, tok, noQuote("a word"))
	}
	return w
}

// testFollowWord reads the operand word of a `[[ ]]` test operator. An
// unquoted `]]` here is the conditional terminator, not an operand, so we
// report the operator as missing its argument rather than consuming `]]`
// as the operand and then failing to find the closing `]]`. A bare `<` or
// `>` (redirection token) already terminates an operand this way; `]]`
// did not, so e.g. `[[ -z ]]` mis-parsed.
func (p *Parser) testFollowWord(tok token, pos Pos) *Word {
	if p.tok == _LitWord && p.val == "]]" {
		p.followErr(pos, tok, noQuote("a word"))
	}
	return p.followWordTok(tok, pos)
}

func (p *Parser) stmtEnd(n Node, start, end string) Pos {
	pos, ok := p.gotRsrv(end)
	if !ok {
		if p.recoverError() {
			return recoveredPos
		}
		p.posErr(n.Pos(), "%#q statement must end with %#q", start, end)
	}
	return pos
}

func (p *Parser) quoteErr(lpos Pos, quote token) {
	if p.tok == _EOF {
		p.posErr(lpos, "unexpected EOF while looking for matching \x60%s'", quote)
		return
	}
	p.posErr(lpos, "reached %#q without closing quote %#q", p.tok, quote)
}

func (p *Parser) matchingErr(lpos Pos, left, right token) {
	if p.tok == _EOF {
		// bash renders this as "matching `}'" — open backtick, close
		// apostrophe (the interp/builtin.go EOF messages hardcode the
		// same). %#q would Go-backquote both sides ("`}`"), which the
		// arith/posixexp/exportfunc fixtures flag.
		p.posErr(lpos, "unexpected EOF while looking for matching \x60%s'", right)
	} else {
		p.posErr(lpos, "reached %#q without matching %#q with %#q", p.tok, left, right)
	}
}

func (p *Parser) matched(lpos Pos, left, right token) Pos {
	pos := p.pos
	if !p.got(right) {
		if p.recoverError() {
			return recoveredPos
		}
		p.matchingErr(lpos, left, right)
	}
	return pos
}

func (p *Parser) errPass(err error) {
	if p.err == nil {
		p.err = err
		p.bsp = uint(len(p.bs)) + 1
		p.r = utf8.RuneSelf
		p.w = 1
		p.tok = _EOF
	}
}

// IsIncomplete reports whether a Parser error could have been avoided with
// extra input bytes. For example, if an [io.EOF] was encountered while there was
// an unclosed quote or parenthesis.
func IsIncomplete(err error) bool {
	perr, ok := err.(ParseError)
	return ok && perr.Incomplete
}

// TODO: probably redo with a [LangVariant] argument.
// Perhaps offer an iterator version as well.

// IsKeyword returns true if the given word is a language keyword
// in POSIX Shell or Bash.
func IsKeyword(word string) bool {
	// This list has been copied from the bash 5.1 source code, file y.tab.c +4460
	// TODO: should we include entries for zsh here? e.g. "{}", "repeat", "always", ...
	switch word {
	case
		"!",
		"[[", // only if COND_COMMAND is defined
		"]]", // only if COND_COMMAND is defined
		"case",
		"coproc", // only if COPROCESS_SUPPORT is defined
		"do",
		"done",
		"else",
		"esac",
		"fi",
		"for",
		"function",
		"if",
		"in",
		"select", // only if SELECT_COMMAND is defined
		"then",
		"time", // only if COMMAND_TIMING is defined
		"until",
		"while",
		"{",
		"}":
		return true
	}
	return false
}

// ParseError represents an error found when parsing a source file, from which
// the parser cannot recover.
type ParseError struct {
	Filename string
	Pos      Pos
	Text     string

	Incomplete bool
}

func (e ParseError) Error() string {
	if e.Filename == "" {
		return fmt.Sprintf("%s: %s", e.Pos, e.Text)
	}
	return fmt.Sprintf("%s:%s: %s", e.Filename, e.Pos, e.Text)
}

// LangError is returned when the parser encounters code that is only valid in
// other shell language variants. The error includes what feature is not present
// in the current language variant, and what languages support it.
type LangError struct {
	Filename string
	Pos      Pos

	// TODO: consider replacing the Langs slice with a bitset.

	// Feature briefly describes which language feature caused the error.
	Feature string
	// Langs lists some of the language variants which support the feature.
	Langs []LangVariant
	// LangUsed is the language variant used which led to the error.
	LangUsed LangVariant
}

func (e LangError) Error() string {
	var sb strings.Builder
	if e.Filename != "" {
		sb.WriteString(e.Filename)
		sb.WriteString(":")
	}
	sb.WriteString(e.Pos.String())
	sb.WriteString(": ")
	sb.WriteString(e.Feature)
	if strings.HasSuffix(e.Feature, "s") {
		sb.WriteString(" are a ")
	} else {
		sb.WriteString(" is a ")
	}
	for i, lang := range e.Langs {
		if i > 0 {
			sb.WriteString("/")
		}
		sb.WriteString(lang.String())
	}
	sb.WriteString(" feature; tried parsing as ")
	sb.WriteString(e.LangUsed.String())
	return sb.String()
}

func (p *Parser) posErr(pos Pos, format string, args ...any) {
	// for i, arg := range args {
	// 	if arg, ok := arg.(fmt.Stringer); ok && arg != _EOF {
	// 		args[i] = quotedToken(arg)
	// 	}
	// }
	p.errPass(ParseError{
		Filename:   p.f.Name,
		Pos:        pos,
		Text:       fmt.Sprintf(format, args...),
		Incomplete: p.tok == _EOF && p.Incomplete(),
	})
}

func (p *Parser) curErr(format string, args ...any) {
	p.posErr(p.pos, format, args...)
}

func (p *Parser) checkLang(pos Pos, langSet LangVariant, format string, a ...any) {
	if p.lang.in(langSet) {
		return
	}
	if langBashLike.in(langSet) {
		// If we're reporting an error because a feature is for bash-like funcs,
		// just mention "bash" rather than "bash/bats" for the sake of clarity.
		langSet &^= LangBats
	}
	p.errPass(LangError{
		Filename: p.f.Name,
		Pos:      pos,
		Feature:  fmt.Sprintf(format, a...),
		Langs:    slices.Collect(langSet.bits()),
		LangUsed: p.lang,
	})
}

func (p *Parser) stmts(yield func(*Stmt, error) bool, stops ...string) {
	gotEnd := true
loop:
	for p.tok != _EOF {
		newLine := p.got(_Newl)
		switch p.tok {
		case _LitWord:
			for _, stop := range stops {
				if p.val == stop {
					break loop
				}
			}
			if p.val == "}" {
				p.curErr(`%#q can only be used to close a block`, rightBrace)
			}
		case rightParen:
			if p.quote == subCmd {
				break loop
			}
		case bckQuote:
			if p.backquoteEnd() {
				break loop
			}
		case dblSemicolon, semiAnd, dblSemiAnd, semiOr:
			if p.quote == switchCase {
				break loop
			}
			p.curErr("%#q can only be used in a case clause", p.tok)
		}
		if !newLine && !gotEnd {
			p.curErr("statements must be separated by &, ; or a newline")
		}
		if p.tok == _EOF {
			break
		}
		p.openNodes++
		s := p.getStmt(true, false, false)
		p.openNodes--
		if s == nil {
			p.invalidStmtStart()
			break
		}
		gotEnd = s.Semicolon.IsValid()
		if !yield(s, p.err) {
			break
		}
	}
}

func (p *Parser) stmtList(stops ...string) ([]*Stmt, []Comment) {
	var stmts []*Stmt
	var last []Comment
	fn := func(s *Stmt, err error) bool {
		stmts = append(stmts, s)
		return true
	}
	p.stmts(fn, stops...)
	split := len(p.accComs)
	if p.tok == _LitWord && (p.val == "elif" || p.val == "else" || p.val == "fi") {
		// Split the comments, so that any aligned with an opening token
		// get attached to it. For example:
		//
		//     if foo; then
		//         # inside the body
		//     # document the else
		//     else
		//     fi
		// TODO(mvdan): look into deduplicating this with similar logic
		// in caseItems.
		for i, c := range slices.Backward(p.accComs) {
			if c.Pos().Col() != p.pos.Col() {
				break
			}
			split = i
		}
	}
	if split > 0 { // keep last nil if empty
		last = p.accComs[:split]
	}
	p.accComs = p.accComs[split:]
	return stmts, last
}

func (p *Parser) invalidStmtStart() {
	switch p.tok {
	case semicolon, and, or, andAnd, orOr, andPipe, andBang:
		p.curErr("%#q can only immediately follow a statement", p.tok)
	case rightParen:
		p.curErr("%#q can only be used to close a subshell", p.tok)
	default:
		p.curErr("%#q is not a valid start for a statement", p.tok)
	}
}

func (p *Parser) getWord() *Word {
	if w := p.wordAnyNumber(); len(w.Parts) > 0 && p.err == nil {
		return w
	}
	return nil
}

func (p *Parser) getLit() *Lit {
	if p.tok.isLit() {
		l := p.lit(p.pos, p.val)
		p.next()
		return l
	}
	return nil
}

func (p *Parser) wordParts(wps []WordPart) []WordPart {
	if p.quote == noState {
		p.quote = unquotedWordCont
		defer func() { p.quote = noState }()
	}
	for {
		p.openNodes++
		n := p.wordPart()
		p.openNodes--
		if n == nil {
			if len(wps) == 0 {
				return nil // normalize empty lists into nil
			}
			return wps
		}
		wps = append(wps, n)
		if p.spaced {
			return wps
		}
	}
}

func (p *Parser) ensureNoNested(pos Pos) {
	if p.forbidNested {
		p.posErr(pos, "expansions not allowed in heredoc words")
	}
}

func (p *Parser) hdocLiteralParam() WordPart {
	if !p.forbidNested || p.quote != dblQuotes || p.tok != dollar {
		return nil
	}
	pos := p.pos
	switch p.r {
	case '@', '*', '#', '?', '$', '!', '-', '0':
		val := "$" + string(p.r)
		p.rune()
		p.next()
		return p.lit(pos, val)
	}
	return nil
}

func (p *Parser) wordPart() WordPart {
	switch p.tok {
	case _Lit, _LitWord, _LitRedir:
		l := p.lit(p.pos, p.val)
		p.next()
		return l
	case dollBrace:
		p.ensureNoNested(p.pos)
		switch p.r {
		case '|':
			p.checkLang(p.pos, langBashLike|LangMirBSDKorn, "`${|stmts;}`")
			fallthrough
		case ' ', '\t', '\n':
			p.checkLang(p.pos, langBashLike|LangMirBSDKorn, "`${ stmts;}`")
			cs := &CmdSubst{
				Left:     p.pos,
				TempFile: p.r != '|',
				ReplyVar: p.r == '|',
			}
			old := p.preNested(subCmdBrace)
			p.rune() // don't tokenize '|'
			p.next()
			cs.Stmts, cs.Last = p.stmtList("}")
			p.postNested(old)
			pos, ok := p.gotRsrv("}")
			if !ok {
				p.matchingErr(cs.Left, dollBrace, rightBrace)
			}
			cs.Right = pos
			return cs
		default:
			return p.paramExp()
		}
	case dollDblParen, dollBrack:
		p.ensureNoNested(p.pos)
		left := p.tok
		ar := &ArithmExp{Left: p.pos, Bracket: left == dollBrack}
		old := p.preNested(arithmExpr)
		p.next()
		if p.got(hash) {
			p.checkLang(ar.Pos(), LangMirBSDKorn, "unsigned expressions")
			ar.Unsigned = true
		}
		ar.X = p.followArithm(left, ar.Left)
		if ar.Bracket {
			if p.tok != rightBrack {
				p.arithmMatchingErr(ar.Left, dollBrack, rightBrack)
			}
			p.postNested(old)
			ar.Right = p.pos
			p.next()
		} else {
			ar.Right = p.arithmEnd(dollDblParen, ar.Left, old)
		}
		return ar
	case dollParen:
		p.ensureNoNested(p.pos)
		return p.cmdSubst()
	case dollar:
		if lit := p.hdocLiteralParam(); lit != nil {
			return lit
		}
		pe := p.paramExp()
		if pe == nil { // was not actually a parameter expansion, like: "foo$"
			l := p.lit(p.pos, "$")
			p.next()
			return l
		}
		p.ensureNoNested(pe.Dollar)
		return pe
	case assgnParen:
		p.checkLang(p.pos, LangZsh, `%#q process substitutions`, p.tok)
		fallthrough
	case cmdIn, cmdOut:
		p.ensureNoNested(p.pos)
		ps := &ProcSubst{Op: ProcOperator(p.tok), OpPos: p.pos}
		old := p.preNested(subCmd)
		p.next()
		ps.Stmts, ps.Last = p.stmtList()
		p.postNested(old)
		ps.Rparen = p.matched(ps.OpPos, token(ps.Op), rightParen)
		return ps
	case sglQuote, dollSglQuote:
		sq := &SglQuoted{Left: p.pos, Dollar: p.tok == dollSglQuote}
		r := p.r
		for p.newLit(r); ; r = p.rune() {
			switch r {
			case '\\':
				if sq.Dollar {
					p.rune()
				}
			case '\'':
				sq.Right = p.nextPos()
				sq.Value = p.endLit()

				p.rune()
				p.next()
				return sq
			case escNewl:
				if p.quote == subCmdBckquo {
					if p.bquoteEscapedByte {
						p.litBs = append(p.litBs, '\\', '\n')
					}
					continue
				}
				p.litBs = append(p.litBs, '\\', '\n')
			case utf8.RuneSelf:
				p.tok = _EOF
				if p.recoverError() {
					sq.Right = recoveredPos
					return sq
				}
				p.quoteErr(sq.Pos(), sglQuote)
				return nil
			}
		}
	case dblQuote, dollDblQuote:
		if p.quote == dblQuotes {
			// p.tok == dblQuote, as "foo$" puts $ in the lit
			return nil
		}
		return p.dblQuoted()
	case bckQuote:
		if p.backquoteEnd() {
			return nil
		}
		p.ensureNoNested(p.pos)
		cs := &CmdSubst{Left: p.pos, Backquotes: true}
		old := p.preNested(subCmdBckquo)
		prevBqDbl := p.bquoteDblQuoted
		p.bquoteDblQuoted = old.quote == dblQuotes
		p.openBquotes++

		// The lexer didn't call p.rune for us, so that it could have
		// the right p.openBquotes to properly handle backslashes.
		p.rune()

		p.next()
		cs.Stmts, cs.Last = p.stmtList()
		if p.tok == bckQuote && p.lastBquoteEsc < p.openBquotes-1 {
			// e.g. found ` before the nested backquote \` was closed.
			p.tok = _EOF
			p.quoteErr(cs.Pos(), bckQuote)
		}
		p.postNested(old)
		p.bquoteDblQuoted = prevBqDbl
		p.openBquotes--
		cs.Right = p.pos

		// Like above, the lexer didn't call p.rune for us.
		p.rune()
		if !p.got(bckQuote) {
			if p.recoverError() {
				cs.Right = recoveredPos
			} else {
				p.quoteErr(cs.Pos(), bckQuote)
			}
		}
		return cs
	case leftParen:
		if p.lang.in(LangZsh) && p.r != ')' {
			// Zsh glob qualifier like *(N) or .(:a); the only case where
			// ( immediately after a word is not a glob qualifier is ()
			// for a function declaration, which the parser handles earlier.
			pos := p.pos
			p.pos = p.nextPos()
			for p.newLit(p.r); p.r != utf8.RuneSelf && p.r != ')'; p.rune() {
			}
			if p.r != ')' {
				p.tok = _EOF // we can only get here due to EOF
				p.matchingErr(pos, leftParen, rightParen)
			}
			p.rune()
			p.val = p.endLit()
			l := p.lit(pos, "("+p.val)
			p.next()
			return l
		}
		return nil
	case globQuest, globStar, globPlus, globAt, globExcl:
		p.checkLang(p.pos, langBashLike|LangMirBSDKorn, "extended globs")
		eg := &ExtGlob{Op: GlobOperator(p.tok), OpPos: p.pos}
		lparens := 1
		r := p.r
	globLoop:
		for p.newLit(r); ; r = p.rune() {
			switch r {
			case utf8.RuneSelf:
				break globLoop
			case '(':
				lparens++
			case ')':
				if lparens--; lparens == 0 {
					break globLoop
				}
			}
		}
		eg.Pattern = p.lit(posAddCol(eg.OpPos, 2), p.endLit())
		p.rune()
		p.next()
		if lparens != 0 {
			p.matchingErr(eg.OpPos, token(eg.Op), rightParen)
		}
		return eg
	default:
		return nil
	}
}

func (p *Parser) cmdSubst() *CmdSubst {
	cs := &CmdSubst{Left: p.pos}
	// Bash 5.3 numbers the lines inside a command substitution one
	// behind the input when a here-document opened earlier on the
	// same line is still waiting for its body: the newline ending
	// the opener's line is accounted to the pending here-document
	// rather than to the line counter. Mirror that lag while inside
	// the parens (and undo it after the close) so diagnostics for
	// the substitution's statements match bash. Bashy mode only.
	hdocLag := p.heredocEOFWarning != nil && len(p.heredocs) > p.buriedHdocs
	if hdocLag {
		p.line--
	}
	old := p.preNested(subCmd)
	p.openComsubs++
	p.next()
	cs.Stmts, cs.Last = p.stmtList()
	p.openComsubs--
	// Here-documents opened inside the substitution whose bodies were
	// not reached before the closing paren: bash warns about them at
	// the close and then reads the bodies from the lines that follow
	// the substitution, which is also what doHeredocs will do.
	if pending := len(p.heredocs) - p.buriedHdocs; pending > 0 &&
		p.heredocComsubWarning != nil && p.tok == rightParen {
		p.heredocComsubWarning(int(p.pos.Line()), pending)
	}
	p.postNested(old)
	if hdocLag {
		p.line++
	}
	cs.Right = p.matched(cs.Left, dollParen, rightParen)
	return cs
}

func (p *Parser) dblQuoted() *DblQuoted {
	alloc := &struct {
		quoted DblQuoted
		parts  [1]WordPart
	}{
		quoted: DblQuoted{Left: p.pos, Dollar: p.tok == dollDblQuote},
	}
	q := &alloc.quoted
	old := p.quote
	p.quote = dblQuotes
	p.next()
	q.Parts = p.wordParts(alloc.parts[:0])
	p.quote = old
	q.Right = p.pos
	if !p.got(dblQuote) {
		if p.recoverError() {
			q.Right = recoveredPos
		} else {
			p.quoteErr(q.Pos(), dblQuote)
		}
	}
	return q
}

// paramExp parses a short or full parameter expansion, depending on whether
// [Parser.tok] is [dollar] or [dollBrace]. It returns nil if a [dollar] token
// does not form a valid parameter expansion, in which case it should be parsed
// as a literal.
func (p *Parser) paramExp() *ParamExp {
	old := p.quote
	oldParamExpOuterQuote := p.paramExpOuterQuote
	p.paramExpOuterQuote = old
	defer func() { p.paramExpOuterQuote = oldParamExpOuterQuote }()
	p.quote = runeByRune
	// [ParamExp.Short] means we are parsing $exp rather than ${exp}.
	pe := &ParamExp{
		Dollar: p.pos,
		Short:  p.tok == dollar,
	}
	if !pe.Short {
		p.skipEscNewl()
	}
	if !pe.Short && p.r == '(' {
		p.checkLang(pe.Pos(), LangZsh, `parameter expansion flags`)
		// For now, for simplicity, we parse flags as just a literal.
		// In the future, parsing as a word is better for cases like
		// `${(ps.$sep.)val}`.
		lparen := p.nextPos()
		p.rune()
		p.pos = p.nextPos()
		for p.newLit(p.r); p.r != utf8.RuneSelf && p.r != ')'; p.rune() {
		}
		p.val = p.endLit()
		if p.r != ')' {
			p.tok = _EOF // we can only get here due to EOF
			p.matchingErr(lparen, leftParen, rightParen)
		}
		pe.Flags = p.lit(p.pos, p.val)
		p.rune()
	}
	// Zsh-only prefixes that change how the parameter is expanded.
	// They may appear in any combination, like ${=^name}.
	// Doubling the rune (${==a}, ${~~a}, ${^^a}) forces the option off.
zshPrefixLoop:
	for p.lang.in(LangZsh) {
		var field *OptState
		switch p.r {
		case '=':
			field = &pe.Split
		case '~':
			field = &pe.GlobSubst
		case '^':
			field = &pe.RcExpand
		default:
			break zshPrefixLoop
		}
		next, after := p.peekTwo()
		state := OptOn
		check := next
		if rune(next) == p.r {
			state = OptOff
			check = after
		}
		if check == utf8.RuneSelf || check == '}' {
			break zshPrefixLoop
		}
		// For the short form, only treat as a prefix if followed by something
		// that could start a parameter name or another zsh prefix.
		if pe.Short && check != '=' && check != '~' && check != '^' &&
			!singleRuneParam(check) && !paramNameRune(check) && check != '"' {
			break zshPrefixLoop
		}
		if state == OptOff {
			p.rune() // consume the first of the doubled pair
		}
		p.rune()
		*field = state
	}
	if !pe.Short || p.lang.in(LangZsh) {
		// Prefixes, like ${#name} to get the length of a variable.
		// Note that in Zsh, the short form like $#name is allowed too.
		switch p.r {
		case '#':
			if p.paramNameStart() {
				pe.Length = true
			}
		case '%':
			if p.paramNameStart() {
				p.checkLang(pe.Pos(), LangMirBSDKorn, "`${%%foo}`")
				pe.Width = true
			}
		case '!':
			// Unlike the others, zsh has no $!foo prefix. Bash
			// only enters indirect mode when the next char starts
			// a real identifier — `${!-x}`, `${!+x}`, `${!:-x}`,
			// `${!?x}`, `${!=x}`, `${!#x}`, `${!%x}` are parsed as
			// `$!` (last bg PID) followed by an operator, NOT
			// indirection. peek before paramNameStart commits.
			next := p.peek()
			isOperatorNext := next == '-' || next == '+' || next == '=' ||
				next == '?' || next == ':' || next == '#' || next == '%' ||
				next == '/'
			if !pe.Short && !isOperatorNext && p.paramNameStart() {
				// `${!name}` parameter indirection. Bash supports it in
				// POSIX mode too (`bash --posix`), so allow it under
				// [LangPOSIX] — unlike the name-prefix listing forms
				// `${!pre*}` / `${!pre@}`, which remain bash/mksh-only
				// and are gated separately below.
				p.checkLang(pe.Pos(), langBashLike|LangMirBSDKorn|LangPOSIX, "`${!foo}`")
				pe.Excl = true
			}
		case '+':
			if p.paramNameStart() {
				p.checkLang(pe.Pos(), LangZsh, "`${+foo}`")
				pe.IsSet = true
			}
		}
	}
	// Bash does not support zsh-style nested parameter expansions like
	// `${${x}}` or `${$(cmd)}`; it parses them at face value and rejects
	// them at expansion time as a bad substitution. Capture the raw
	// remainder so the runner can emit that same runtime diagnostic.
	if !pe.Short && p.lang.in(langBashExact) && p.r == '$' {
		if p1 := p.peek(); p1 == '{' || p1 == '(' {
			// Record an empty parameter name so the expansion keeps the
			// usual non-nil Param invariant; the runner reports the bad
			// substitution before the name is ever read. The raw
			// remainder (the nested `$...`) is captured verbatim, so
			// reset the pending token to avoid prefixing it with `${`.
			pe.Param = p.lit(p.nextPos(), "")
			p.tok = illegalTok
			return p.deferBadSubst(pe, old)
		}
	}
	p.skipEscNewl()
	if pe = p.paramExpParameter(pe); pe == nil {
		p.quote = old
		return nil // just "$"
	}
	// In short mode, any indexing or suffixes is not allowed, and we don't require '}'.
	// Zsh is an exception: $foo[1] and $foo[1,3] are valid. Note that $1[x] does not qualify.
	if pe.Short {
		if p.lang.in(LangZsh) && p.r == '[' && (len(p.val) != 1 || !positionalRuneParam(p.val[0])) {
			p.pos = p.nextPos()
			p.rune()
			pe.Index = p.eitherIndex()
		}
		p.quote = old
		p.next()
		return pe
	}
	p.skipEscNewl()
	// Index expressions like ${foo[1]}. Note that expansion suffixes can be combined,
	// like ${foo[@]//replace/with}.
	if p.r == '[' {
		p.checkLang(p.nextPos(), langBashLike|LangMirBSDKorn|LangZsh, "arrays")
		// In zsh some of these like ${@[-1]} or ${*[1,3]} work,
		// so we don't do this sort of check at all.
		if !p.lang.in(LangZsh) && pe.Param != nil && !ValidName(pe.Param.Value) {
			p.posErr(p.nextPos(), "cannot index a special parameter name")
		}
		p.pos = p.nextPos()
		p.rune()
		pe.Index = p.eitherIndexBlank(true)
	}
	tokRune := p.r
	p.pos = p.nextPos()
	p.tok = p.paramToken(p.r)
	if p.tok == rightBrace {
		pe.Rbrace = p.pos
		p.quote = old
		p.next()
		return pe
	}
	if p.tok != _EOF && (pe.Length || pe.Width || pe.IsSet) {
		if p.lang.in(langBashExact) {
			// Bash defers `${#foo%}` and friends to expansion
			// time, rejecting them as a bad substitution there.
			return p.deferBadSubst(pe, old)
		}
		p.curErr("cannot combine multiple parameter expansion operators")
	}
	if pe.Param != nil && pe.Param.Value == "#" {
		// `${#OP}` with no operand (a bare `${#=}`, `${#+}`, `${#%}`, or
		// `${#/}`) is a malformed length substitution that bash rejects as
		// a bad substitution. When an operand follows (`${#=x}`, `${#+x}`,
		// `${#%x}`, `${#/x}`) the `#` is instead the special parameter `$#`
		// and OP its expansion operator, so leave Names unset and let the
		// regular operator parsing below build that expansion. paramToken
		// has already consumed the operator, so p.r is the next rune: `}`
		// means an empty operand.
		switch p.tok {
		case slash, perc, assgn, plus:
			if p.r == '}' {
				pe.Names = NamesPrefix
			}
		}
	}
	switch p.tok {
	case slash, dblSlash: // pattern search and replace
		p.checkLang(p.pos, langBashLike|LangMirBSDKorn|LangZsh, "search and replace")
		pe.Repl = &Replace{All: p.tok == dblSlash}
		p.quote = paramExpRepl
		p.paramExpReplFirst = true
		p.next()
		p.paramExpReplFirst = false
		pe.Repl.Orig = p.getWord()
		p.quote = paramExpExp
		if p.got(slash) {
			pe.Repl.With = p.getWord()
		}
	case colon: // slicing
		if pe.Names != 0 && !pe.Excl && p.r == '}' {
			break
		}
		if pe.Param != nil && pe.Param.Value == "#" && p.r == '}' {
			pe.Names = NamesPrefix
			pe.Rbrace = p.nextPos()
			p.rune()
			p.quote = old
			p.next()
			return pe
		}
		if p.lang.in(LangZsh) && (p.r == '&' || asciiLetter(p.r)) {
			pos := p.pos
		loop:
			for p.newLit(p.r); ; p.rune() {
				switch p.r {
				case utf8.RuneSelf:
					p.tok = _EOF
					p.matchingErr(pe.Dollar, dollBrace, rightBrace)
					break loop
				case '}':
					pe.Modifiers = append(pe.Modifiers, p.lit(pos, p.endLit()))
					pe.Rbrace = p.nextPos()
					p.rune()
					break loop
				case ':':
					pe.Modifiers = append(pe.Modifiers, p.lit(pos, p.endLit()))
					p.rune()
					pos = p.nextPos()
					p.newLit(p.r)
				}
			}
			p.quote = old
			p.next()
			return pe
		}
		p.checkLang(p.pos, langBashLike|LangMirBSDKorn|LangZsh, "slicing")
		pe.Slice = &Slice{}
		colonPos := p.pos
		p.quote = paramExpArithm
		if p.next(); p.tok != colon && p.tok != rightBrace {
			if pe.Param != nil && pe.Param.Value == "#" && p.tok == perc {
				pe.Slice.Offset = p.wordOne(p.lit(p.pos, "%"))
				p.next()
			} else {
				pe.Slice.Offset = p.followArithm(colon, colonPos)
			}
		}
		colonPos = p.pos
		if p.got(colon) {
			if p.tok == rightBrace {
				pe.Slice.Length = p.wordOne(p.lit(colonPos, "0"))
			} else {
				pe.Slice.Length = p.followArithm(colon, colonPos)
			}
		}
		// Need to use a different matched style so arithm errors
		// get reported correctly
		p.quote = old
		pe.Rbrace = p.pos
		p.matchedArithm(pe.Dollar, dollBrace, rightBrace)
		return pe
	case caret, dblCaret, comma, dblComma, peTilde, peDblTilde: // upper/lower/toggle case
		p.checkLang(p.pos, langBashLike, "this expansion operator")
		pe.Exp = p.paramExpExp()
	case at, star:
		switch {
		case p.tok == star && !pe.Excl && p.r == '}':
			// Bash 5.3 parses `${name*}` (no `!`) without
			// complaint and defers the error to expansion time
			// as `bad substitution`. Reuse the names operator so
			// the runner can emit that same runtime diagnostic.
			pe.Names = ParNamesOperator(p.tok)
			p.next()
		case p.tok == star && !pe.Excl:
			if p.lang.in(langBashExact) {
				return p.deferBadSubst(pe, old)
			}
			p.curErr("not a valid parameter expansion operator: %#q", p.tok)
		case pe.Excl && p.r == '}':
			p.checkLang(pe.Pos(), langBashLike, "`${!foo%s}`", p.tok)
			pe.Names = ParNamesOperator(p.tok)
			p.next()
		case p.tok == at:
			p.checkLang(p.pos, langBashLike|LangMirBSDKorn, "this expansion operator")
			fallthrough
		default:
			pe.Exp = p.paramExpExp()
		}
	case plus, colPlus, minus, colMinus, quest, colQuest, assgn, colAssgn,
		perc, dblPerc, hash, dblHash, colHash, colPipe, colStar:
		pe.Exp = p.paramExpExp()
	case _EOF:
	default:
		if paramNameRune(tokRune) {
			if pe.Param != nil {
				p.curErr("%#q cannot be followed by a word", pe.Param.Value)
			} else {
				p.curErr("nested parameter expansion cannot be followed by a word")
			}
		} else {
			if p.lang.in(langBashExact) {
				// Bash scans to the matching `}` and rejects the
				// whole expansion at expansion time (`${x!y}`).
				return p.deferBadSubst(pe, old)
			}
			p.curErr("not a valid parameter expansion operator: %#q", string(tokRune))
		}
	}
	if p.tok != _EOF && p.tok != rightBrace {
		p.tok = p.paramToken(p.r)
	}
	p.quote = old
	pe.Rbrace = p.matched(pe.Dollar, dollBrace, rightBrace)
	return pe
}

func (p *Parser) skipEscNewl() {
	for p.r == escNewl {
		p.rune()
	}
}

// badSubstRemainder consumes the raw remainder of a parameter
// expansion up to its matching `}` for operators which bash accepts
// at parse time but rejects at expansion time, such as `${x!y}` or
// `${#foo%}`. pre holds the operator text already consumed.
func (p *Parser) badSubstRemainder(pre string) *Lit {
	pos := p.pos
	var sb strings.Builder
	sb.WriteString(pre)
	depth := 0
	for {
		switch p.r {
		case utf8.RuneSelf:
			p.tok = _EOF
			return p.lit(pos, sb.String())
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return p.lit(pos, sb.String())
			}
			depth--
		}
		sb.WriteRune(p.r)
		p.rune()
	}
}

// deferBadSubst finishes a ${...} expansion whose remainder bash only
// rejects at expansion time, storing the raw text in pe.BadSubst.
func (p *Parser) deferBadSubst(pe *ParamExp, old quoteState) *ParamExp {
	pre := p.tok.String()
	if p.tok == illegalTok {
		pre = ""
	}
	pe.BadSubst = p.badSubstRemainder(pre)
	if p.tok == _EOF {
		p.matchingErr(pe.Dollar, dollBrace, rightBrace)
		return pe
	}
	pe.Rbrace = p.nextPos()
	p.rune()
	p.quote = old
	p.next()
	return pe
}

func (p *Parser) paramNameStart() bool {
	r := p.peek()
	if r == '\\' {
		if p1, p2 := p.peekTwo(); p1 == '\\' && (p2 == '\n' || p2 == '\r') {
			r = 'a'
		}
	}
	if r == utf8.RuneSelf || singleRuneParam(r) || paramNameRune(r) || r == '"' {
		p.rune()
		return true
	}
	return false
}

func (p *Parser) nestedParameterStart(pe *ParamExp) (left token, quotePos Pos) {
	if pe.Short {
		return illegalTok, Pos{}
	}
	if p.r == '"' {
		quotePos = p.nextPos()
		p.rune()
	}
	if p.r != '$' {
		if quotePos.IsValid() {
			return dollar, quotePos
		}
		return illegalTok, Pos{}
	}
	switch p1 := p.peek(); p1 {
	case '{', '(':
		p.pos = p.nextPos()
		p.checkLang(p.pos, LangZsh, "nested parameter expansions")
		if p.err != nil {
			return illegalTok, Pos{} // xxx given that we overwrite p.tok below
		}
		p.rune()
		p.rune()
		if p1 == '{' {
			left = dollBrace
		} else { // '('
			left = dollParen
		}
	}
	return left, quotePos
}

func (p *Parser) paramExpParameter(pe *ParamExp) *ParamExp {
	// Check for Zsh nested parameter expressions like ${(f)"$(foo)"}.
	if left, quotePos := p.nestedParameterStart(pe); left != illegalTok {
		var wp WordPart
		switch p.tok = left; p.tok {
		case dollBrace: // ${#${nested parameter}}
			p.tok = dollBrace
			wp = p.paramExp()
		case dollParen: // ${#$(nested command)}
			wp = p.cmdSubst()
		default: // dollar
			p.posErr(pe.Pos(), "invalid nested parameter expansion")
		}
		if quotePos.IsValid() {
			if p.r != '"' {
				p.tok = p.paramToken(p.r)
				if p.tok == illegalTok {
					p.posErr(pe.Pos(), "invalid nested parameter expansion")
				} else {
					p.quoteErr(quotePos, dblQuote)
				}
			}
			pe.NestedParam = &DblQuoted{
				Left:  quotePos,
				Right: p.nextPos(),
				Parts: []WordPart{wp},
			}
			p.rune()
		} else {
			pe.NestedParam = wp
		}
		return pe
	}
	if p.dollSglQuotedParamName(pe) {
		return pe
	}
	// The parameter name itself, like $foo or $?.
	switch p.r {
	case '?', '-':
		if pe.Length && p.peek() != '}' {
			// actually ${#-default}, not ${#-}; fix the ambiguity
			pe.Length = false
			pos := p.nextPos()
			pe.Param = p.lit(posAddCol(pos, -1), "#")
			pe.Param.ValueEnd = pos
			break
		}
		fallthrough
	case '@', '*', '#', '!', '$':
		r, pos := p.r, p.nextPos()
		p.rune()
		pe.Param = p.lit(pos, string(r))
	default:
		// Note that $1a is equivalent to ${1}a, but ${1a} is not.
		// POSIX Shell says the latter is unspecified behavior, so match Bash's behavior.
		pos := p.nextPos()
		if pe.Short && singleRuneParam(p.r) {
			p.val = string(p.r)
			p.rune()
		} else {
			for p.newLit(p.r); p.r != utf8.RuneSelf; p.rune() {
				if !paramNameRune(p.r) && p.r != escNewl {
					break
				}
			}
			p.val = p.endLit()
			if !numberLiteral(p.val) && !ValidName(p.val) {
				if pe.Short {
					return nil // just "$"
				}
				if pe.Length {
					pe.Length = false
					pe.Names = NamesPrefix
					pe.Param = p.lit(posAddCol(pos, -1), p.val)
					return pe
				}
				if p.lang.in(LangZsh) && p.val == "" {
					// Zsh allows omitting the parameter name, e.g. ${:-word}.
					return pe
				}
				p.posErr(pos, "invalid parameter name")
			}
		}
		pe.Param = p.lit(pos, p.val)
	}
	return pe
}

func (p *Parser) dollSglQuotedParamName(pe *ParamExp) bool {
	if pe.Short || p.r != '$' || p.peek() != '\'' {
		return false
	}
	p.checkLang(p.nextPos(), langBashLike, "$'name' parameter names")
	pos := p.nextPos()
	p.rune() // '
	p.rune() // first quoted rune
	start := p.nextPos()
	for p.newLit(p.r); ; p.rune() {
		switch p.r {
		case '\\':
			p.rune()
		case '\'':
			val := p.endLit()
			if !numberLiteral(val) && !ValidName(val) {
				p.posErr(pos, "invalid parameter name")
			}
			pe.Param = p.lit(start, val)
			p.rune()
			return true
		case utf8.RuneSelf:
			p.tok = _EOF
			p.matchingErr(pos, dollSglQuote, sglQuote)
			return true
		}
	}
}

func (p *Parser) paramExpExp() *Expansion {
	op := ParExpOperator(p.tok)
	switch op {
	case MatchEmpty, ArrayExclude, ArrayIntersect:
		p.checkLang(p.pos, LangZsh, "${name%sarg}", op)
	}
	oldDblQuoted := p.paramExpExpDblQuoted
	oldOp := p.paramExpExpOp
	p.paramExpExpDblQuoted = p.paramExpOuterQuote == dblQuotes
	p.paramExpExpOp = op
	defer func() {
		p.paramExpExpDblQuoted = oldDblQuoted
		p.paramExpExpOp = oldOp
	}()
	p.quote = paramExpExp
	p.next()
	if op == OtherParamOps {
		if p.tok.isLit() {
			switch p.val {
			case "a", "k", "u", "A", "E", "K", "L", "P", "U":
				p.checkLang(p.pos, langBashLike, "this expansion operator")
			case "#":
				p.checkLang(p.pos, LangMirBSDKorn, "this expansion operator")
			}
		}
	}
	return &Expansion{Op: op, Word: p.getWord()}
}

func (p *Parser) eitherIndex() ArithmExpr {
	return p.eitherIndexBlank(false)
}

// eitherIndexBlank is eitherIndex with blankOK controlling whether a
// whitespace-only subscript like ${b[   ]} is accepted. Bash rejects
// `${b[]}` at parse time but defers a blank subscript to expansion
// time, where it evaluates as index 0 (indexed arrays) or as a key
// that no associative array contains.
func (p *Parser) eitherIndexBlank(blankOK bool) ArithmExpr {
	old := p.quote
	oldAssignIndexWords := p.assignIndexWords
	lpos := p.pos
	p.quote = paramExpArithm
	p.assignIndexWords = true
	p.next()
	if p.rawAssignIndex && p.lang.in(langBashExact) {
		switch p.tok {
		case rightBrack:
			val := ""
			if p.pos.Offset() > lpos.Offset()+1 {
				val = " "
			}
			expr := ArithmExpr(p.wordOne(p.lit(p.pos, val)))
			p.quote = old
			p.assignIndexWords = oldAssignIndexWords
			p.matchedArithm(lpos, leftBrack, rightBrack)
			return expr
		case star, at:
			if p.nakedAssignIndex {
				// Until the closing bracket is followed by '=', this is an
				// ordinary command word. Keep parsing so forms such as
				// f[*T] can recover as words; a bare [*] or [@] remains a
				// valid array subscript below.
				break
			}
			expr := ArithmExpr(p.wordOne(p.lit(p.pos, p.tok.String())))
			p.next()
			p.quote = old
			p.assignIndexWords = oldAssignIndexWords
			p.matchedArithm(lpos, leftBrack, rightBrack)
			return expr
		}
	}
	switch p.tok {
	case star, at, perc, exclMark:
		p.tok, p.val = _LitWord, p.tok.String()
	}
	if p.lang.in(langBashExact) && p.tok == rightBrack &&
		(blankOK || p.pos.Offset() > lpos.Offset()+1) {
		val := ""
		if p.pos.Offset() > lpos.Offset()+1 {
			val = " "
		}
		expr := ArithmExpr(p.wordOne(p.lit(p.pos, val)))
		p.quote = old
		p.assignIndexWords = oldAssignIndexWords
		p.matchedArithm(lpos, leftBrack, rightBrack)
		return expr
	}
	expr := p.followArithm(leftBrack, lpos)
	if p.nakedAssignIndex {
		text := arithmExprText(expr)
		if p.tok != rightBrack {
			// The subscript is not valid arithmetic. That is NOT a parse
			// error: bash scans to the matching bracket and only complains
			// when it EVALUATES the subscript, so `a[b[c]d]=e` is a runtime
			// "syntax error in expression" with status 1, not a rejected
			// script. Recover the raw text as one literal and let the
			// interpreter report it (bash-5.3 tests/arith.tests line 358).
			lit := &Lit{ValuePos: lpos, ValueEnd: p.pos, Value: text}
			depth := 0
			for p.tok != _EOF {
				if p.tok == rightBrack && depth == 0 {
					break
				}
				sep := ""
				if p.spaced {
					sep = " "
				}
				if p.tok.isLit() {
					next := p.getLit()
					lit.Value += sep + next.Value
					lit.ValueEnd = next.ValueEnd
					continue
				}
				tok := p.tok
				lit.Value += sep + tok.String()
				lit.ValueEnd = posAddCol(p.pos, len(tok.String()))
				if tok == leftBrack {
					depth++
				} else if tok == rightBrack {
					depth--
				}
				p.nextArith(false)
			}
			expr = p.wordOne(lit)
		}
	}
	p.quote = old
	p.assignIndexWords = oldAssignIndexWords
	p.matchedArithm(lpos, leftBrack, rightBrack)
	return expr
}

func (p *Parser) zshSubFlags() *FlagsArithm {
	zf := &FlagsArithm{}
	// Lex flags as raw text, like paramExp does for ${(flags)...}.
	lparen := p.pos
	old := p.quote
	p.quote = runeByRune
	p.pos = p.nextPos()
	for p.newLit(p.r); p.r != utf8.RuneSelf && p.r != ')'; p.rune() {
	}
	p.val = p.endLit()
	if p.r != ')' {
		p.tok = _EOF
		p.matchingErr(lparen, leftParen, rightParen)
	}
	zf.Flags = p.lit(p.pos, p.val)
	p.rune()
	// Lex the argument as a raw pattern, stopping at ',' or ']',
	// since zsh treats it as a pattern rather than an arithmetic expression.
	argPos := p.nextPos()
	for p.newLit(p.r); p.r != utf8.RuneSelf && p.r != ',' && p.r != ']'; p.rune() {
	}
	if val := p.endLit(); val != "" {
		zf.X = p.wordOne(p.lit(argPos, val))
	}
	p.quote = old
	p.next()
	return zf
}

func (p *Parser) stopToken() bool {
	switch p.tok {
	case _EOF, _Newl, semicolon, and, or, andAnd, orOr, orAnd, andPipe, andBang,
		dblSemicolon, semiAnd, dblSemiAnd, semiOr, rightParen:
		return true
	case bckQuote:
		return p.backquoteEnd()
	}
	return false
}

func (p *Parser) backquoteEnd() bool {
	return p.lastBquoteEsc < p.openBquotes
}

// ValidName returns whether val is a valid name as per the POSIX spec.
func ValidName(val string) bool {
	if val == "" {
		return false
	}
	for i, r := range val {
		switch {
		case asciiLetter(r), r == '_':
		case i > 0 && asciiDigit(r):
		default:
			return false
		}
	}
	return true
}

func validFuncName(val string, posix bool) bool {
	if ValidName(val) {
		return true
	}
	if !posix || !strings.Contains(val, "/") {
		return false
	}
	for _, r := range val {
		switch {
		case asciiLetter(r), asciiDigit(r), r == '_', r == '/':
		default:
			return false
		}
	}
	return true
}

func numberLiteral[T string | []byte](val T) bool {
	if len(val) == 0 {
		return false
	}
	for _, r := range string(val) {
		if !asciiDigit(r) {
			return false
		}
	}
	return true
}

func (p *Parser) hasValidIdent() bool {
	if !p.tok.isLit() {
		return false
	}
	if end := p.eqlOffs; end > 0 {
		if p.val[end-1] == '+' && p.lang.in(langBashLike|LangMirBSDKorn|LangZsh) {
			end-- // a+=x
		}
		if ValidName(p.val[:end]) {
			return true
		}
	} else if !ValidName(p.val) {
		return false // *[i]=x
	}
	return p.r == '[' // a[i]=x
}

func (p *Parser) getAssign(needEqual bool) *Assign {
	as := &Assign{}
	if p.eqlOffs > 0 { // foo=bar
		nameEnd := p.eqlOffs
		if p.lang.in(langBashLike|LangMirBSDKorn|LangZsh) && p.val[p.eqlOffs-1] == '+' {
			// a+=b
			as.Append = true
			nameEnd--
		}
		as.Name = p.lit(p.pos, p.val[:nameEnd])
		// since we're not using the entire p.val
		as.Name.ValueEnd = posAddCol(as.Name.ValuePos, nameEnd)
		left := p.lit(posAddCol(p.pos, 1), p.val[p.eqlOffs+1:])
		if left.Value != "" {
			left.ValuePos = posAddCol(left.ValuePos, p.eqlOffs)
			as.Value = p.wordOne(left)
		}
		p.next()
	} else { // foo[x]=bar
		as.Name = p.lit(p.pos, p.val)
		// hasValidIdent already checks p.r is '['
		p.rune()
		p.pos = posAddCol(p.pos, 1)
		oldRawAssignIndex := p.rawAssignIndex
		oldNakedAssignIndex := p.nakedAssignIndex
		p.rawAssignIndex = true
		p.nakedAssignIndex = needEqual
		as.Index = p.eitherIndex()
		p.rawAssignIndex = oldRawAssignIndex
		p.nakedAssignIndex = oldNakedAssignIndex
		if p.spaced || p.stopToken() {
			if needEqual {
				as.Naked = true
				return as
			}
			as.Naked = true
			return as
		}
		if p.tok == assgnParen {
			if !p.lang.in(langBashLike | LangZsh) {
				p.curErr("arrays cannot be nested")
				return nil
			}
			// zsh allows a[i]=(values...). Bash parses it and
			// reports "cannot assign list to array member" at runtime.
			// assgnParen consumed both '=' and '(',
			// so rewrite as leftParen for array parsing below.
			p.tok = leftParen
			p.pos = posAddCol(p.pos, 1)
		} else {
			if len(p.val) > 0 && p.val[0] == '+' {
				as.Append = true
				p.val = p.val[1:]
				p.pos = posAddCol(p.pos, 1)
			}
			if len(p.val) < 1 || p.val[0] != '=' {
				if needEqual {
					as.Naked = true
					if len(p.val) == 0 {
						p.next()
					}
					return as
				}
				if as.Append {
					p.followErr(as.Pos(), "a[b]+", assgn)
				} else {
					p.followErr(as.Pos(), "a[b]", assgn)
				}
				return nil
			}
			p.pos = posAddCol(p.pos, 1)
			p.val = p.val[1:]
			if p.val == "" {
				p.next()
			}
		}
	}
	if p.spaced || p.stopToken() {
		return as
	}
	if as.Value == nil && p.tok == leftParen {
		p.checkLang(p.pos, langBashLike|LangMirBSDKorn|LangZsh, "arrays")
		as.Array = &ArrayExpr{Lparen: p.pos}
		newQuote := p.quote
		if p.lang.in(langBashLike | LangZsh) {
			newQuote = arrayElems
		}
		old := p.preNested(newQuote)
		p.next()
		p.got(_Newl)
		for p.tok != _EOF && p.tok != rightParen {
			ae := &ArrayElem{}
			ae.Comments, p.accComs = p.accComs, nil
			if p.tok == leftBrack && p.arrayElemIndex() {
				left := p.pos
				oldRawAssignIndex := p.rawAssignIndex
				p.rawAssignIndex = true
				ae.Index = p.eitherIndex()
				p.rawAssignIndex = oldRawAssignIndex
				if p.tok == assgnParen {
					p.curErr("arrays cannot be nested")
					return nil
				}
				if p.tok == addAssgn {
					// Bash 5.x `[idx]+=value` — append to the
					// existing element rather than overwrite.
					ae.Append = true
					p.next()
				} else {
					p.follow(left, `[x]`, assgn)
				}
			}
			if ae.Value = p.getWord(); ae.Value == nil {
				switch p.tok {
				case leftBrack:
					if ae.Index != nil {
						break
					}
					ae.Value = &Word{Parts: []WordPart{
						&Lit{ValuePos: p.pos, ValueEnd: posAddCol(p.pos, 1), Value: p.tok.String()},
					}}
					p.next()
				case rightBrack:
					ae.Value = &Word{Parts: []WordPart{
						&Lit{ValuePos: p.pos, ValueEnd: posAddCol(p.pos, 1), Value: p.tok.String()},
					}}
					p.next()
				case assgn:
					ae.Value = &Word{Parts: []WordPart{
						&Lit{ValuePos: p.pos, ValueEnd: posAddCol(p.pos, 1), Value: "="},
					}}
					p.next()
				case _Newl, rightParen:
					// TODO: support [index]=[
				default:
					p.curErr("array element values must be words")
					return nil
				}
			}
			if len(p.accComs) > 0 {
				c := p.accComs[0]
				if c.Pos().Line() == ae.End().Line() {
					ae.Comments = append(ae.Comments, c)
					p.accComs = p.accComs[1:]
				}
			}
			as.Array.Elems = append(as.Array.Elems, ae)
			p.got(_Newl)
		}
		as.Array.Last, p.accComs = p.accComs, nil
		p.postNested(old)
		as.Array.Rparen = p.matched(as.Array.Lparen, leftParen, rightParen)
	} else if w := p.getWord(); w != nil {
		if as.Value == nil {
			as.Value = w
		} else {
			as.Value.Parts = append(as.Value.Parts, w.Parts...)
		}
	}
	return as
}

func (p *Parser) arrayElemIndex() bool {
	if !p.lang.in(langBashExact) {
		return true
	}
	i := int(p.pos.Offset()) - int(p.offs) + 1
	return i >= len(p.bs) || (p.bs[i] != ' ' && p.bs[i] != '\t')
}

func (p *Parser) peekRedir() bool {
	switch p.tok {
	case _LitRedir, rdrOut, appOut, rdrIn, rdrInOut, dplIn, dplOut,
		rdrClob, appClob, hdoc, dashHdoc, wordHdoc,
		rdrAll, rdrAllClob, appAll, appAllClob:
		return true
	}
	return false
}

func (p *Parser) doRedirect(s *Stmt) {
	var r *Redirect
	if s.Redirs == nil {
		var alloc struct {
			redirs [4]*Redirect
			redir  Redirect
		}
		s.Redirs = alloc.redirs[:0]
		r = &alloc.redir
		s.Redirs = append(s.Redirs, r)
	} else {
		r = &Redirect{}
		s.Redirs = append(s.Redirs, r)
	}
	r.N = p.getLit()
	if r.N != nil && r.N.Value[0] == '{' {
		p.checkLang(r.N.Pos(), langBashLike, "`{varname}` redirects")
	}
	r.Op, r.OpPos = RedirOperator(p.tok), p.pos
	switch r.Op {
	case RdrAll, AppAll:
		p.checkLang(p.pos, langBashLike|LangMirBSDKorn|LangZsh, "%#q redirects", r.Op)
	case AppClob, RdrAllClob, AppAllClob:
		p.checkLang(p.pos, LangZsh, "%#q redirects", r.Op)
	}
	p.next()
	switch r.Op {
	case Hdoc, DashHdoc:
		old := p.quote
		p.quote, p.forbidNested = hdocWord, true
		p.heredocs = append(p.heredocs, r)
		// An empty delimiter (`<<` with nothing but a newline or EOF
		// after it) is a syntax error even under HeredocEOFWarning:
		// bash 5.3 reports "syntax error near unexpected token
		// `newline'" and exits 2. The warning is only for a heredoc
		// with a real delimiter whose body runs off the end of input.
		r.Word = p.followWordTok(token(r.Op), r.OpPos)
		p.quote, p.forbidNested = old, false
		if p.tok == _Newl {
			if len(p.accComs) > 0 {
				c := p.accComs[0]
				if c.Pos().Line() == s.End().Line() {
					s.Comments = append(s.Comments, c)
					p.accComs = p.accComs[1:]
				}
			}
			p.doHeredocs()
		}
	case WordHdoc:
		p.checkLang(r.OpPos, langBashLike|LangMirBSDKorn|LangZsh, "herestrings")
		fallthrough
	default:
		r.Word = p.followWordTok(token(r.Op), r.OpPos)
	}
	if p.lang.in(LangBashPP) && r.Op == RdrIn &&
		r.Word != nil && r.Word.Pos().Offset() != r.OpPos.Offset()+1 && bashppLeadingDash(r.Word) {
		r.BashPPKeepSpace = true
	}
}

func (p *Parser) getStmt(readEnd, binCmd, fnBody bool) *Stmt {
	pos, ok := p.gotRsrv("!")
	s := &Stmt{Position: pos}
	if ok {
		s.Negated = true
		if p.stopToken() {
			// bash treats a lone `!` as `! true` (no command body,
			// just the negation), so the stmt resolves to exit
			// status 1. Mark it negated and let the caller see an
			// otherwise-empty Stmt.
			if p.lang.in(langBashExact) {
				if readEnd {
					p.stmtTerm(s)
				}
				return s
			}
			p.posErr(s.Pos(), `%#q cannot form a statement alone`, exclMark)
		}
		if _, ok := p.gotRsrv("!"); ok {
			// bash 5.3 allows `! ! cmd` (`! true` semantics on the
			// inner `!`, then outer negation flips again). Each
			// additional `!` toggles s.Negated.
			if p.lang.in(langBashExact) {
				s.Negated = !s.Negated
				for {
					if _, ok := p.gotRsrv("!"); !ok {
						break
					}
					s.Negated = !s.Negated
				}
				if p.stopToken() {
					// `! !` (or longer chains) alone — same
					// treatment as a single `!`: empty stmt with
					// negation applied.
					if readEnd {
						p.stmtTerm(s)
					}
					return s
				}
			} else {
				p.posErr(s.Pos(), `cannot negate a command multiple times`)
			}
		}
	}
	if s = p.gotStmtPipe(s, false); s == nil || p.err != nil {
		return nil
	}
	if fnBody {
		return s
	}
	// instead of using recursion, iterate manually
	for p.tok == andAnd || p.tok == orOr {
		if binCmd {
			// left associativity: in a list of BinaryCmds, the
			// right recursion should only read a single element
			return s
		}
		b := &BinaryCmd{
			OpPos: p.pos,
			Op:    BinCmdOperator(p.tok),
			X:     s,
		}
		p.next()
		p.got(_Newl)
		b.Y = p.getStmt(false, true, false)
		if b.Y == nil || p.err != nil {
			if p.recoverError() {
				b.Y = &Stmt{Position: recoveredPos}
			} else {
				p.followErr(b.OpPos, b.Op, noQuote("a statement"))
				return nil
			}
		}
		s = &Stmt{Position: s.Position}
		s.Cmd = b
		s.Comments, b.X.Comments = b.X.Comments, nil
	}
	if readEnd {
		p.stmtTerm(s)
	}
	if len(p.accComs) > 0 && !binCmd && !fnBody {
		c := p.accComs[0]
		if c.Pos().Line() == s.End().Line() {
			s.Comments = append(s.Comments, c)
			p.accComs = p.accComs[1:]
		}
	}
	return s
}

func (p *Parser) stmtTerm(s *Stmt) {
	switch p.tok {
	case semicolon:
		s.Semicolon = p.pos
		p.next()
	case and:
		s.Semicolon = p.pos
		p.next()
		s.Background = true
	case orAnd:
		s.Semicolon = p.pos
		p.next()
		s.Coprocess = true
	case andPipe, andBang:
		s.Semicolon = p.pos
		p.next()
		s.Disown = true
	}
}

func (p *Parser) gotStmtPipe(s *Stmt, binCmd bool) *Stmt {
	s.Comments, p.accComs = p.accComs, nil
	for p.peekRedir() {
		p.doRedirect(s)
	}
	redirsStart := len(s.Redirs)
	switch p.tok {
	case _LitWord:
		if p.lang.in(LangBashPP) && (p.bashppLabel(s) || p.bashppKeywordStatement(s)) {
			break
		}
		if p.lang.in(LangBashPP) && strings.HasPrefix(p.val, "@") && p.bashppDecoratedDecl(s) {
			break
		}
		if p.lang.in(LangBashPP) && p.pos.Col() == 1 && RecognizeStartSite(p.val).Site == StartSource {
			txn := p.beginBashPPTxn()
			if block := p.bashppSourceBlock(); block != nil {
				txn.commit(p)
				s.Cmd = block
				break
			}
			txn.rollback(p)
		}
		switch p.val {
		case "{":
			p.block(s)
		case "{}":
			// Zsh treats closing braces in a special way, allowing this.
			if p.lang.in(LangZsh) {
				s.Cmd = &Block{Lbrace: p.pos, Rbrace: posAddCol(p.pos, 1)}
				p.next()
			}
		case "if":
			if p.lang.in(LangBashPP) && p.bashppFuncDepth > 0 && p.bashppIf(s) {
				break
			}
			p.ifClause(s)
		case "while", "until":
			// TODO(zsh): "repeat"
			controls := p.bashppControls
			p.bashppControls = nil
			p.whileClause(s, p.val == "until")
			p.bashppControls = controls
		case "for":
			if p.lang.in(LangBashPP) && (p.bashppFuncDepth > 0 || p.bashppChanCopy) && p.bashppRange(s) {
				break
			}
			if p.lang.in(LangBashPP) && p.bashppFuncDepth > 0 && p.bashppFor(s) {
				break
			}
			controls := p.bashppControls
			p.bashppControls = nil
			p.forClause(s)
			p.bashppControls = controls
		case "case":
			p.caseClause(s)
		case "switch":
			if p.lang.in(LangBashPP) && p.bashppFuncDepth > 0 {
				p.bashppSwitch(s)
			}
		// TODO(zsh): { try-list } "always" { always-list }
		case "}":
			p.curErr(`%#q can only be used to close a block`, rightBrace)
		case "then", "elif":
			p.curErr("%#q can only be used in an `if`", p.val)
		case "fi":
			p.curErr("%#q can only be used to end an `if`", p.val)
		case "do":
			p.curErr(`%#q can only be used in a loop`, p.val)
		case "done":
			// A Bash++ short declaration may bind a Go identifier whose text is
			// also a shell reserved word. Probe the complete statement before
			// diagnosing `done`: only a supported typed short declaration claims
			// it, while an actual loop terminator is rewound byte for byte.
			if p.lang.in(LangBashPP) && p.bashppFuncDepth > 0 {
				txn := p.beginBashPPTxn()
				probe := &Stmt{Position: s.Position}
				name := p.wordOne(p.lit(p.pos, p.val))
				p.next()
				p.callExpr(probe, name, false)
				if p.err == nil {
					if _, ok := probe.Cmd.(*BashPPShortDecl); ok {
						txn.commit(p)
						s.Cmd, s.Redirs = probe.Cmd, probe.Redirs
						break
					}
				}
				txn.rollback(p)
			}
			p.curErr(`%#q can only be used to end a loop`, p.val)
		case "esac":
			p.curErr("%#q can only be used to end a `case`", p.val)
		case "in":
			// `for`/`case` consume their own `in`, so reaching here means
			// it was used as a command name -- a syntax error in bash.
			p.curErr("%#q can only be used in a `for` or `case`", p.val)
		case "!":
			if !s.Negated && p.lang.in(langBashExact) {
				// bash 5.3 accepts `time ! cmd`, `! ! cmd`, and
				// other contexts where a `!` precedes the inner
				// statement after a controlling keyword. Consume
				// the `!` and continue parsing the rest of the
				// pipeline as the negated body.
				s.Negated = true
				p.next()
				return p.gotStmtPipe(s, binCmd)
			}
			if !s.Negated {
				p.curErr(`%#q can only be used in full statements`, exclMark)
				break
			}
		case "[[":
			if p.lang.in(langBashLike | LangMirBSDKorn | LangZsh) {
				p.testClause(s)
			}
		case "]]":
			if p.lang.in(langBashLike | LangMirBSDKorn | LangZsh) {
				p.curErr(`%#q can only be used to close a test`, dblRightBrack)
			}
		case "let":
			if p.lang.in(langBashLike | LangMirBSDKorn | LangZsh) {
				p.letClause(s)
			}
		case "function":
			if p.lang.in(langBashLike | LangMirBSDKorn | LangZsh) {
				p.bashFuncDecl(s)
			}
		case "agentic":
			if p.lang.in(LangBashPP) {
				p.bashppAgentic(s)
			}
		case "declare":
			if p.lang.in(langBashLike | LangZsh) { // Note that mksh lacks this one.
				p.declClause(s)
			}
		case "local", "typeset", "nameref":
			if p.lang.in(langBashLike | LangMirBSDKorn | LangZsh) {
				p.declClause(s)
			}
		case "export", "readonly":
			if p.lang.in(langBashLike|LangMirBSDKorn|LangZsh) && p.r != '(' && p.peek() != '(' {
				p.declClause(s)
			}
		case "time":
			if p.lang.in(langBashLike | LangMirBSDKorn | LangZsh) {
				p.timeClause(s)
			}
		case "coproc":
			if p.lang.in(langBashLike) { // Note that mksh lacks this one.
				p.coprocClause(s)
			}
		case "select":
			if p.lang.in(LangBashPP) && (p.bashppFuncDepth > 0 || p.bashppChanCopy) && p.bashppSelect(s) {
				break
			}
			if p.lang.in(langBashLike | LangMirBSDKorn | LangZsh) {
				controls := p.bashppControls
				p.bashppControls = nil
				p.selectClause(s)
				p.bashppControls = controls
			}
		case "@test":
			if p.lang.in(LangBats) {
				p.testDecl(s)
			}
		}
		if s.Cmd != nil {
			break
		}
		if p.hasValidIdent() {
			p.callExpr(s, nil, true)
			break
		}
		name := p.lit(p.pos, p.val)
		p.next()
		if p.lang.in(LangBashPP) && p.tok == leftParen {
			if cmd := p.bashppFuncForm(&CallExpr{Args: []*Word{p.wordOne(name)}}); cmd != nil {
				s.Cmd = cmd
				break
			}
		}
		if p.lang.in(LangBashPP) && p.tok == leftParen {
			if cmd := p.bashppConstGroup(&CallExpr{Args: []*Word{p.wordOne(name)}}); cmd != nil {
				s.Cmd = cmd
				break
			}
		}
		if p.lang.in(LangBashPP) && p.tok == leftParen {
			if cmd := p.bashppImportGroup(&CallExpr{Args: []*Word{p.wordOne(name)}}); cmd != nil {
				s.Cmd = cmd
				break
			}
		}
		if p.lang.in(LangBashPP) && p.tok == leftParen && !p.spaced {
			if cmd := p.bashppParenForm(&CallExpr{Args: []*Word{p.wordOne(name)}}); cmd != nil {
				s.Cmd = cmd
				break
			}
		}
		// In zsh, ( after a word is a glob qualifier unless followed
		// immediately by ), which is the func declaration syntax.
		if p.tok == leftParen && (!p.lang.in(LangZsh) || p.r == ')') {
			p.next()
			p.follow(name.ValuePos, "foo(", rightParen)
			// Only the pure-POSIX grammar restricts function names to POSIX
			// "names". bash's posix MODE keeps the bash extension that allows
			// hyphens, dots, etc. (`test-hyphen() {}` runs under `bash --posix`),
			// so don't reject those when posixMode is layered on a bash grammar.
			if p.lang.in(LangPOSIX) && !validFuncName(name.Value, true) {
				p.posErr(name.Pos(), "invalid func name")
			}
			p.funcDecl(s, name.ValuePos, false, true, name)
		} else {
			w := p.wordOne(name)
			if p.lang.in(LangZsh) && !p.spaced {
				w.Parts = append(w.Parts, p.wordParts(nil)...)
			}
			p.callExpr(s, w, false)
		}
	case bckQuote:
		if p.backquoteEnd() {
			break
		}
		fallthrough
	case _Lit, dollBrace, dollDblParen, dollParen, dollar, cmdIn, assgnParen, cmdOut,
		sglQuote, dollSglQuote, dblQuote, dollDblQuote, dollBrack,
		globQuest, globStar, globPlus, globAt, globExcl:
		if p.hasValidIdent() {
			p.callExpr(s, nil, true)
			break
		}
		w := p.wordAnyNumber()
		if p.lang.in(LangBashPP) && p.tok == leftParen && !p.spaced {
			if cmd := p.bashppParenForm(&CallExpr{Args: []*Word{w}}); cmd != nil {
				s.Cmd = cmd
				break
			}
		}
		if p.got(leftParen) {
			// Bash 5.3 accepts `<(:) ()` / `'a b' ()` etc. at parse
			// time and emits "not a valid identifier" at runtime.
			// Reconstruct the source text of the word as the name
			// and let the interpreter validate it. Other languages
			// still reject at parse time.
			if p.lang.in(langBashExact) {
				name := funcNameWordText(w)
				if name == "" {
					p.posErr(w.Pos(), "invalid func name")
				}
				lit := &Lit{ValuePos: w.Pos(), ValueEnd: w.End(), Value: name}
				p.follow(w.Pos(), "foo(", rightParen)
				p.funcDecl(s, w.Pos(), false, true, lit)
				break
			}
			p.posErr(w.Pos(), "invalid func name")
		}
		p.callExpr(s, w, false)
	case leftParen:
		if p.lang.in(LangBashPP) {
			if cmd := p.bashppPointerMethodExpr(); cmd != nil {
				s.Cmd = cmd
				break
			}
		}
		if p.r == ')' {
			p.rune()
			fpos := p.pos
			p.next()
			if p.tok == _LitWord && p.val == "{" {
				p.checkLang(fpos, LangZsh, "anonymous functions")
			}
			p.funcDecl(s, fpos, false, true)
			break
		}
		p.subshell(s)
	case dblLeftParen:
		p.arithmExpCmd(s)
	}
	if s.Cmd == nil && len(s.Redirs) == 0 {
		return nil // no statement found
	}
	if redirsStart > 0 && s.Cmd != nil {
		if _, ok := s.Cmd.(*CallExpr); !ok {
			p.checkLang(s.Pos(), LangZsh, "redirects before compound commands")
		}
	}
	for p.peekRedir() {
		p.doRedirect(s)
	}
	// A discarded receive, `<-done`, has no command at all: bash reads it as a
	// redirect from a file named `-done`, so it arrives here as a statement
	// whose only content is that redirect. Reclassifying it needs the redirect
	// list to be final, which is why this sits after the trailing loop rather
	// than beside the other Bash++ dispatch in callExpr.
	if s.Cmd == nil && p.lang.in(LangBashPP) {
		if cmd := bashppChanForm(nil, s.Redirs); cmd != nil {
			s.Cmd = cmd
			s.Redirs = nil
		}
	}
	// instead of using recursion, iterate manually
	for p.tok == or || p.tok == orAnd {
		if binCmd {
			// left associativity: in a list of BinaryCmds, the
			// right recursion should only read a single element
			return s
		}
		if p.tok == orAnd && p.lang.in(LangMirBSDKorn) {
			// No need to check for LangPOSIX, as on that language
			// we parse |& as two tokens.
			break
		}
		b := &BinaryCmd{OpPos: p.pos, Op: BinCmdOperator(p.tok), X: s}
		p.next()
		p.got(_Newl)
		if b.Y = p.gotStmtPipe(&Stmt{Position: p.pos}, true); b.Y == nil || p.err != nil {
			if p.recoverError() {
				b.Y = &Stmt{Position: recoveredPos}
			} else {
				p.followErr(b.OpPos, b.Op, noQuote("a statement"))
				break
			}
		}
		s = &Stmt{Position: s.Position}
		s.Cmd = b
		s.Comments, b.X.Comments = b.X.Comments, nil
		// in "! x | y", the bang applies to the entire pipeline
		s.Negated = b.X.Negated
		b.X.Negated = false
	}
	return s
}

func (p *Parser) subshell(s *Stmt) {
	sub := &Subshell{Lparen: p.pos}
	old := p.preNested(subCmd)
	p.next()
	// A subshell is a fresh execution context: a `return` inside it belongs to
	// no enclosing Bash++ func, so the Go-form depth does not cross the `(`.
	savedFuncDepth := p.bashppFuncDepth
	savedChanCopy := p.bashppChanCopy
	savedControls := p.bashppControls
	p.bashppFuncDepth = 0
	p.bashppChanCopy = savedChanCopy || savedFuncDepth > 0
	p.bashppControls = nil
	sub.Stmts, sub.Last = p.followStmts("(", sub.Lparen)
	p.bashppFuncDepth = savedFuncDepth
	p.bashppChanCopy = savedChanCopy
	p.bashppControls = savedControls
	p.postNested(old)
	if p.err == nil && p.tok == _EOF && p.heredocEOFWarning != nil && p.hdocEOFLine != 0 {
		// A here-document inside this subshell ran to end-of-file
		// (we already emitted bash 5.3's warning for it), so the `)`
		// can never arrive. Report bash's wording, anchored at the
		// EOF position rather than at the `(`.
		p.posErr(p.nextPos(), "syntax error: unexpected end of file from `(' command on line %d", sub.Lparen.Line())
	}
	sub.Rparen = p.matched(sub.Lparen, leftParen, rightParen)
	s.Cmd = sub
}

func (p *Parser) arithmExpCmd(s *Stmt) {
	ar := &ArithmCmd{Left: p.pos}
	old := p.preNested(arithmExprCmd)
	p.next()
	if p.got(hash) {
		p.checkLang(ar.Pos(), LangMirBSDKorn, "unsigned expressions")
		ar.Unsigned = true
	}
	ar.X = p.followArithm(dblLeftParen, ar.Left)
	ar.Right = p.arithmEnd(dblLeftParen, ar.Left, old)
	s.Cmd = ar
}

func (p *Parser) block(s *Stmt) {
	p.blockWithEmpty(s, false)
}

// bashppBlock parses a block in an already committed Go region. Unlike a
// shell compound command, a Go block may have an empty statement list.
func (p *Parser) bashppBlock(s *Stmt) {
	p.blockWithEmpty(s, true)
}

func (p *Parser) blockWithEmpty(s *Stmt, allowEmpty bool) {
	b := &Block{Lbrace: p.pos}
	if allowEmpty && p.tok == _LitWord && p.val == "{}" {
		b.Rbrace = posAddCol(p.pos, 1)
		p.next()
		s.Cmd = b
		return
	}
	p.next()
	if allowEmpty {
		b.Stmts, b.Last = p.stmtList("}")
	} else {
		b.Stmts, b.Last = p.followStmts("{", b.Lbrace, "}")
	}
	if pos, ok := p.gotRsrv("}"); ok {
		b.Rbrace = pos
	} else if p.recoverError() {
		b.Rbrace = recoveredPos
	} else {
		p.matchingErr(b.Lbrace, leftBrace, rightBrace)
	}
	s.Cmd = b
}

func (p *Parser) ifClause(s *Stmt) {
	rootIf := &IfClause{Position: p.pos}
	p.next()
	rootIf.Cond, rootIf.CondLast = p.followStmts("if", rootIf.Position, "then")
	rootIf.ThenPos = p.followRsrv(rootIf.Position, "if <cond>", "then")
	rootIf.Then, rootIf.ThenLast = p.followStmts("then", rootIf.ThenPos, "fi", "elif", "else")
	curIf := rootIf
	for p.tok == _LitWord && p.val == "elif" {
		elf := &IfClause{Position: p.pos}
		curIf.Last = p.accComs
		p.accComs = nil
		p.next()
		elf.Cond, elf.CondLast = p.followStmts("elif", elf.Position, "then")
		elf.ThenPos = p.followRsrv(elf.Position, "elif <cond>", "then")
		elf.Then, elf.ThenLast = p.followStmts("then", elf.ThenPos, "fi", "elif", "else")
		curIf.Else = elf
		curIf = elf
	}
	if elsePos, ok := p.gotRsrv("else"); ok {
		curIf.Last = p.accComs
		p.accComs = nil
		els := &IfClause{Position: elsePos}
		els.Then, els.ThenLast = p.followStmts("else", els.Position, "fi")
		curIf.Else = els
		curIf = els
	}
	curIf.Last = p.accComs
	p.accComs = nil
	rootIf.FiPos = p.stmtEnd(rootIf, "if", "fi")
	for els := rootIf.Else; els != nil; els = els.Else {
		// All the nested IfClauses share the same FiPos.
		els.FiPos = rootIf.FiPos
	}
	s.Cmd = rootIf
}

func (p *Parser) whileClause(s *Stmt, until bool) {
	wc := &WhileClause{WhilePos: p.pos, Until: until}
	rsrv := "while"
	rsrvCond := "while <cond>"
	if wc.Until {
		rsrv = "until"
		rsrvCond = "until <cond>"
	}
	p.next()
	wc.Cond, wc.CondLast = p.followStmts(rsrv, wc.WhilePos, "do")
	wc.DoPos = p.followRsrv(wc.WhilePos, rsrvCond, "do")
	wc.Do, wc.DoLast = p.followStmts("do", wc.DoPos, "done")
	wc.DonePos = p.stmtEnd(wc, rsrv, "done")
	s.Cmd = wc
}

func (p *Parser) forClause(s *Stmt) {
	fc := &ForClause{ForPos: p.pos}
	p.next()
	fc.Loop = p.loop(fc.ForPos)

	start, end := "do", "done"
	if pos, ok := p.gotRsrv("{"); ok {
		p.checkLang(pos, langBashLike|LangMirBSDKorn, "for loops with braces")
		fc.DoPos = pos
		fc.Braces = true
		start, end = "{", "}"
	} else {
		fc.DoPos = p.followRsrv(fc.ForPos, "for foo [in words]", start)
	}

	s.Comments = append(s.Comments, p.accComs...)
	p.accComs = nil
	fc.Do, fc.DoLast = p.followStmts(start, fc.DoPos, end)
	fc.DonePos = p.stmtEnd(fc, "for", end)
	s.Cmd = fc
}

func (p *Parser) loop(fpos Pos) Loop {
	switch p.tok {
	case leftParen, dblLeftParen:
		p.checkLang(p.pos, langBashLike|LangZsh, "c-style fors")
	}
	if p.tok == dblLeftParen {
		cl := &CStyleLoop{Lparen: p.pos}
		old := p.preNested(arithmExprCmd)
		p.next()
		cl.Init = p.arithmExpr(false)
		if !p.got(dblSemicolon) {
			p.follow(p.pos, "expr", semicolon)
			cl.Cond = p.arithmExpr(false)
			p.follow(p.pos, "expr", semicolon)
		}
		cl.Post = p.arithmExpr(false)
		cl.Rparen = p.arithmEnd(dblLeftParen, cl.Lparen, old)
		p.got(semicolon)
		p.got(_Newl)
		return cl
	}
	return p.wordIter("for", fpos)
}

func (p *Parser) wordIter(ftok string, fpos Pos) *WordIter {
	wi := &WordIter{}
	if wi.Name = p.getLit(); wi.Name == nil {
		if p.lang.in(langBashLike | LangMirBSDKorn) {
			if w := p.getWord(); w != nil {
				if wordHasParamExp(w) {
					wi.Name = &Lit{
						ValuePos: w.Pos(),
						ValueEnd: w.End(),
						Value:    funcNameWordText(w),
					}
				} else {
					// Allow any word as the iterator variable name; the
					// interpreter will validate that it is a valid identifier.
					wi.Name = &Lit{
						ValuePos: w.Pos(),
						ValueEnd: w.End(),
						Value:    w.Lit(),
					}
				}
			}
		}
		if wi.Name == nil {
			p.followErr(fpos, ftok, noQuote("a literal"))
		}
	}
	if p.got(semicolon) {
		p.got(_Newl)
		return wi
	}
	p.got(_Newl)
	if pos, ok := p.gotRsrv("in"); ok {
		wi.InPos = pos
		for !p.stopToken() {
			if w := p.getWord(); w == nil {
				p.curErr("word list can only contain words")
			} else {
				wi.Items = append(wi.Items, w)
			}
		}
		p.got(semicolon)
		p.got(_Newl)
	} else if p.tok == _LitWord && p.val == "do" {
	} else {
		p.followErr(fpos, ftok+" foo", noQuote("`in`, `do`, `;`, or a newline"))
	}
	return wi
}

func wordHasParamExp(w *Word) bool {
	for _, part := range w.Parts {
		if _, ok := part.(*ParamExp); ok {
			return true
		}
	}
	return false
}

func (p *Parser) selectClause(s *Stmt) {
	fc := &ForClause{ForPos: p.pos, Select: true}
	p.next()
	fc.Loop = p.wordIter("select", fc.ForPos)
	fc.DoPos = p.followRsrv(fc.ForPos, "select foo [in words]", "do")
	fc.Do, fc.DoLast = p.followStmts("do", fc.DoPos, "done")
	fc.DonePos = p.stmtEnd(fc, "select", "done")
	s.Cmd = fc
}

func (p *Parser) caseClause(s *Stmt) {
	cc := &CaseClause{Case: p.pos}
	p.next()
	cc.Word = p.getWord()
	if cc.Word == nil {
		p.followErr(cc.Case, "case", noQuote("a word"))
	}
	end := "esac"
	p.got(_Newl)
	if pos, ok := p.gotRsrv("{"); ok {
		cc.In = pos
		cc.Braces = true
		p.checkLang(cc.Pos(), LangMirBSDKorn, "`case i {`")
		end = "}"
	} else {
		cc.In = p.followRsrv(cc.Case, "case x", "in")
	}
	cc.Items = p.caseItems(end)
	cc.Last, p.accComs = p.accComs, nil
	cc.Esac = p.stmtEnd(cc, "case", end)
	s.Cmd = cc
}

func (p *Parser) caseItems(stop string) (items []*CaseItem) {
	p.got(_Newl)
	for p.tok != _EOF && (p.tok != _LitWord || p.val != stop) {
		ci := &CaseItem{}
		ci.Comments, p.accComs = p.accComs, nil
		p.got(leftParen)
		for p.tok != _EOF {
			if w := p.getWord(); w == nil {
				p.curErr("case patterns must consist of words")
			} else {
				ci.Patterns = append(ci.Patterns, w)
			}
			if p.tok == rightParen {
				break
			}
			if !p.got(or) {
				p.curErr("case patterns must be separated with %#q", or)
			}
		}
		old := p.preNested(switchCase)
		p.next()
		ci.Stmts, ci.Last = p.stmtList(stop)
		p.postNested(old)
		switch p.tok {
		case dblSemicolon, semiAnd, dblSemiAnd, semiOr:
		default:
			ci.Op = Break
			items = append(items, ci)
			return items
		}
		ci.Last = append(ci.Last, p.accComs...)
		p.accComs = nil
		ci.OpPos = p.pos
		ci.Op = CaseOperator(p.tok)
		p.next()
		p.got(_Newl)

		// Split the comments:
		//
		// case x in
		// a)
		//   foo
		//   ;;
		//   # comment for a
		// # comment for b
		// b)
		//   [...]
		split := len(p.accComs)
		for i, c := range slices.Backward(p.accComs) {
			if c.Pos().Col() != p.pos.Col() {
				break
			}
			split = i
		}
		ci.Comments = append(ci.Comments, p.accComs[:split]...)
		p.accComs = p.accComs[split:]

		items = append(items, ci)
	}
	return items
}

func (p *Parser) testClause(s *Stmt) {
	tc := &TestClause{Left: p.pos}
	old := p.preNested(testExpr)
	p.next()
	if tc.X = p.testExprBinary(false); tc.X == nil {
		p.followErrExp(tc.Left, dblLeftBrack)
	}
	tc.Right = p.pos
	if _, ok := p.gotRsrv("]]"); !ok {
		p.matchingErr(tc.Left, dblLeftBrack, dblRightBrack)
	}
	p.postNested(old)
	s.Cmd = tc
}

func (p *Parser) testExprBinary(pastAndOr bool) TestExpr {
	p.got(_Newl)
	var left TestExpr
	if pastAndOr {
		left = p.testExprUnary()
	} else {
		left = p.testExprBinary(true)
	}
	if left == nil {
		return left
	}
	p.got(_Newl)
	switch p.tok {
	case andAnd, orOr:
	case _LitWord:
		if p.val == "]]" {
			return left
		}
		if p.tok = token(testBinaryOp(p.val)); p.tok == illegalTok {
			p.curErr("not a valid test operator: %#q", p.val)
		}
	case rdrIn, rdrOut:
	case _EOF, rightParen:
		return left
	case _Lit:
		p.curErr("test operator words must consist of a single literal")
	case _LitRedir:
		// A redirection-shaped token such as `3<` cannot be a binary
		// test operator; report the literal (its leading fd digits)
		// rather than the internal token name.
		p.curErr("not a valid test operator: %#q", p.val)
	default:
		p.curErr("not a valid test operator: %#q", p.tok)
	}
	b := &BinaryTest{
		OpPos: p.pos,
		Op:    BinTestOperator(p.tok),
		X:     left,
	}
	switch b.Op {
	case AndTest, OrTest:
		p.next()
		if b.Y = p.testExprBinary(false); b.Y == nil {
			p.followErrExp(b.OpPos, b.Op)
		}
	case TsReMatch:
		p.checkLang(p.pos, langBashLike|LangZsh, "regex tests")
		p.rxOpenParens = 0
		p.rxFirstPart = true
		// TODO(mvdan): Using nested states within a regex will break in
		// all sorts of ways. The better fix is likely to use a stop
		// token, like we do with heredocs.
		p.quote = testExprRegexp
		fallthrough
	default:
		if _, ok := b.X.(*Word); !ok {
			p.posErr(b.OpPos, "expected %#q, %#q or %#q after complex expr",
				AndTest, OrTest, dblRightBrack)
		}
		p.next()
		b.Y = p.testFollowWord(token(b.Op), b.OpPos)
	}
	return b
}

// testExprNotBody parses the operand of a `!` unary in `[[ ]]`.
// `!` binds tighter than `&&` / `||` but looser than binary
// primaries — so we accept a single unary primary, optionally
// followed by one binary-primary op and its RHS, but never a
// `&&` / `||` chain (which the outer testExprBinary handles).
func (p *Parser) testExprNotBody() TestExpr {
	p.got(_Newl)
	left := p.testExprUnary()
	if left == nil {
		return nil
	}
	p.got(_Newl)
	switch p.tok {
	case andAnd, orOr, _EOF, rightParen:
		return left
	case _LitWord:
		if p.val == "]]" {
			return left
		}
		op := token(testBinaryOp(p.val))
		if op == illegalTok {
			p.curErr("not a valid test operator: %#q", p.val)
		}
		p.tok = op
	case rdrIn, rdrOut:
		// `<` / `>` as string-compare binary primaries.
	case _Lit:
		p.curErr("test operator words must consist of a single literal")
	case _LitRedir:
		// A redirection-shaped token such as `3<` cannot be a binary
		// test operator; report the literal (its leading fd digits)
		// rather than the internal token name.
		p.curErr("not a valid test operator: %#q", p.val)
	default:
		p.curErr("not a valid test operator: %#q", p.tok)
	}
	b := &BinaryTest{
		OpPos: p.pos,
		Op:    BinTestOperator(p.tok),
		X:     left,
	}
	if b.Op == AndTest || b.Op == OrTest {
		// Unreachable given the cases above, but guard against
		// future operator additions.
		return left
	}
	if b.Op == TsReMatch {
		p.checkLang(p.pos, langBashLike|LangZsh, "regex tests")
		p.rxOpenParens = 0
		p.rxFirstPart = true
		p.quote = testExprRegexp
	}
	if _, ok := b.X.(*Word); !ok {
		p.posErr(b.OpPos, "expected %#q, %#q or %#q after complex expr",
			AndTest, OrTest, dblRightBrack)
	}
	p.next()
	b.Y = p.testFollowWord(token(b.Op), b.OpPos)
	return b
}

func (p *Parser) testExprUnary() TestExpr {
	switch p.tok {
	case _EOF, rightParen:
		return nil
	case _LitWord:
		op := token(testUnaryOp(p.val))
		switch op {
		case illegalTok:
		case tsRefVar, tsModif: // not available in mksh
			if p.lang.in(langBashLike) {
				p.tok = op
			}
		default:
			p.tok = op
		}
	}
	switch p.tok {
	case exclMark:
		u := &UnaryTest{OpPos: p.pos, Op: TsNot}
		p.next()
		// `!` binds tighter than `&&` / `||` but looser than binary
		// primaries (`<`, `==`, `-eq`, …): `! a || b` is
		// `(! a) || b`, while `! a < b` is `! (a < b)`. Parse a
		// single unary primary, then optionally one binary-primary
		// op + RHS, then let the outer testExprBinary handle any
		// `&&` / `||` chain.
		if u.X = p.testExprNotBody(); u.X == nil {
			p.followErrExp(u.OpPos, u.Op)
		}
		return u
	case tsExists, tsRegFile, tsDirect, tsCharSp, tsBlckSp, tsNmPipe,
		tsSocket, tsSmbLink, tsSticky, tsGIDSet, tsUIDSet, tsGrpOwn,
		tsUsrOwn, tsModif, tsRead, tsWrite, tsExec, tsNoEmpty,
		tsFdTerm, tsEmpStr, tsNempStr, tsOptSet, tsVarSet, tsRefVar:
		u := &UnaryTest{OpPos: p.pos, Op: UnTestOperator(p.tok)}
		p.next()
		u.X = p.testFollowWord(token(u.Op), u.OpPos)
		return u
	case leftParen:
		pe := &ParenTest{Lparen: p.pos}
		p.next()
		if pe.X = p.testExprBinary(false); pe.X == nil {
			p.followErrExp(pe.Lparen, leftParen)
		}
		pe.Rparen = p.matched(pe.Lparen, leftParen, rightParen)
		return pe
	case _LitWord:
		if p.val == "]]" {
			return nil
		}
		fallthrough
	default:
		if w := p.getWord(); w != nil {
			return w
		}
		// otherwise we'd return a typed nil above
		return nil
	}
}

func (p *Parser) declClause(s *Stmt) {
	ds := &DeclClause{Variant: p.lit(p.pos, p.val)}
	p.next()
	for !p.stopToken() && !p.peekRedir() {
		if p.hasValidIdent() {
			ds.Args = append(ds.Args, p.getAssign(false))
		} else if p.tok.isLit() && p.eqlOffs > 0 && !strings.Contains(p.val[:p.eqlOffs], "{") {
			p.curErr("invalid var name")
		} else if p.tok == _LitWord && ValidName(p.val) {
			ds.Args = append(ds.Args, &Assign{
				Naked: true,
				Name:  p.getLit(),
			})
		} else if w := p.getWord(); w != nil {
			ds.Args = append(ds.Args, &Assign{
				Naked: true,
				Value: w,
			})
		} else {
			p.followErr(p.pos, ds.Variant.Value, noQuote("names or assignments"))
		}
	}
	s.Cmd = ds
}

func isBashCompoundCommand(tok token, val string) bool {
	switch tok {
	case leftParen, dblLeftParen:
		return true
	case _LitWord:
		switch val {
		case "{", "if", "while", "until", "for", "case", "[[",
			"coproc", "let", "function", "declare", "local",
			"export", "readonly", "typeset", "nameref":
			return true
		}
	}
	return false
}

func (p *Parser) timeClause(s *Stmt) {
	tc := &TimeClause{Time: p.pos}
	p.next()
	if _, ok := p.gotRsrv("-p"); ok {
		tc.PosixFormat = true
	}
	// bash 5.3 also accepts `--` as an end-of-options marker right
	// after `time` / `time -p` *when* another statement follows.
	// `time --` alone keeps treating `--` as the body command.
	if p.lang.in(langBashExact) && p.tok == _LitWord && p.val == "--" {
		save := p.pos
		p.next()
		if p.stopToken() {
			// no body after `--`; restore so it parses as the body.
			p.tok = _LitWord
			p.val = "--"
			p.pos = save
		}
	}
	tc.Stmt = p.gotStmtPipe(&Stmt{Position: p.pos}, false)
	s.Cmd = tc
}

func (p *Parser) coprocClause(s *Stmt) {
	cc := &CoprocClause{Coproc: p.pos}
	if p.next(); isBashCompoundCommand(p.tok, p.val) {
		// has no name
		cc.Stmt = p.gotStmtPipe(&Stmt{Position: p.pos}, false)
		s.Cmd = cc
		return
	}
	cc.Name = p.getWord()
	cc.Stmt = p.gotStmtPipe(&Stmt{Position: p.pos}, false)
	if cc.Stmt == nil {
		if cc.Name == nil {
			p.posErr(cc.Coproc, "coproc clause requires a command")
			return
		}
		// name was in fact the stmt
		cc.Stmt = &Stmt{Position: cc.Name.Pos()}
		cc.Stmt.Cmd = p.call(cc.Name)
		cc.Name = nil
	} else if cc.Name != nil {
		if call, ok := cc.Stmt.Cmd.(*CallExpr); ok {
			// name was in fact the start of a call
			call.Args = append([]*Word{cc.Name}, call.Args...)
			cc.Name = nil
		}
	}
	s.Cmd = cc
}

func (p *Parser) letClause(s *Stmt) {
	lc := &LetClause{Let: p.pos}
	old := p.preNested(arithmExprLet)
	p.next()
	for !p.stopToken() && !p.peekRedir() {
		x := p.arithmExpr(true)
		if x == nil {
			break
		}
		lc.Exprs = append(lc.Exprs, x)
	}
	if len(lc.Exprs) == 0 && !p.stopToken() && !p.peekRedir() {
		p.followErrExp(lc.Let, "let")
	}
	p.postNested(old)
	s.Cmd = lc
}

// funcNameWordText reconstructs a source-form string from a parsed Word
// being used as a function name. It handles the common WordPart shapes
// that bash accepts in function names (literals, $param, ${param},
// single/double quotes, process substitutions); the resulting string
// is what bash would diagnose as the "not a valid identifier" name at
// runtime if it is not a valid identifier.
func funcNameWordText(w *Word) string {
	var sb strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *Lit:
			sb.WriteString(p.Value)
		case *ParamExp:
			sb.WriteByte('$')
			if !p.Short {
				sb.WriteByte('{')
			}
			if p.Length {
				sb.WriteByte('#')
			} else if p.Excl {
				sb.WriteByte('!')
			}
			if p.Param != nil {
				sb.WriteString(p.Param.Value)
			}
			if !p.Short {
				sb.WriteByte('}')
			}
		case *SglQuoted:
			if p.Dollar {
				sb.WriteByte('$')
			}
			sb.WriteByte('\'')
			sb.WriteString(p.Value)
			sb.WriteByte('\'')
		case *DblQuoted:
			if p.Dollar {
				sb.WriteByte('$')
			}
			sb.WriteByte('"')
			for _, inner := range p.Parts {
				if lit, ok := inner.(*Lit); ok {
					sb.WriteString(lit.Value)
				} else {
					sb.WriteString(funcNameWordText(&Word{Parts: []WordPart{inner}}))
				}
			}
			sb.WriteByte('"')
		case *CmdSubst:
			sb.WriteString("$(...)")
		case *ProcSubst:
			if p.Op == CmdIn {
				sb.WriteString("<(")
			} else {
				sb.WriteString(">(")
			}
			for _, stmt := range p.Stmts {
				if call, ok := stmt.Cmd.(*CallExpr); ok && len(call.Args) > 0 {
					sb.WriteString(funcNameWordText(call.Args[0]))
				}
			}
			sb.WriteByte(')')
		default:
			// Unknown part — emit a placeholder so the runtime sees a
			// non-empty (invalid) identifier.
			sb.WriteByte('?')
		}
	}
	return sb.String()
}

func (p *Parser) bashFuncDecl(s *Stmt) {
	fpos := p.pos
	p.next()
	names := make([]*Lit, 0, 1)
	// Bash accepts `function NAME` where NAME may contain non-name
	// characters like `$`, `[`, quotes, etc. (e.g. `function sys$read`
	// or `function 'a b'`), and defers the "not a valid identifier"
	// error to runtime. Read each name as a Word and stringify it so
	// the interpreter can validate the literal text later. Only Bash
	// has this deferral; other languages still reject at parse time.
	if p.lang.in(langBashExact) {
		for p.tok != _Newl && p.tok != semicolon && p.tok != _EOF &&
			p.tok != leftBrace && !(p.tok == _LitWord && p.val == "{") &&
			p.tok != leftParen {
			w := p.getWord()
			if w == nil {
				break
			}
			text := funcNameWordText(w)
			if text == "" {
				break
			}
			names = append(names, &Lit{ValuePos: w.Pos(), ValueEnd: w.End(), Value: text})
		}
	} else {
		for p.tok == _LitWord && p.val != "{" {
			names = append(names, p.lit(p.pos, p.val))
			p.next()
		}
	}
	hasParens := p.got(leftParen)
	switch len(names) {
	case 0:
		if hasParens || (p.tok == _LitWord && p.val == "{") {
			p.checkLang(fpos, LangZsh, "anonymous functions")
		} else if !p.lang.in(LangZsh) {
			p.followErr(fpos, "function", noQuote("a name"))
		}
		names = nil // avoid non-nil zero-length slices
	case 1:
		// allowed in all variants
	default:
		p.checkLang(fpos, LangZsh, "multi-name functions")
	}
	if hasParens {
		p.follow(fpos, "function foo(", rightParen)
	}
	p.funcDecl(s, fpos, true, hasParens, names...)
}

func (p *Parser) testDecl(s *Stmt) {
	td := &TestDecl{Position: p.pos}
	p.next()
	if td.Description = p.getWord(); td.Description == nil {
		p.followErr(td.Position, "@test", noQuote("a description word"))
	}
	if td.Body = p.getStmt(false, false, true); td.Body == nil {
		p.followErr(td.Position, `@test "desc"`, noQuote("a statement"))
	}
	s.Cmd = td
}

func (p *Parser) unexpectedInCallExpr(ce *CallExpr) {
	// Note that we'll only keep the first error that happens.
	if len(ce.Args) > 0 {
		if cmd := ce.Args[0].Lit(); isBashCompoundCommand(_LitWord, cmd) {
			p.checkLang(p.pos, langBashLike, "the %#q builtin", cmd)
		}
	}
	p.curErr("a command can only contain words and redirects; encountered %#q", p.tok)
}

func (p *Parser) callExpr(s *Stmt, w *Word, assign bool) {
	ce := p.call(w)
	var bashppCompositeTxn *parserTransaction
	bashppCompositeArgs := 0
	if w == nil {
		ce.Args = ce.Args[:0]
	}
	if assign {
		as := p.getAssign(true)
		if as != nil && as.Naked {
			ce.Args = append(ce.Args, p.assignAsWord(as))
		} else if as != nil {
			ce.Assigns = append(ce.Assigns, as)
		}
	}
	bashppScalarTried := false
	bashppReturnTried := false
loop:
	for {
		if p.lang.in(LangBashPP) && bashppCompositeTxn == nil && len(s.Redirs) == 0 {
			if p.bashppFuncDepth > 0 && !bashppReturnTried && len(ce.Assigns) == 0 && len(ce.Args) > 0 && bashppLitValue(ce.Args[0]) == "return" {
				bashppReturnTried = true
				if ret := p.bashppScalarReturn(ce); ret != nil {
					s.Cmd = ret
					return
				}
			}
			if (p.tok == leftParen && (bashppAddressHead(ce) || bashppScalarHead(ce))) || (p.tok == bckQuote && bashppAddressHead(ce)) {
				if decl := p.bashppLeadingScalarOperand(ce); decl != nil {
					s.Cmd = decl
					return
				}
			}
		}
		// A Go scalar expression written with shell metacharacters is claimed
		// here, ahead of the terminator and redirect arms below, because those
		// arms have already destroyed it by the time a completed command could
		// be inspected; see sh/syntax/bashpp_scalar.go. The attempt is
		// transactional and made at most once, so a shape it declines reaches
		// the ordinary arms with the parser exactly as they would have found it.
		if p.lang.in(LangBashPP) && !bashppScalarTried &&
			bashppCompositeTxn == nil && len(s.Redirs) == 0 &&
			bashppScalarOpTok(p.tok) != "" && (bashppScalarHead(ce) || p.tok == and && bashppAddressHead(ce)) {
			bashppScalarTried = true
			if decl := p.bashppScalarTail(ce); decl != nil {
				s.Cmd = decl
				return
			}
		}
		if p.lang.in(LangBashPP) && bashppCompositeTxn == nil && len(s.Redirs) == 0 &&
			bashppCompoundTokenHead(ce, p.tok) {
			if update := p.bashppCompoundTail(ce); update != nil {
				s.Cmd = update
				return
			}
		}
		switch p.tok {
		case _EOF, _Newl, semicolon, and, or, andAnd, orOr, orAnd, andPipe, andBang,
			dblSemicolon, semiAnd, dblSemiAnd, semiOr:
			if bashppCompositeTxn != nil {
				if bashppCompositeCommandDepth(ce) == 0 {
					if bashppCompositeCommandComplete(ce) {
						bashppCompositeTxn.commit(p)
					} else {
						bashppCompositeTxn.rollback(p)
						ce.Args = ce.Args[:bashppCompositeArgs]
					}
					break loop
				}
				if p.tok == _EOF {
					bashppCompositeTxn.rollback(p)
					ce.Args = ce.Args[:bashppCompositeArgs]
					break loop
				}
			}
			// A supported Bash# composite literal or struct declaration owns
			// separators until its balanced closing brace. This is intentionally
			// decided from the complete prefix already parsed as shell words; an
			// unclosed or unsupported Class-E prefix still terminates here and
			// retains ordinary shell behavior.
			if p.lang.in(LangBashPP) && (p.tok == _Newl || p.tok == semicolon) &&
				bashppCompositeCommandDepth(ce) > 0 {
				if bashppCompositeTxn == nil {
					bashppCompositeTxn = p.beginBashPPTxn()
					bashppCompositeArgs = len(ce.Args)
				}
				if bashppStructTypeCommand(ce) {
					ce.Args = append(ce.Args, p.wordOne(&Lit{ValuePos: p.pos, ValueEnd: posAddCol(p.pos, 1), Value: ";"}))
				}
				p.next()
				continue
			}
			break loop
		case _LitWord:
			if len(ce.Args) == 0 && p.hasValidIdent() {
				as := p.getAssign(true)
				if as != nil && as.Naked {
					ce.Args = append(ce.Args, p.assignAsWord(as))
				} else if as != nil {
					ce.Assigns = append(ce.Assigns, as)
				}
				break
			}
			if evalArrayAssignmentArg := len(ce.Args) > 0 && ce.Args[0].Lit() == "eval" &&
				p.eqlOffs > 0 && ValidName(p.val[:p.eqlOffs]); evalArrayAssignmentArg {
				w := p.wordOne(p.lit(p.pos, p.val))
				p.next()
				if p.tok == leftParen && !p.spaced {
					w.Parts = append(w.Parts, p.evalArrayArgParts()...)
				}
				ce.Args = append(ce.Args, w)
				break
			}
			// Avoid failing later with the confusing "} can only be used to close a block".
			if p.val == "{" && w != nil && w.Lit() == "function" {
				p.checkLang(p.pos, langBashLike, `the "function" builtin`)
			}
			// Zsh does not require a semicolon to close a block. Go's
			// semicolon-insertion rule gives a committed Bash++ func body the
			// same property. Keep this keyed to bashppFuncDepth: ordinary Bash
			// and POSIX blocks, including speculative regions which later roll
			// back, must retain their separator rules byte for byte.
			if p.val == "}" && (p.lang.in(LangZsh) ||
				p.lang.in(LangBashPP) && p.bashppFuncDepth > 0) {
				break loop
			}
			w := p.wordOne(p.lit(p.pos, p.val))
			p.next()
			if p.lang.in(LangZsh) && !p.spaced {
				w.Parts = append(w.Parts, p.wordParts(nil)...)
			}
			ce.Args = append(ce.Args, w)
		case _Lit:
			if len(ce.Args) == 0 && p.hasValidIdent() {
				as := p.getAssign(true)
				if as != nil && as.Naked {
					ce.Args = append(ce.Args, p.assignAsWord(as))
				} else if as != nil {
					ce.Assigns = append(ce.Assigns, as)
				}
				break
			}
			ce.Args = append(ce.Args, p.wordAnyNumber())
		case bckQuote:
			if p.backquoteEnd() {
				break loop
			}
			fallthrough
		case dollBrace, dollDblParen, dollParen, dollar, cmdIn, assgnParen, cmdOut,
			sglQuote, dollSglQuote, dblQuote, dollDblQuote, dollBrack,
			globQuest, globStar, globPlus, globAt, globExcl:
			ce.Args = append(ce.Args, p.wordAnyNumber())
		case dblLeftParen:
			p.curErr("%#q can only be used to open an arithmetic cmd", p.tok)
		case leftParen:
			if p.lang.in(LangBashPP) {
				// `go` is intentionally claimed only with the call spelling.  The
				// ordinary `go build ./...` command never reaches this branch.
				if len(ce.Args) == 2 && bashppLitValue(ce.Args[0]) == "go" && bashppLitValue(ce.Args[1]) == "func" {
					kw := bashppBareLit(ce.Args[0])
					if kw != nil {
						call := p.bashppLitCall(p.bashppFuncLit(bashppBareLit(ce.Args[1])))
						if call != nil {
							s.Cmd = &BashPPGo{Kw: kw, Call: call}
							return
						}
					}
				}
				nested := len(ce.Args) > 1
				if nested {
					for _, arg := range ce.Args[:len(ce.Args)-1] {
						if arg.Lit() == ":=" || arg.Lit() == "defer" || arg.Lit() == "return" {
							nested = false
						}
					}
				}
				// In a committed Bash++ function, `p = new(T)` is an
				// unambiguous pointer assignment, not a shell command with a
				// nested call as its final argument. Let bashppParenForm consume
				// the complete typed assignment transactionally below.
				if len(ce.Args) == 3 && ce.Args[1].Lit() == "=" && ce.Args[2].Lit() == "new" {
					nested = false
				}
				// Likewise, `x, y = f()` owns the entire result-bearing
				// assignment; it is not a shell command followed by a nested call.
				if len(ce.Args) >= 3 && ce.Args[len(ce.Args)-2].Lit() == "=" {
					nested = false
				}
				if nested {
					last := ce.Args[len(ce.Args)-1]
					if call, ok := p.bashppParenForm(&CallExpr{Args: []*Word{last}}).(*BashPPCall); ok {
						if len(ce.Args) == 2 && bashppLitValue(ce.Args[0]) == "go" {
							if kw := bashppBareLit(ce.Args[0]); kw != nil {
								s.Cmd = &BashPPGo{Kw: kw, Call: call}
								return
							}
						}
						s.Cmd = &BashPPCommandCall{Before: ce.Args[:len(ce.Args)-1], Call: call}
						return
					}
				}
				if cmd := p.bashppFuncForm(ce); cmd != nil {
					s.Cmd = cmd
					return
				}
				if cmd := p.bashppDeferForm(ce); cmd != nil {
					s.Cmd = cmd
					return
				}
				if cmd := p.bashppImportGroup(ce); cmd != nil {
					s.Cmd = cmd
					return
				}
				if cmd := p.bashppConstGroup(ce); cmd != nil {
					s.Cmd = cmd
					return
				}
				if cmd := p.bashppInterfaceForm(ce); cmd != nil {
					s.Cmd = cmd
					return
				}
				if cmd := p.bashppParenForm(ce); cmd != nil {
					s.Cmd = cmd
					return
				}
			}
			if p.lang.in(LangZsh) && p.r != ')' {
				ce.Args = append(ce.Args, p.wordAnyNumber())
				break
			}
			p.unexpectedInCallExpr(ce)
		case rightParen:
			if p.quote == subCmd {
				break loop
			}
			p.unexpectedInCallExpr(ce)
		default:
			if p.peekRedir() {
				p.doRedirect(s)
				continue
			}
			p.unexpectedInCallExpr(ce)
		}
	}
	if len(ce.Args) == 0 {
		ce.Args = nil
	}
	// The Bash++ command-position dispatch. It runs here, after the command is
	// complete and its terminator reached, precisely so that nothing has been
	// consumed on the strength of a prefix; see sh/syntax/bashpp_decl.go. An
	// ordinary command is handed back untouched. Reserved declaration words
	// and binding start sites diagnose invalid bodies instead of falling back.
	if p.lang.in(LangBashPP) && p.err == nil {
		if len(ce.Assigns) == 0 && len(s.Redirs) == 0 && len(ce.Args) == 2 && bashppLitValue(ce.Args[0]) == "goto" {
			if label := bashppBareLit(ce.Args[1]); label != nil && bashppIsIdent(label.Value) && !isGoReservedWord(label.Value) {
				s.Cmd = &BashPPGoto{Kw: bashppBareLit(ce.Args[0]), Label: label}
				return
			}
		}
		if branch := p.bashppBranch(ce, s.Redirs); branch != nil {
			s.Cmd = branch
			return
		}
		if cmd := p.bashppImportGroup(ce); cmd != nil {
			s.Cmd = cmd
			return
		}
		if imp := bashppImport(ce, s.Redirs); imp != nil {
			s.Cmd = imp
			return
		}
		if block := p.bashppEmbed(ce, s.Redirs); block != nil {
			s.Cmd = block
			return
		}
		if ret := p.bashppReturn(ce, s.Redirs); ret != nil {
			s.Cmd = ret
			return
		}
		// Channel send and receive, which bash spells as a `<` redirect; see
		// sh/syntax/bashpp_chan.go for why they are reclassified from the
		// finished tree rather than recognized in the lexer. The redirect is
		// consumed by the typed node, so it is dropped from the statement.
		if cmd := bashppChanForm(ce, s.Redirs); cmd != nil {
			s.Cmd = cmd
			s.Redirs = nil
			return
		}
		if decl := bashppShortDecl(ce, s.Redirs, true); decl != nil {
			knownFuncValue := len(decl.Rhs) == 1 && p.bashppCallable(bashppLitValue(decl.Rhs[0]))
			if ident, ok := decl.Expr.(*BashPPIdent); ok {
				knownFuncValue = p.bashppCallable(ident.Name.Value)
			}
			if len(decl.MethodValue) > 0 || knownFuncValue {
				for _, name := range decl.Lhs {
					p.bashppRegisterFunc(name.Value)
				}
			}
			s.Cmd = decl
			return
		}
		if update := p.bashppUpdate(ce, s.Redirs, p.bashppFuncDepth > 0 || len(ce.Assigns) == 0); update != nil {
			s.Cmd = update
			return
		}
		if assign := bashppAssign(ce, s.Redirs, true); assign != nil {
			s.Cmd = assign
			return
		}
		if decl := bashppTypedVarDecl(ce, s.Redirs); decl != nil {
			s.Cmd = decl
			return
		}
		if decl := bashppTypeDecl(ce, s.Redirs); decl != nil {
			s.Cmd = decl
			return
		}
		if decl := bashppUntypedDecl(ce, s.Redirs); decl != nil {
			s.Cmd = decl
			return
		}
		p.bashppReservedFallback(ce)
	}
	s.Cmd = ce
}

func (p *Parser) evalArrayArgParts() []WordPart {
	var parts []WordPart
	parts = append(parts, p.lit(p.pos, "("))
	p.next()
	for p.tok != _EOF && p.tok != rightParen {
		if p.spaced {
			parts = append(parts, &Lit{
				ValuePos: posAddCol(p.pos, -1),
				ValueEnd: p.pos,
				Value:    " ",
			})
		}
		before := len(parts)
		parts = p.wordParts(parts)
		if len(parts) == before {
			break
		}
	}
	if p.tok == rightParen {
		parts = append(parts, p.lit(p.pos, ")"))
		p.next()
	} else {
		p.matchingErr(Pos{}, leftParen, rightParen)
	}
	return parts
}

func (p *Parser) funcDecl(s *Stmt, pos Pos, long, withParens bool, names ...*Lit) {
	if p.lang.in(LangBashPP) {
		for _, name := range names {
			plainName := strings.NewReplacer("\"", "", "'", "").Replace(name.Value)
			if plainName != "func" && bashppReservedWord(plainName) {
				p.posErr(name.Pos(), "%s is reserved and cannot name a shell function", name.Value)
				return
			}
		}
	}
	fd := &FuncDecl{
		Position: pos,
		RsrvWord: long,
		Parens:   withParens,
	}
	if len(names) == 1 {
		fd.Name = names[0]
	} else {
		fd.Names = names
	}
	p.got(_Newl)
	// Bash (and bats, which is bash-based) require a function body to be a
	// compound command: a group `{ …; }`, subshell `( … )`, if/for/while/
	// until/case/select, arithmetic command `(( … ))`, or test clause
	// `[[ … ]]`. A simple command (or a pipeline, `declare`/`let`/`time`/
	// `coproc`, a negation, etc.) is a syntax error, matching bash — even
	// in bash's POSIX mode. Other variants (POSIX sh/dash, mksh) accept a
	// simple-command body as an extension, so leave them unchanged.
	// An empty body (a terminator or EOF here) is reported by the
	// getStmt call below as "must be followed by a statement"; only flag
	// a present-but-non-compound body.
	if p.lang.in(langBashLike) && !p.stopToken() && !p.startsCompoundCommand() {
		p.curErr("a function body must be a compound command")
	}
	// A `return` inside a nested shell function is that function's own shell
	// return, not the enclosing Bash++ func's Go-form return, so drop the Go
	// depth across the body and restore it after.
	savedFuncDepth := p.bashppFuncDepth
	savedControls := p.bashppControls
	p.bashppFuncDepth = 0
	p.bashppControls = nil
	if fd.Body = p.getStmt(false, false, true); fd.Body == nil {
		p.followErr(fd.Pos(), "foo()", noQuote("a statement"))
	}
	p.bashppFuncDepth = savedFuncDepth
	p.bashppControls = savedControls
	s.Cmd = fd
}

// startsCompoundCommand reports whether the current token begins a compound
// command: a group, subshell, if/for/while/until/case/select, arithmetic
// command, or test clause. Bash requires a function body to be one of these.
func (p *Parser) startsCompoundCommand() bool {
	switch p.tok {
	case leftParen, dblLeftParen:
		return true
	case _LitWord:
		switch p.val {
		case "{", "if", "for", "while", "until", "case", "select", "[[":
			return true
		}
	}
	return false
}
