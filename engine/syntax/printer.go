// Copyright (c) 2016, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package syntax

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/qiangli/bash/fileutil"
)

// PrinterOption is a function which can be passed to NewPrinter
// to alter its behavior. To apply option to existing Printer
// call it directly, for example KeepPadding(true)(printer).
type PrinterOption func(*Printer)

// Indent sets the number of spaces used for indentation. If set to 0,
// tabs will be used instead.
func Indent(spaces uint) PrinterOption {
	return func(p *Printer) { p.indentSpaces = spaces }
}

// BinaryNextLine will make binary operators appear on the next line
// when a binary command, such as a pipe, spans multiple lines. A
// backslash will be used.
func BinaryNextLine(enabled bool) PrinterOption {
	return func(p *Printer) { p.binNextLine = enabled }
}

// SwitchCaseIndent will make switch cases be indented. As such, switch
// case bodies will be two levels deeper than the switch itself.
func SwitchCaseIndent(enabled bool) PrinterOption {
	return func(p *Printer) { p.swtCaseIndent = enabled }
}

// TODO(v4): consider turning this into a "space all operators" option, to also
// allow foo=( bar baz ), (( x + y )), and so on.

// SpaceRedirects will put a space after most redirection operators. The
// exceptions are '>&', '<&', '>(', and '<('.
func SpaceRedirects(enabled bool) PrinterOption {
	return func(p *Printer) { p.spaceRedirects = enabled }
}

// KeepPadding will keep most nodes and tokens in the same column that
// they were in the original source. This allows the user to decide how
// to align and pad their code with spaces.
//
// Note that this feature is best-effort and will only keep the
// alignment stable, so it may need some human help the first time it is
// run.
//
// Deprecated: this formatting option is flawed and buggy, and often does
// not result in what the user wants when the code gets complex enough.
// The next major version, v4, will remove this feature entirely.
// See: https://github.com/mvdan/sh/issues/658
func KeepPadding(enabled bool) PrinterOption {
	return func(p *Printer) {
		if enabled && !p.keepPadding {
			// Enable the flag, and set up the writer wrapper.
			p.keepPadding = true
			p.cols.Writer = p.w.(*bufio.Writer)
			p.w = &p.cols

		} else if !enabled && p.keepPadding {
			// Ensure we reset the state to that of NewPrinter.
			p.keepPadding = false
			p.w = p.cols.Writer
			p.cols = colCounter{}
		}
	}
}

// Minify will print programs in a way to save the most bytes possible.
// For example, indentation and comments are skipped, and extra
// whitespace is avoided when possible.
func Minify(enabled bool) PrinterOption {
	return func(p *Printer) { p.minify = enabled }
}

// SingleLine will attempt to print programs in one line. For example, lists of
// commands or nested blocks do not use newlines in this mode. Note that some
// newlines must still appear, such as those following comments or around
// here-documents.
//
// Print's trailing newline when given a [*File] is not affected by this option.
func SingleLine(enabled bool) PrinterOption {
	return func(p *Printer) { p.singleLine = enabled }
}

// JoinEscapedNewlines prints a command whose words the source continued
// with backslash-newlines on one line, as bash's `declare -f` does: the
// escaped newline is layout, not syntax, and a printer that keeps it breaks
// a caller that appends `;` to every line.
func JoinEscapedNewlines(enabled bool) PrinterOption {
	return func(p *Printer) { p.joinBslashNewl = enabled }
}

// FunctionNextLine will place a function's opening braces on the next line.
func FunctionNextLine(enabled bool) PrinterOption {
	return func(p *Printer) { p.funcNextLine = enabled }
}

// BashCompatArith prints arithmetic expressions preserving the source
// spacing around binary operators (so `i<3` stays compact and `i < 3`
// keeps its spaces). This matches bash 5.3's `declare -f` rendering,
// which echoes operator gaps verbatim from the parsed source.
func BashCompatArith(enabled bool) PrinterOption {
	return func(p *Printer) { p.bashCompatArith = enabled }
}

// hasGap reports whether there is at least one column between two
// adjacent positions in the source — used to decide whether to emit a
// space when re-rendering arithmetic operators in bash-compat mode.
func hasGap(a, b Pos) bool {
	if !a.IsValid() || !b.IsValid() {
		return false
	}
	if a.Line() != b.Line() {
		return true
	}
	return b.Col() > a.Col()
}

// NewPrinter allocates a new Printer and applies any number of options.
func NewPrinter(opts ...PrinterOption) *Printer {
	p := &Printer{
		w:         bufio.NewWriter(nil),
		tabWriter: new(tabwriter.Writer),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Print "pretty-prints" the given syntax tree node to the given writer. Writes
// to w are buffered.
//
// The node types supported at the moment are [*File], [*Stmt], [*Word], [*Assign], any
// [Command] node, and any WordPart node. A trailing newline will only be printed
// when a [*File] is used.
func (p *Printer) Print(w io.Writer, node Node) error {
	p.reset()

	if p.minify && p.singleLine {
		return fmt.Errorf("Minify and SingleLine together are not supported yet; please file an issue describing your use case: https://github.com/mvdan/sh/issues")
	}

	// TODO: consider adding a raw mode to skip the tab writer, much like in
	// go/printer.
	twmode := tabwriter.DiscardEmptyColumns | tabwriter.StripEscape
	tabwidth := 8
	if p.indentSpaces == 0 {
		// indenting with tabs
		twmode |= tabwriter.TabIndent
	} else {
		// indenting with spaces
		tabwidth = int(p.indentSpaces)
	}
	p.tabWriter.Init(w, 0, tabwidth, 1, ' ', twmode)
	w = p.tabWriter

	p.w.Reset(w)
	switch node := node.(type) {
	case *File:
		p.stmtList(node.Stmts, node.Last)
		p.newline(Pos{})
	case *Stmt:
		p.stmtList([]*Stmt{node}, nil)
	case Command:
		p.command(node, nil)
	case *Word:
		p.line = node.Pos().Line()
		p.word(node)
	case WordPart:
		p.line = node.Pos().Line()
		p.wordPart(node, nil)
	case *Assign:
		p.line = node.Pos().Line()
		p.assigns([]*Assign{node})
	default:
		return fmt.Errorf("unsupported node type: %T", node)
	}
	p.flushHeredocs()
	p.flushComments()

	// flush the writers
	if err := p.w.Flush(); err != nil {
		return err
	}
	if tw, _ := w.(*tabwriter.Writer); tw != nil {
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

type bufWriter interface {
	Write([]byte) (int, error)
	WriteString(string) (int, error)
	WriteByte(byte) error
	Reset(io.Writer)
	Flush() error
}

type colCounter struct {
	*bufio.Writer
	column    int
	lineStart bool
}

func (c *colCounter) addByte(b byte) {
	switch b {
	case '\n':
		c.column = 0
		c.lineStart = true
	case '\t', ' ', tabwriter.Escape:
	default:
		c.lineStart = false
	}
	c.column++
}

func (c *colCounter) WriteByte(b byte) error {
	c.addByte(b)
	return c.Writer.WriteByte(b)
}

func (c *colCounter) WriteString(s string) (int, error) {
	for _, b := range []byte(s) {
		c.addByte(b)
	}
	return c.Writer.WriteString(s)
}

func (c *colCounter) Reset(w io.Writer) {
	c.column = 1
	c.lineStart = true
	c.Writer.Reset(w)
}

// Printer holds the internal state of the printing mechanism of a
// program.
type Printer struct {
	w         bufWriter
	tabWriter *tabwriter.Writer
	cols      colCounter // used for [KeepPadding]

	indentSpaces    uint
	binNextLine     bool
	swtCaseIndent   bool
	spaceRedirects  bool
	keepPadding     bool
	minify          bool
	singleLine      bool
	joinBslashNewl  bool
	inArithm        int // depth of $((...)) being printed
	funcNextLine    bool
	bashCompatArith bool

	wantSpace wantSpaceState // whether space is required or has been written

	wantNewline bool // newline is wanted for pretty-printing; ignored by singleLine; ignored by singleLine
	mustNewline bool // newline is required to keep shell syntax valid
	wroteSemi   bool // wrote ';' for the current statement

	// pendingComments are any comments in the current line or statement
	// that we have yet to print. This is useful because that way, we can
	// ensure that all comments are written immediately before a newline.
	// Otherwise, in some edge cases we might wrongly place words after a
	// comment in the same line, breaking programs.
	pendingComments []Comment

	// firstLine means we are still writing the first line
	firstLine bool
	// line is the current line number
	line uint

	// lastLevel is the last level of indentation that was used.
	lastLevel uint
	// level is the current level of indentation.
	level uint
	// levelIncs records which indentation level increments actually
	// took place, to revert them once their section ends.
	levelIncs []bool

	nestedBinary bool

	// pendingHdocs is the list of pending heredocs to write.
	pendingHdocs []*Redirect

	// used when printing <<- heredocs with tab indentation
	tabsPrinter *Printer
}

func (p *Printer) reset() {
	p.wantSpace = spaceWritten
	p.wantNewline, p.mustNewline = false, false
	p.pendingComments = p.pendingComments[:0]

	// minification uses its own newline logic
	p.firstLine = !p.minify
	p.line = 0

	p.lastLevel, p.level = 0, 0
	p.levelIncs = p.levelIncs[:0]
	p.nestedBinary = false
	p.pendingHdocs = p.pendingHdocs[:0]
}

func (p *Printer) spaces(n uint) {
	for range n {
		p.w.WriteByte(' ')
	}
}

func (p *Printer) space() {
	p.w.WriteByte(' ')
	p.wantSpace = spaceWritten
}

func (p *Printer) spacePad(pos Pos) {
	if p.cols.lineStart && p.indentSpaces == 0 {
		// Never add padding at the start of a line unless we are indenting
		// with spaces, since this may result in mixing of spaces and tabs.
		return
	}
	if p.wantSpace == spaceRequired {
		p.w.WriteByte(' ')
		p.wantSpace = spaceWritten
	}
	for p.cols.column > 0 && p.cols.column < int(pos.Col()) {
		p.w.WriteByte(' ')
	}
}

// wantsNewline reports whether we want to print at least one newline before
// printing a node at a given position. A zero position can be given to simply
// tell if we want a newline following what's just been printed.
func (p *Printer) wantsNewline(pos Pos, escapingNewline bool) bool {
	if p.mustNewline {
		// We must have a newline here.
		return true
	}
	if p.singleLine && len(p.pendingComments) == 0 {
		// The newline is optional, and singleLine skips it.
		// Don't skip if there are any pending comments,
		// as that might move them further down to the wrong place.
		return false
	}
	if escapingNewline && p.minify {
		return false
	}
	// The newline is optional, and we want it via either wantNewline or via
	// the position's line.
	return p.wantNewline || pos.Line() > p.line
}

func (p *Printer) bslashNewl() {
	if p.joinBslashNewl && p.inArithm == 0 {
		// The source line is consumed; the following word's spacePad
		// writes the separating space. Line breaks inside $((...)) are
		// kept: bash's declare -f reprints them (comsub-posix2.sub).
		p.line++
		return
	}
	if p.wantSpace == spaceRequired {
		p.space()
	}
	p.w.WriteString("\\\n")
	p.line++
	p.indent()
}

func (p *Printer) spacedString(s string, pos Pos) {
	p.spacePad(pos)
	p.w.WriteString(s)
	p.wantSpace = spaceRequired
}

func (p *Printer) spacedToken(s string, pos Pos) {
	if p.minify {
		p.w.WriteString(s)
		p.wantSpace = spaceNotRequired
		return
	}
	p.spacePad(pos)
	p.w.WriteString(s)
	p.wantSpace = spaceRequired
}

func (p *Printer) semiOrNewl(s string, pos Pos) {
	if p.wantsNewline(Pos{}, false) || len(p.pendingHdocs) > 0 {
		p.newline(pos)
		p.indent()
	} else {
		if !p.wroteSemi {
			p.w.WriteByte(';')
		}
		if !p.minify {
			p.space()
		}
		p.advanceLine(pos.Line())
	}
	p.w.WriteString(s)
	p.wantSpace = spaceRequired
}

// bashppSignature prints `(params) results`, shared by the declaration and the
// literal so a signature reads the same wherever it was written.
func (p *Printer) bashppSignature(params, results []*BashPPField, resLparen Pos) {
	p.writeLit("(")
	p.bashppFields(params)
	p.writeLit(")")
	if len(results) == 0 {
		return
	}
	p.writeLit(" ")
	if resLparen.IsValid() {
		p.writeLit("(")
		p.bashppFields(results)
		p.writeLit(")")
		return
	}
	p.bashppFields(results)
}

// bashppMakeChan prints `make(chan T)` or `make(chan T, n)`. It is not part of
// the command switch because a channel constructor is a VALUE: it appears only
// as the right-hand side of a short declaration, never as a statement.
func (p *Printer) bashppMakeChan(mk *BashPPMakeChan) {
	p.spacedString(mk.Make.Value, mk.Make.Pos())
	p.writeLit("(chan ")
	p.writeLit(mk.ChanType.Elem.Value)
	if mk.CapacityExpr != nil {
		p.writeLit(", ")
		p.bashppExpr(mk.CapacityExpr)
	} else if mk.Capacity != nil {
		p.writeLit(", ")
		p.word(mk.Capacity)
	}
	p.writeLit(")")
}

// bashppFuncLit prints a function literal: `func(a int) int { … }`. The body
// goes through the ordinary block printer, so a closure indents exactly as the
// braces around it would anywhere else.
func (p *Printer) bashppFuncLit(lit *BashPPFuncLit) {
	p.spacedString(lit.Kw.Value, lit.Kw.Pos())
	p.bashppSignature(lit.Params, lit.Results, lit.ResLparen)
	p.wantSpace = spaceRequired
	if lit.Body != nil {
		p.command(lit.Body, nil)
	}
}

// bashppFields prints a Go-form parameter or result list, e.g. `a, b int, c
// string`. It writes exact bytes rather than driving the pending-space model so
// the signature reads the same regardless of the surrounding positions.
func (p *Printer) bashppFields(fields []*BashPPField) {
	for i, f := range fields {
		if i > 0 {
			p.writeLit(", ")
		}
		for j, n := range f.Names {
			if j > 0 {
				p.writeLit(", ")
			}
			p.writeLit(n.Value)
		}
		if f.Variadic() {
			if len(f.Names) > 0 {
				p.writeLit(" ")
			}
			p.writeLit("...")
		}
		if f.FieldTypeExpr != nil {
			if len(f.Names) > 0 && !f.Variadic() {
				p.writeLit(" ")
			}
			p.bashppType(f.FieldTypeExpr)
		} else if f.FieldType != nil {
			if len(f.Names) > 0 && !f.Variadic() {
				p.writeLit(" ")
			}
			p.writeLit(f.FieldType.Value)
		}
		if f.Tag != nil {
			p.writeLit(" " + f.Tag.Value)
		}
		if f.Default != nil {
			p.writeLit(" = ")
			p.word(f.Default)
		}
	}
}

func (p *Printer) writeLit(s string) {
	// If p.tabWriter is nil, this is the nested printer being used to print
	// <<- heredoc bodies, so the parent printer will add the escape bytes
	// later.
	if p.tabWriter != nil && strings.Contains(s, "\t") {
		p.w.WriteByte(tabwriter.Escape)
		defer p.w.WriteByte(tabwriter.Escape)
	}
	p.w.WriteString(s)
}

func (p *Printer) incLevel() {
	inc := false
	if p.level <= p.lastLevel || len(p.levelIncs) == 0 {
		p.level++
		inc = true
	} else if last := &p.levelIncs[len(p.levelIncs)-1]; *last {
		*last = false
		inc = true
	}
	p.levelIncs = append(p.levelIncs, inc)
}

func (p *Printer) decLevel() {
	if p.levelIncs[len(p.levelIncs)-1] {
		p.level--
	}
	p.levelIncs = p.levelIncs[:len(p.levelIncs)-1]
}

func (p *Printer) indent() {
	if p.minify {
		return
	}
	p.lastLevel = p.level
	switch {
	case p.level == 0:
	case p.indentSpaces == 0:
		p.w.WriteByte(tabwriter.Escape)
		for i := uint(0); i < p.level; i++ {
			p.w.WriteByte('\t')
		}
		p.w.WriteByte(tabwriter.Escape)
	default:
		p.spaces(p.indentSpaces * p.level)
	}
}

// TODO(mvdan): add an indent call at the end of newline?

// newline prints one newline and advances p.line to pos.Line().
func (p *Printer) newline(pos Pos) {
	p.flushHeredocs()
	p.flushComments()
	p.w.WriteByte('\n')
	p.wantSpace = spaceWritten
	p.wantNewline, p.mustNewline = false, false
	p.advanceLine(pos.Line())
}

func (p *Printer) advanceLine(line uint) {
	if p.line < line {
		p.line = line
	}
}

func (p *Printer) flushHeredocs() {
	if len(p.pendingHdocs) == 0 {
		return
	}
	hdocs := p.pendingHdocs
	p.pendingHdocs = p.pendingHdocs[:0]
	coms := p.pendingComments
	p.pendingComments = nil
	if len(coms) > 0 {
		c := coms[0]
		if c.Pos().Line() == p.line {
			p.pendingComments = append(p.pendingComments, c)
			p.flushComments()
			coms = coms[1:]
		}
	}

	// Reuse the last indentation level, as
	// indentation levels are usually changed before
	// newlines are printed along with their
	// subsequent indentation characters.
	newLevel := p.level
	p.level = p.lastLevel

	for _, r := range hdocs {
		p.line++
		p.w.WriteByte('\n')
		p.wantSpace = spaceWritten
		p.wantNewline, p.wantNewline = false, false
		if r.Op == DashHdoc && p.indentSpaces == 0 && !p.minify {
			if r.Hdoc != nil {
				extra := extraIndenter{
					bufWriter:   p.w,
					baseIndent:  int(p.level + 1),
					firstIndent: -1,
				}
				p.tabsPrinter = &Printer{
					w: &extra,

					// The options need to persist.
					indentSpaces:   p.indentSpaces,
					binNextLine:    p.binNextLine,
					swtCaseIndent:  p.swtCaseIndent,
					spaceRedirects: p.spaceRedirects,
					keepPadding:    p.keepPadding,
					minify:         p.minify,
					funcNextLine:   p.funcNextLine,

					line: r.Hdoc.Pos().Line(),
				}
				p.tabsPrinter.wordParts(r.Hdoc.Parts, true)
			}
			p.indent()
		} else if r.Hdoc != nil {
			p.wordParts(r.Hdoc.Parts, true)
		}
		p.unquotedWord(r.Word)
		if r.Hdoc != nil {
			// Overwrite p.line, since printing r.Word again can set
			// p.line to the beginning of the heredoc again.
			p.advanceLine(r.Hdoc.End().Line())
		}
		p.wantSpace = spaceNotRequired
	}
	p.level = newLevel
	p.pendingComments = coms
	p.mustNewline = true
}

// newline prints between zero and two newlines.
// If any newlines are printed, it advances p.line to pos.Line().
func (p *Printer) newlines(pos Pos) {
	if p.firstLine && len(p.pendingComments) == 0 {
		p.firstLine = false
		return // no empty lines at the top
	}
	if !p.wantsNewline(pos, false) {
		return
	}
	p.flushHeredocs()
	p.flushComments()
	p.w.WriteByte('\n')
	p.wantSpace = spaceWritten
	p.wantNewline, p.mustNewline = false, false

	l := pos.Line()
	if l > p.line+1 && !p.minify {
		p.w.WriteByte('\n') // preserve single empty lines
	}
	p.advanceLine(l)
	p.indent()
}

func (p *Printer) rightParen(pos Pos) {
	if len(p.pendingHdocs) > 0 || !p.minify {
		p.newlines(pos)
	}
	p.w.WriteByte(')')
	p.wantSpace = spaceRequired
}

func (p *Printer) semiRsrv(s string, pos Pos) {
	if p.wantsNewline(pos, false) {
		p.newlines(pos)
	} else {
		if !p.wroteSemi {
			p.w.WriteByte(';')
		}
		if !p.minify {
			p.spacePad(pos)
		}
	}
	p.w.WriteString(s)
	p.wantSpace = spaceRequired
}

func (p *Printer) flushComments() {
	for i, c := range p.pendingComments {
		if i == 0 {
			// Flush any pending heredocs first. Otherwise, the
			// comments would become part of a heredoc body.
			p.flushHeredocs()
		}
		p.firstLine = false
		// We can't call any of the newline methods, as they call this
		// function and we'd recurse forever.
		cline := c.Hash.Line()
		switch {
		case p.mustNewline, i > 0, cline > p.line && p.line > 0:
			p.w.WriteByte('\n')
			if cline > p.line+1 {
				p.w.WriteByte('\n')
			}
			p.indent()
			p.wantSpace = spaceWritten
			p.spacePad(c.Pos())
		case p.wantSpace == spaceRequired:
			if p.keepPadding {
				p.spacePad(c.Pos())
			} else {
				p.w.WriteByte('\t')
			}
		case p.wantSpace != spaceWritten:
			p.space()
		}
		// don't go back one line, which may happen in some edge cases
		p.advanceLine(cline)
		p.w.WriteByte('#')
		p.writeLit(strings.TrimRightFunc(c.Text, unicode.IsSpace))
		p.wantNewline = true
		p.mustNewline = true
	}
	p.pendingComments = nil
}

func (p *Printer) comments(comments ...Comment) {
	if p.minify {
		for _, c := range comments {
			if fileutil.Shebang([]byte("#"+c.Text)) != "" && c.Hash.Col() == 1 && c.Hash.Line() == 1 {
				p.w.WriteString(strings.TrimRightFunc("#"+c.Text, unicode.IsSpace))
				p.w.WriteString("\n")
				p.line++
			}
		}
		return
	}
	p.pendingComments = append(p.pendingComments, comments...)
}

func (p *Printer) wordParts(wps []WordPart, quoted bool) {
	// We disallow unquoted escaped newlines between word parts below.
	// However, we want to allow a leading escaped newline for cases such as:
	//
	//   foo <<< \
	//     "bar baz"
	if !quoted && !p.singleLine && wps[0].Pos().Line() > p.line {
		p.bslashNewl()
	}
	for i, wp := range wps {
		var next WordPart
		if i+1 < len(wps) {
			next = wps[i+1]
		}
		// Keep escaped newlines separating word parts when quoted.
		// Note that those escaped newlines don't cause indentaiton.
		// When not quoted, we strip them out consistently,
		// because attempting to keep them would prevent indentation.
		// Can't use p.wantsNewline here, since this is only about
		// escaped newlines.
		for quoted && !p.singleLine && wp.Pos().Line() > p.line {
			p.w.WriteString("\\\n")
			p.line++
		}
		p.wordPart(wp, next)
		p.advanceLine(wp.End().Line())
	}
}

func (p *Printer) wordPart(wp, next WordPart) {
	switch wp := wp.(type) {
	case *Lit:
		p.writeLit(wp.Value)
	case *SglQuoted:
		if wp.Dollar {
			p.w.WriteByte('$')
		}
		p.w.WriteByte('\'')
		p.writeLit(wp.Value)
		p.w.WriteByte('\'')
		p.advanceLine(wp.End().Line())
	case *DblQuoted:
		p.dblQuoted(wp)
	case *CmdSubst:
		p.advanceLine(wp.Pos().Line())
		p.cmdSubst(wp)
	case *ParamExp:
		litCont := ";"
		if nextLit, ok := next.(*Lit); ok && nextLit.Value != "" {
			litCont = nextLit.Value[:1]
		}
		if p.minify && !wp.Short && wp.simple() {
			name := wp.Param.Value
			switch {
			case len(name) > 1 && !ValidName(name): // ${10}
			case ValidName(name + litCont): // ${var}cont
			default:
				x2 := *wp
				x2.Short = true
				p.paramExp(&x2)
				return
			}
		}
		p.paramExp(wp)
	case *ArithmExp:
		p.w.WriteString("$((")
		if wp.Unsigned {
			p.w.WriteString("# ")
		}
		p.inArithm++
		p.arithmExpr(wp.X, false, false)
		p.inArithm--
		p.w.WriteString("))")
	case *ExtGlob:
		p.w.WriteString(wp.Op.String())
		p.writeLit(wp.Pattern.Value)
		p.w.WriteByte(')')
	case *ProcSubst:
		// avoid conflict with << and others
		if p.wantSpace == spaceRequired {
			p.space()
		}
		p.w.WriteString(wp.Op.String())
		p.nestedStmts(wp.Stmts, wp.Last, wp.Rparen)
		p.rightParen(wp.Rparen)
	}
}

func (p *Printer) dblQuoted(dq *DblQuoted) {
	if dq.Dollar {
		p.w.WriteByte('$')
	}
	p.w.WriteByte('"')
	if len(dq.Parts) > 0 {
		p.wordParts(dq.Parts, true)
	}
	// Add any trailing escaped newlines.
	for p.line < dq.Right.Line() {
		p.w.WriteString("\\\n")
		p.line++
	}
	p.w.WriteByte('"')
}

func (p *Printer) wroteIndex(index ArithmExpr) bool {
	if index == nil {
		return false
	}
	p.w.WriteByte('[')
	// Note that e.g. foo[1,3]=$bar in Zsh does not allow any spaces around the comma,
	// as that breaks the assignment word.
	binary, ok := index.(*BinaryArithm)
	compact := ok && binary.Op == Comma
	p.arithmExpr(index, compact, false)
	p.w.WriteByte(']')
	return true
}

func (p *Printer) paramExp(pe *ParamExp) {
	if pe.nakedIndex() { // arr[x]
		p.writeLit(pe.Param.Value)
		p.wroteIndex(pe.Index)
		return
	}
	p.w.WriteByte('$')
	if !pe.Short {
		p.w.WriteByte('{')
	}
	if pe.Flags != nil {
		p.w.WriteByte('(')
		p.writeLit(pe.Flags.Value)
		p.w.WriteByte(')')
	}
	switch {
	case pe.Length:
		p.w.WriteByte('#')
	case pe.Width:
		p.w.WriteByte('%')
	case pe.IsSet:
		p.w.WriteByte('+')
	case pe.Excl:
		p.w.WriteByte('!')
	}
	for _, pre := range [...]struct {
		c     byte
		state OptState
	}{
		{'=', pe.Split},
		{'~', pe.GlobSubst},
		{'^', pe.RcExpand},
	} {
		if pre.state == OptUnset {
			continue
		}
		p.w.WriteByte(pre.c)
		if pre.state == OptOff {
			p.w.WriteByte(pre.c)
		}
	}
	switch {
	case pe.Param != nil:
		p.writeLit(pe.Param.Value)
	case pe.NestedParam != nil:
		// Note that Zsh supports ${${nested}} but not ${$nested},
		// so we need to avoid that simplification here.
		saved := p.minify
		p.minify = false
		p.wordPart(pe.NestedParam, nil)
		p.minify = saved
	}
	p.wroteIndex(pe.Index)
	switch {
	case len(pe.Modifiers) > 0:
		for _, lit := range pe.Modifiers {
			p.w.WriteByte(':')
			p.w.WriteString(lit.Value)
		}
	case pe.Slice != nil:
		p.w.WriteByte(':')
		p.arithmExpr(pe.Slice.Offset, true, true)
		if pe.Slice.Length != nil {
			p.w.WriteByte(':')
			p.arithmExpr(pe.Slice.Length, true, false)
		}
	case pe.Repl != nil:
		if pe.Repl.All {
			p.w.WriteByte('/')
		}
		p.w.WriteByte('/')
		if pe.Repl.Orig != nil {
			p.word(pe.Repl.Orig)
		}
		// The pattern/replacement separator is only printed when there is
		// a pattern or a replacement. Printing it for an empty pattern
		// (`${foo//}`) would make a leading `/` of the separator be
		// reparsed as a literal first character of the pattern.
		if pe.Repl.Orig != nil || pe.Repl.With != nil {
			p.w.WriteByte('/')
		}
		if pe.Repl.With != nil {
			p.word(pe.Repl.With)
		}
	case pe.BadSubst != nil:
		p.writeLit(pe.BadSubst.Value)
	case pe.Names != 0:
		p.writeLit(pe.Names.String())
	case pe.Exp != nil:
		p.w.WriteString(pe.Exp.Op.String())
		if pe.Exp.Word != nil {
			p.word(pe.Exp.Word)
		}
	}
	if !pe.Short {
		p.w.WriteByte('}')
	}
}

func (p *Printer) cmdSubst(cs *CmdSubst) {
	switch {
	case cs.TempFile:
		p.w.WriteString("${")
		p.wantSpace = spaceRequired
		p.nestedStmts(cs.Stmts, cs.Last, cs.Right)
		p.wantSpace = spaceNotRequired
		p.semiRsrv("}", cs.Right)
	case cs.ReplyVar:
		p.w.WriteString("${|")
		p.nestedStmts(cs.Stmts, cs.Last, cs.Right)
		p.wantSpace = spaceNotRequired
		p.semiRsrv("}", cs.Right)
	// Special case: `# inline comment`
	case cs.Backquotes && len(cs.Stmts) == 0 &&
		len(cs.Last) == 1 && cs.Right.Line() == p.line:
		p.w.WriteString("`#")
		p.w.WriteString(cs.Last[0].Text)
		p.w.WriteString("`")
	default:
		p.w.WriteString("$(")
		if len(cs.Stmts) > 0 && startsWithLparen(cs.Stmts[0]) {
			p.wantSpace = spaceRequired
		} else {
			p.wantSpace = spaceNotRequired
		}
		p.nestedStmts(cs.Stmts, cs.Last, cs.Right)
		p.rightParen(cs.Right)
	}
}

func (p *Printer) loop(loop Loop) {
	switch loop := loop.(type) {
	case *WordIter:
		p.writeLit(loop.Name.Value)
		if loop.InPos.IsValid() {
			p.spacedString(" in", Pos{})
			p.wordJoin(loop.Items)
		}
	case *CStyleLoop:
		p.w.WriteString("((")
		if loop.Init == nil {
			p.space()
		}
		p.arithmExpr(loop.Init, false, false)
		p.w.WriteString("; ")
		p.arithmExpr(loop.Cond, false, false)
		p.w.WriteString("; ")
		p.arithmExpr(loop.Post, false, false)
		p.w.WriteString("))")
	}
}

func (p *Printer) arithmExpr(expr ArithmExpr, compact, spacePlusMinus bool) {
	if p.minify {
		compact = true
	}
	p.arithmExprRecurse(expr, compact, spacePlusMinus)
}

func (p *Printer) arithmExprRecurse(expr ArithmExpr, compact, spacePlusMinus bool) {
	switch expr := expr.(type) {
	case *Word:
		p.word(expr)
	case *BinaryArithm:
		if compact {
			p.arithmExprRecurse(expr.X, compact, spacePlusMinus)
			p.w.WriteString(expr.Op.String())
			p.arithmExprRecurse(expr.Y, compact, false)
		} else if p.bashCompatArith {
			p.arithmExprRecurse(expr.X, compact, spacePlusMinus)
			leftSpace := expr.Op != Comma && hasGap(expr.X.End(), expr.OpPos)
			if leftSpace {
				p.space()
			}
			p.w.WriteString(expr.Op.String())
			opEnd := posAddCol(expr.OpPos, len(expr.Op.String()))
			if hasGap(opEnd, expr.Y.Pos()) {
				p.space()
			}
			p.arithmExprRecurse(expr.Y, compact, false)
		} else {
			p.arithmExprRecurse(expr.X, compact, spacePlusMinus)
			if expr.Op != Comma {
				p.space()
			}
			p.w.WriteString(expr.Op.String())
			p.space()
			p.arithmExprRecurse(expr.Y, compact, false)
		}
	case *UnaryArithm:
		if expr.Post {
			p.arithmExprRecurse(expr.X, compact, spacePlusMinus)
			p.w.WriteString(expr.Op.String())
		} else {
			if spacePlusMinus {
				switch expr.Op {
				case Plus, Minus:
					p.space()
				}
			}
			p.w.WriteString(expr.Op.String())
			p.arithmExprRecurse(expr.X, compact, false)
		}
	case *ParenArithm:
		p.w.WriteByte('(')
		p.arithmExprRecurse(expr.X, false, false)
		p.w.WriteByte(')')
	case *FlagsArithm:
		p.w.WriteByte('(')
		p.w.WriteString(expr.Flags.Value)
		p.w.WriteByte(')')
		if expr.X != nil {
			p.arithmExprRecurse(expr.X, compact, false)
		}
	}
}

func (p *Printer) testExpr(expr TestExpr) {
	// Multi-line test expressions don't need to escape newlines.
	if expr.Pos().Line() > p.line {
		p.newlines(expr.Pos())
		p.spacePad(expr.Pos())
	} else if p.wantSpace == spaceRequired {
		p.space()
	}
	p.testExprSameLine(expr)
}

func (p *Printer) testExprSameLine(expr TestExpr) {
	p.advanceLine(expr.Pos().Line())
	switch expr := expr.(type) {
	case *Word:
		p.word(expr)
	case *BinaryTest:
		p.testExprSameLine(expr.X)
		p.space()
		p.w.WriteString(expr.Op.String())
		switch expr.Op {
		case AndTest, OrTest:
			p.wantSpace = spaceRequired
			p.testExpr(expr.Y)
		default:
			p.space()
			p.testExprSameLine(expr.Y)
		}
	case *UnaryTest:
		p.w.WriteString(expr.Op.String())
		p.space()
		p.testExprSameLine(expr.X)
	case *ParenTest:
		p.w.WriteByte('(')
		if startsWithLparen(expr.X) {
			p.wantSpace = spaceRequired
		} else {
			p.wantSpace = spaceNotRequired
		}
		p.testExpr(expr.X)
		p.w.WriteByte(')')
	}
}

func (p *Printer) word(w *Word) {
	p.wordParts(w.Parts, false)
	p.wantSpace = spaceRequired
}

func (p *Printer) unquotedWord(w *Word) {
	for _, wp := range w.Parts {
		switch wp := wp.(type) {
		case *SglQuoted:
			p.writeLit(wp.Value)
		case *DblQuoted:
			p.wordParts(wp.Parts, true)
		case *Lit:
			for i := 0; i < len(wp.Value); i++ {
				if b := wp.Value[i]; b == '\\' {
					if i++; i < len(wp.Value) {
						p.w.WriteByte(wp.Value[i])
					}
				} else {
					p.w.WriteByte(b)
				}
			}
		}
	}
}

func (p *Printer) wordJoin(ws []*Word) {
	anyNewline := false
	for _, w := range ws {
		if pos := w.Pos(); pos.Line() > p.line && !p.singleLine {
			if !anyNewline {
				p.incLevel()
				anyNewline = true
			}
			p.bslashNewl()
		}
		p.spacePad(w.Pos())
		p.word(w)
	}
	if anyNewline {
		p.decLevel()
	}
}

func (p *Printer) casePatternJoin(pats []*Word) {
	anyNewline := false
	for i, w := range pats {
		// Only valid situation for a literal 'esac' here is with a preceding left paran.
		if i == 0 && w.Lit() == "esac" {
			p.w.WriteString("(")
		}
		if i > 0 {
			p.spacedToken("|", Pos{})
		}
		if p.wantsNewline(w.Pos(), true) {
			if !anyNewline {
				p.incLevel()
				anyNewline = true
			}
			p.bslashNewl()
		} else {
			p.spacePad(w.Pos())
		}
		p.word(w)
	}
	if anyNewline {
		p.decLevel()
	}
}

func (p *Printer) elemJoin(elems []*ArrayElem, last []Comment) {
	p.incLevel()
	for _, el := range elems {
		var left []Comment
		for _, c := range el.Comments {
			if c.Pos().After(el.Pos()) {
				left = append(left, c)
				break
			}
			p.comments(c)
		}
		// Multi-line array expressions don't need to escape newlines.
		if el.Pos().Line() > p.line {
			p.newlines(el.Pos())
			p.spacePad(el.Pos())
		} else if p.wantSpace == spaceRequired {
			p.space()
		}
		if p.wroteIndex(el.Index) {
			p.w.WriteByte('=')
		}
		if el.Value != nil {
			p.word(el.Value)
		}
		p.comments(left...)
	}
	if len(last) > 0 {
		p.comments(last...)
		p.flushComments()
	}
	p.decLevel()
}

func (p *Printer) stmt(s *Stmt) {
	p.wroteSemi = false
	if s.Negated {
		p.spacedString("!", s.Pos())
	}
	var startRedirs int
	if s.Cmd != nil {
		startRedirs = p.command(s.Cmd, s.Redirs)
	}
	p.incLevel()
	for _, r := range s.Redirs[startRedirs:] {
		if p.wantsNewline(r.OpPos, true) {
			p.bslashNewl()
		}
		if p.wantSpace == spaceRequired {
			p.spacePad(r.Pos())
		}
		if r.N != nil {
			p.writeLit(r.N.Value)
		}
		p.w.WriteString(r.Op.String())
		// Bash 5.3's `declare -f` output omits the space between
		// `<<` / `<<-` and the heredoc delimiter, even with
		// SpaceRedirects enabled. DplIn / DplOut (`>&` / `<&`)
		// share the same flush-the-operator convention.
		if p.spaceRedirects && r.Op != DplIn && r.Op != DplOut && r.Op != Hdoc && r.Op != DashHdoc {
			p.space()
		} else if r.BashPPKeepSpace {
			// Preserve the space that distinguishes a shell redirect from
			// a Bash++ channel arrow when this statement is reparsed.
			p.space()
		} else {
			p.wantSpace = spaceRequired
		}
		p.word(r.Word)
		if r.Op == Hdoc || r.Op == DashHdoc {
			p.pendingHdocs = append(p.pendingHdocs, r)
		}
	}
	sep := s.Semicolon.IsValid() && s.Semicolon.Line() > p.line && !p.singleLine
	if sep || s.Background || s.Coprocess || s.Disown {
		if sep {
			p.bslashNewl()
		} else if !p.minify {
			p.space()
		}
		if s.Background {
			p.w.WriteString("&")
		} else if s.Coprocess {
			p.w.WriteString("|&")
		} else if s.Disown {
			p.w.WriteString("&|")
		} else {
			p.w.WriteString(";")
		}
		p.wroteSemi = true
		p.wantSpace = spaceRequired
	}
	p.decLevel()
}

func (p *Printer) printRedirsUntil(redirs []*Redirect, startRedirs int, pos Pos) int {
	for _, r := range redirs[startRedirs:] {
		if r.Pos().After(pos) || r.Op == Hdoc || r.Op == DashHdoc {
			break
		}
		if p.wantSpace == spaceRequired {
			p.spacePad(r.Pos())
		}
		if r.N != nil {
			p.writeLit(r.N.Value)
		}
		p.w.WriteString(r.Op.String())
		if p.spaceRedirects && (r.Op != DplIn && r.Op != DplOut) {
			p.space()
		} else {
			p.wantSpace = spaceRequired
		}
		p.word(r.Word)
		startRedirs++
	}
	return startRedirs
}

func (p *Printer) bashppExpr(expr BashPPExpr) {
	switch x := expr.(type) {
	case *BashPPFuncLit:
		p.bashppFuncLit(x)
	case *BashPPBasicLit:
		p.writeLit(x.Value.Value)
	case *BashPPIdent:
		p.writeLit(x.Name.Value)
	case *BashPPParenExpr:
		p.writeLit("(")
		p.bashppExpr(x.X)
		p.writeLit(")")
	case *BashPPUnaryExpr:
		p.writeLit(x.Op.Value)
		p.bashppExpr(x.X)
	case *BashPPAddressExpr:
		p.writeLit("&")
		p.bashppExpr(x.X)
	case *BashPPDerefExpr:
		p.writeLit("*")
		p.bashppExpr(x.X)
	case *BashPPNewExpr:
		p.writeLit("new(")
		if x.Init != nil {
			p.bashppExpr(x.Init)
		} else {
			p.bashppType(x.AllocType)
		}
		p.writeLit(")")
	case *BashPPTypeAssertExpr:
		p.bashppExpr(x.X)
		p.writeLit(".(")
		if x.TypeToken != nil {
			p.writeLit("type")
		} else {
			p.bashppType(x.Assert)
		}
		p.writeLit(")")
	case *BashPPBinaryExpr:
		p.bashppExpr(x.X)
		p.space()
		p.writeLit(x.Op.Value)
		p.space()
		p.bashppExpr(x.Y)
	case *BashPPCall:
		p.command(x, nil)
	case *BashPPConvertExpr:
		if x.ConvTypeExpr != nil {
			p.bashppType(x.ConvTypeExpr)
		} else {
			p.writeLit(x.ConvType.Value)
		}
		p.writeLit("(")
		p.bashppExpr(x.X)
		p.writeLit(")")
	case *BashPPIndexExpr:
		p.bashppExpr(x.X)
		p.writeLit("[")
		p.bashppExpr(x.Index)
		p.writeLit("]")
	case *BashPPSliceExpr:
		p.bashppExpr(x.X)
		p.writeLit("[")
		if x.Low != nil {
			p.bashppExpr(x.Low)
		}
		p.writeLit(":")
		if x.High != nil {
			p.bashppExpr(x.High)
		}
		if x.SecondColon.IsValid() {
			p.writeLit(":")
			if x.Max != nil {
				p.bashppExpr(x.Max)
			}
		}
		p.writeLit("]")
	case *BashPPSelectorExpr:
		p.bashppExpr(x.X)
		p.writeLit(".")
		p.writeLit(x.Sel.Value)
	case *BashPPCompositeLit:
		if x.LitType != nil {
			p.bashppType(x.LitType)
		}
		p.writeLit("{")
		for i, elem := range x.Elems {
			if i > 0 {
				p.writeLit(", ")
			}
			if elem.Key != nil {
				p.bashppExpr(elem.Key)
				p.writeLit(": ")
			}
			p.bashppExpr(elem.Value)
		}
		p.writeLit("}")
	default:
		panic(fmt.Sprintf("unhandled Bash++ expression %T", expr))
	}
}

func (p *Printer) bashppType(typ BashPPTypeExpr) {
	switch x := typ.(type) {
	case *BashPPChanType:
		prefix := "chan "
		if x.Direction == "send" {
			prefix = "chan<- "
		}
		if x.Direction == "recv" {
			prefix = "<-chan "
		}
		p.writeLit(prefix)
		if x.Element != nil {
			p.bashppType(x.Element)
		} else if x.Elem != nil {
			p.writeLit(x.Elem.Value)
		}
	case *BashPPFuncType:
		p.writeLit("func")
		p.bashppSignature(x.Params, x.Results, x.ResLparen)
	case *BashPPNamedType:
		p.writeLit(x.Name.Value)
		if len(x.TypeArgs) > 0 {
			p.writeLit("[")
			for i, arg := range x.TypeArgs {
				if i > 0 {
					p.writeLit(", ")
				}
				p.bashppType(arg.ArgType)
			}
			p.writeLit("]")
		}
	case *BashPPTypeParamType:
		p.writeLit(x.Name.Value)
	case *BashPPUnionType:
		for i, term := range x.Terms {
			if i > 0 {
				p.writeLit(`\|`)
			}
			p.bashppType(term)
		}
	case *BashPPApproxType:
		p.writeLit("~")
		p.bashppType(x.Term)
	case *BashPPCollectionType:
		if x.Kind == "map" {
			p.writeLit("map[")
			p.bashppType(x.Key)
			p.writeLit("]")
		} else {
			p.writeLit("[")
			if x.Length != nil {
				p.writeLit(x.Length.Value)
			}
			p.writeLit("]")
		}
		p.bashppType(x.Element)
	case *BashPPStructType:
		// Keep the opening brace attached. Besides matching the compact Go
		// spelling, this lets the shell lexer expose an anonymous struct literal
		// as one committed composite prefix on a reparse.
		p.writeLit("struct{")
		for i, field := range x.Fields {
			if i > 0 {
				p.writeLit("; ")
			}
			if !field.Embedded {
				for j, name := range field.Names {
					if j > 0 {
						p.writeLit(", ")
					}
					p.writeLit(name.Value)
				}
				p.space()
			}
			if field.FieldTypeExpr != nil {
				p.bashppType(field.FieldTypeExpr)
			} else if field.FieldType != nil {
				p.writeLit(field.FieldType.Value)
			}
			if field.Tag != nil {
				p.writeLit(" " + field.Tag.Value)
			}
		}
		p.writeLit("}")
	case *BashPPPointerType:
		p.writeLit("*")
		p.bashppType(x.Element)
	case *BashPPInterfaceType:
		p.writeLit("interface{")
		for i, elem := range bashppInterfaceElems(x) {
			if i > 0 {
				p.writeLit("; ")
			}
			p.bashppInterfaceElem(elem)
		}
		p.writeLit("}")
	default:
		panic(fmt.Sprintf("unhandled Bash++ type %T", typ))
	}
}

func bashppInterfaceElems(iface *BashPPInterfaceType) []*BashPPInterfaceElem {
	if len(iface.Elems) > 0 {
		return iface.Elems
	}
	out := make([]*BashPPInterfaceElem, len(iface.Methods))
	for i, spec := range iface.Methods {
		out[i] = &BashPPInterfaceElem{Method: spec}
	}
	return out
}

func (p *Printer) bashppInterfaceElem(elem *BashPPInterfaceElem) {
	if elem.Method != nil {
		spec := elem.Method
		p.writeLit(spec.Name.Value)
		p.bashppSignature(spec.Params, spec.Results, spec.ResLparen)
		return
	}
	p.bashppType(elem.Embedded)
}

func (p *Printer) command(cmd Command, redirs []*Redirect) (startRedirs int) {
	p.advanceLine(cmd.Pos().Line())
	p.spacePad(cmd.Pos())
	switch cmd := cmd.(type) {
	case *SourceBlock:
		if cmd.Src != nil {
			p.writeLit(`embed ` + cmd.Language.Value + ` "` + cmd.Src.Value + `"`)
		} else {
			p.writeLit(cmd.Fence + cmd.Language.Value)
		}
		switch {
		case cmd.Alias != nil && cmd.Runner != nil && cmd.Alias.Value == cmd.Runner.Value:
			p.writeLit(" as !" + cmd.Runner.Value)
		case cmd.Alias != nil && cmd.Runner != nil:
			p.writeLit(" as " + cmd.Alias.Value + " !" + cmd.Runner.Value)
		case cmd.Alias != nil:
			p.writeLit(" as " + cmd.Alias.Value)
		case cmd.Runner != nil:
			p.writeLit(" !" + cmd.Runner.Value)
		}
		if cmd.Src != nil {
			p.wantSpace = spaceRequired
			break
		}
		p.w.WriteByte('\n')
		p.w.WriteString(cmd.Body)
		if cmd.Body != "" && !strings.HasSuffix(cmd.Body, "\n") {
			p.w.WriteByte('\n')
		}
		p.writeLit(cmd.Fence)
		p.line = cmd.ClosingPos.Line()
		p.wantSpace = spaceRequired
	case *CallExpr:
		p.assigns(cmd.Assigns)
		if len(cmd.Args) > 0 {
			startRedirs = p.printRedirsUntil(redirs, startRedirs, cmd.Args[0].Pos())
		}
		if len(cmd.Args) <= 1 {
			p.wordJoin(cmd.Args)
			return startRedirs
		}
		p.wordJoin(cmd.Args[:1])
		startRedirs = p.printRedirsUntil(redirs, startRedirs, cmd.Args[1].Pos())
		p.wordJoin(cmd.Args[1:])
	case *Block:
		p.w.WriteByte('{')
		// avoid ; in an empty block
		p.wroteSemi = true
		p.wantSpace = spaceRequired
		// Forbid "foo()\n{ bar; }"
		p.wantNewline = p.wantNewline || p.funcNextLine
		p.nestedStmts(cmd.Stmts, cmd.Last, cmd.Rbrace)
		p.semiRsrv("}", cmd.Rbrace)
	case *IfClause:
		p.ifClause(cmd, false)
	case *Subshell:
		p.w.WriteByte('(')
		stmts := cmd.Stmts
		if len(stmts) > 0 && startsWithLparen(stmts[0]) {
			p.wantSpace = spaceRequired
			// Add a space between nested parentheses if we're printing them in a single line,
			// to avoid the ambiguity between `((` and `( (`.
			if (cmd.Lparen.Line() != stmts[0].Pos().Line() || len(stmts) > 1) && !p.singleLine {
				p.wantSpace = spaceNotRequired

				if p.minify {
					p.mustNewline = true
				}
			}
		} else if len(stmts) == 0 {
			// Zsh allows empty subshells, but prevent `()`
			// from looking like `() { anon-func; }`.
			p.wantSpace = spaceRequired
		} else {
			p.wantSpace = spaceNotRequired
		}

		p.spacePad(stmtsPos(cmd.Stmts, cmd.Last))
		p.nestedStmts(cmd.Stmts, cmd.Last, cmd.Rparen)
		p.wantSpace = spaceNotRequired
		p.spacePad(cmd.Rparen)
		p.rightParen(cmd.Rparen)
	case *WhileClause:
		if cmd.Until {
			p.spacedString("until", cmd.Pos())
		} else {
			p.spacedString("while", cmd.Pos())
		}
		p.nestedStmts(cmd.Cond, cmd.CondLast, Pos{})
		p.semiOrNewl("do", cmd.DoPos)
		p.nestedStmts(cmd.Do, cmd.DoLast, cmd.DonePos)
		p.semiRsrv("done", cmd.DonePos)
	case *ForClause:
		if cmd.Select {
			p.w.WriteString("select ")
		} else {
			p.w.WriteString("for ")
		}
		p.loop(cmd.Loop)
		p.semiOrNewl("do", cmd.DoPos)
		p.nestedStmts(cmd.Do, cmd.DoLast, cmd.DonePos)
		p.semiRsrv("done", cmd.DonePos)
	case *BinaryCmd:
		p.stmt(cmd.X)
		if p.minify || p.singleLine || cmd.Y.Pos().Line() <= p.line {
			// leave p.nestedBinary untouched
			p.spacedToken(cmd.Op.String(), cmd.OpPos)
			p.advanceLine(cmd.Y.Pos().Line())
			p.stmt(cmd.Y)
			break
		}
		indent := !p.nestedBinary
		if indent {
			p.incLevel()
		}
		if p.binNextLine {
			if len(p.pendingHdocs) == 0 {
				p.bslashNewl()
			}
			p.spacedToken(cmd.Op.String(), cmd.OpPos)
			if len(cmd.Y.Comments) > 0 {
				p.wantSpace = spaceNotRequired
				p.newline(cmd.Y.Pos())
				p.indent()
				p.comments(cmd.Y.Comments...)
				p.newline(Pos{})
				p.indent()
			}
		} else {
			p.spacedToken(cmd.Op.String(), cmd.OpPos)
			p.advanceLine(cmd.OpPos.Line())
			p.comments(cmd.Y.Comments...)
			p.newline(Pos{})
			p.indent()
		}
		p.advanceLine(cmd.Y.Pos().Line())
		_, p.nestedBinary = cmd.Y.Cmd.(*BinaryCmd)
		p.stmt(cmd.Y)
		if indent {
			p.decLevel()
		}
		p.nestedBinary = false
	case *FuncDecl:
		p.bashppDecorators(cmd.Decorators, func() Pos {
			if cmd.Agentic != nil {
				return cmd.Agentic.Pos()
			}
			return cmd.Position
		}())
		if cmd.Agentic != nil {
			p.spacedString("agentic", cmd.Agentic.Pos())
		}
		if cmd.RsrvWord {
			p.spacedString("function", Pos{})
		}
		if cmd.Name != nil {
			p.spacedString(cmd.Name.Value, Pos{})
		} else {
			for _, name := range cmd.Names {
				p.spacedString(name.Value, Pos{})
			}
		}
		if !cmd.RsrvWord || cmd.Parens {
			p.w.WriteString("()")
			p.wantSpace = spaceNotRequired
		}
		if p.funcNextLine {
			p.newline(Pos{})
			p.indent()
		} else if !cmd.Parens || !p.minify {
			p.space()
		}
		p.advanceLine(cmd.Body.Pos().Line())
		p.comments(cmd.Body.Comments...)
		p.stmt(cmd.Body)
	case *CaseClause:
		p.w.WriteString("case ")
		p.word(cmd.Word)
		p.w.WriteString(" in")
		p.advanceLine(cmd.In.Line())
		p.wantSpace = spaceRequired
		if p.swtCaseIndent {
			p.incLevel()
		}
		if len(cmd.Items) == 0 {
			// Apparently "case x in; esac" is invalid shell.
			p.mustNewline = true
		}
		for i, ci := range cmd.Items {
			var last []Comment
			for i, c := range ci.Comments {
				if c.Pos().After(ci.Pos()) {
					last = ci.Comments[i:]
					break
				}
				p.comments(c)
			}
			p.newlines(ci.Pos())
			p.spacePad(ci.Pos())
			p.casePatternJoin(ci.Patterns)
			p.w.WriteByte(')')
			if !p.minify {
				p.wantSpace = spaceRequired
			} else {
				p.wantSpace = spaceNotRequired
			}

			p.nestedStmts(ci.Stmts, ci.Last, ci.OpPos)
			p.level++
			if !p.minify || i != len(cmd.Items)-1 {
				if p.wantsNewline(ci.OpPos, false) {
					p.newlines(ci.OpPos)
					p.wantNewline = true
				}
				p.spacedToken(ci.Op.String(), ci.OpPos)
				p.advanceLine(ci.OpPos.Line())
				// avoid ; directly after tokens like ;;
				p.wroteSemi = true
			}
			p.comments(last...)
			p.flushComments()
			p.level--
		}
		p.comments(cmd.Last...)
		if p.swtCaseIndent {
			p.flushComments()
			p.decLevel()
		}
		p.semiRsrv("esac", cmd.Esac)
	case *ArithmCmd:
		p.w.WriteString("((")
		if cmd.Unsigned {
			p.w.WriteString("# ")
		}
		p.arithmExpr(cmd.X, false, false)
		p.w.WriteString("))")
	case *TestClause:
		p.w.WriteString("[[ ")
		p.incLevel()
		p.testExpr(cmd.X)
		p.decLevel()
		p.spacedString("]]", cmd.Right)
	case *DeclClause:
		p.spacedString(cmd.Variant.Value, cmd.Pos())
		p.assigns(cmd.Args)
	case *TimeClause:
		p.spacedString("time", cmd.Pos())
		if cmd.PosixFormat {
			p.spacedString("-p", cmd.Pos())
		}
		if cmd.Stmt != nil {
			p.stmt(cmd.Stmt)
		}
	case *CoprocClause:
		p.spacedString("coproc", cmd.Pos())
		if cmd.Name != nil {
			p.space()
			p.word(cmd.Name)
		}
		p.space()
		p.stmt(cmd.Stmt)
	case *LetClause:
		p.spacedString("let", cmd.Pos())
		for _, n := range cmd.Exprs {
			p.space()
			p.arithmExpr(n, true, false)
		}
	case *TestDecl:
		p.spacedString("@test", cmd.Pos())
		p.space()
		p.word(cmd.Description)
		p.space()
		p.stmt(cmd.Body)
	case *BashPPDecl:
		// Printed from the node's own positions rather than from the source
		// it came from, which is what makes the printer the second reader of
		// the classification instead of a second classifier. `=` carries no
		// position of its own — the node does not record one — so it is
		// written at whatever column the surrounding words leave it.
		p.spacedString(cmd.Kw.Value, cmd.Kw.Pos())
		p.spacedString(cmd.Name.Value, cmd.Name.Pos())
		if len(cmd.TypeParams) > 0 {
			p.writeLit("[")
			for i, param := range cmd.TypeParams {
				if i > 0 {
					p.writeLit(", ")
				}
				for j, name := range param.Names {
					if j > 0 {
						p.writeLit(", ")
					}
					p.writeLit(name.Value)
				}
				p.space()
				p.bashppType(param.Constraint)
			}
			p.writeLit("]")
			p.space()
		}
		if cmd.DeclType != nil {
			if cmd.Alias {
				p.spacedString("=", Pos{})
			}
			p.spacedString(cmd.DeclType.Value, cmd.DeclType.Pos())
		}
		if len(cmd.StructFields) > 0 {
			p.spacedString("{", cmd.Lbrace)
			for i, field := range cmd.StructFields {
				if i > 0 {
					p.writeLit(";")
				}
				if !field.Embedded {
					p.spacedString(field.Names[0].Value, field.Names[0].Pos())
				}
				p.spacedString(field.FieldType.Value, field.FieldType.Pos())
				if field.Tag != nil {
					p.spacedString(field.Tag.Value, field.Tag.Pos())
				}
			}
			p.spacedString("}", cmd.Rbrace)
		}
		if len(cmd.EnumMembers) > 0 {
			p.spacedString("{", cmd.Lbrace)
			for i, member := range cmd.EnumMembers {
				if i > 0 {
					p.writeLit(";")
				}
				p.spacedString(member.Value, member.Pos())
			}
			p.spacedString("}", cmd.Rbrace)
		}
		if iface, ok := cmd.DeclTypeExpr.(*BashPPInterfaceType); ok {
			p.spacedString("{", cmd.Lbrace)
			for i, elem := range bashppInterfaceElems(iface) {
				if i > 0 {
					p.writeLit("; ")
				}
				p.spacePad(elem.Pos())
				p.bashppInterfaceElem(elem)
			}
			if len(bashppInterfaceElems(iface)) > 0 {
				p.space()
			}
			p.spacedString("}", cmd.Rbrace)
		}
		if len(cmd.Init) > 0 {
			p.spacedString("=", Pos{})
			p.wordJoin(cmd.Init)
		}
	case *BashPPAssign:
		if len(cmd.Names) > 0 {
			for i, name := range cmd.Names {
				if i > 0 {
					p.writeLit(",")
				}
				p.spacedString(name.Value, name.Pos())
			}
			p.spacedString("=", cmd.Eq)
			p.space()
			if cmd.Recv != nil {
				p.command(cmd.Recv, nil)
				break
			}
			if cmd.Call != nil {
				p.command(cmd.Call, nil)
				break
			}
			if len(cmd.ValueExprs) == 0 {
				for i, value := range cmd.Values {
					if i > 0 {
						p.space()
					}
					p.word(value)
				}
				break
			}
			for i, value := range cmd.Values {
				if i > 0 {
					p.writeLit(", ")
				}
				p.word(value)
			}
			break
		}
		p.word(cmd.Target)
		p.spacedString("=", cmd.Eq)
		p.space()
		if cmd.Recv != nil {
			p.command(cmd.Recv, nil)
		} else if cmd.Call != nil {
			p.command(cmd.Call, nil)
		} else {
			p.word(cmd.Value)
		}
	case *BashPPForAssign:
		p.writeLit(cmd.Name.Value)
		p.writeLit(" = ")
		p.bashppExpr(cmd.Expr)
	case *BashPPIncDec:
		if cmd.TargetWord != nil {
			p.word(cmd.TargetWord)
		} else {
			p.writeLit(cmd.Name.Value)
		}
		p.writeLit(cmd.Op.Value)
	case *BashPPUpdate:
		p.word(cmd.TargetWord)
		p.spacedString(cmd.Op.Value, cmd.Op.Pos())
		p.space()
		p.word(cmd.ValueWord)
	case *BashPPShortDecl:
		for i, lhs := range cmd.Lhs {
			if i > 0 {
				p.writeLit(",")
			}
			p.spacedString(lhs.Value, lhs.Pos())
		}
		p.spacedString(":=", cmd.OpPos)
		switch {
		case len(cmd.RhsExprs) > 0:
			for i, expr := range cmd.RhsExprs {
				if i > 0 {
					p.writeLit(",")
				}
				p.space()
				p.bashppExpr(expr)
			}
		case cmd.FuncLit != nil:
			p.bashppFuncLit(cmd.FuncLit)
		case cmd.Call != nil:
			// A call is the source of record for named arguments (and for an
			// invoked literal, which has no word spelling in Rhs at all).
			p.space()
			p.command(cmd.Call, nil)
		case cmd.Recv != nil:
			p.space()
			p.command(cmd.Recv, nil)
		case cmd.MakeChan != nil:
			p.space()
			p.bashppMakeChan(cmd.MakeChan)
		case cmd.Expr != nil:
			p.space()
			p.bashppExpr(cmd.Expr)
		default:
			p.space()
			for i, rhs := range cmd.Rhs {
				if i > 0 {
					p.writeLit(", ")
				}
				p.word(rhs)
			}
		}
	case *BashPPCall:
		if cmd.CalleeExpr != nil {
			p.bashppExpr(cmd.CalleeExpr)
		}
		if cmd.FuncLit != nil {
			p.bashppFuncLit(cmd.FuncLit)
		}
		if cmd.PointerMethodExpr && len(cmd.Fun) == 2 {
			p.writeLit("(*")
			p.writeLit(cmd.Fun[0].Value)
			p.writeLit(").")
			p.writeLit(cmd.Fun[1].Value)
		}
		for i, fun := range cmd.Fun {
			if cmd.PointerMethodExpr {
				break
			}
			if i > 0 {
				p.writeLit(".")
			}
			p.writeLit(fun.Value)
		}
		if len(cmd.TypeArgs) > 0 {
			p.writeLit("[")
			for i, arg := range cmd.TypeArgs {
				if i > 0 {
					p.writeLit(", ")
				}
				p.bashppType(arg.ArgType)
			}
			p.writeLit("]")
		}
		p.writeLit("(")
		if cmd.ArgExprs != nil {
			for i, arg := range cmd.ArgExprs {
				if i > 0 {
					p.writeLit(", ")
				}
				if arg == nil && i == 0 && cmd.ArgType != nil {
					p.bashppType(cmd.ArgType)
				} else {
					p.bashppExpr(arg)
				}
			}
		} else {
			positional := len(cmd.Args) - len(cmd.ArgNames)
			for i, arg := range cmd.Args {
				if i > 0 {
					p.writeLit(", ")
				}
				if i >= positional {
					p.writeLit(cmd.ArgNames[i-positional].Value)
					p.writeLit(": ")
				}
				p.word(arg)
			}
		}
		if cmd.Ellipsis.IsValid() {
			p.writeLit("...")
		}
		p.writeLit(")")
	case *BashPPCommandCall:
		p.wordJoin(cmd.Before)
		p.space()
		p.command(cmd.Call, nil)
	case *BashPPAgenticBlock:
		p.spacedString("agentic", cmd.Kw.Pos())
		p.space()
		p.command(cmd.Body, nil)
	case *BashPPFuncDecl:
		p.bashppDecorators(cmd.Decorators, func() Pos {
			if cmd.Agentic != nil {
				return cmd.Agentic.Pos()
			}
			return cmd.Kw.Pos()
		}())
		if cmd.Agentic != nil {
			p.spacedString("agentic", cmd.Agentic.Pos())
		}
		p.spacedString(cmd.Kw.Value, cmd.Kw.Pos())
		if cmd.Receiver != nil {
			p.space()
			p.writeLit("(")
			p.writeLit(cmd.Receiver.Name.Value)
			p.writeLit(" ")
			if cmd.Receiver.Pointer {
				p.writeLit("*")
			}
			p.writeLit(cmd.Receiver.RecvType.Value)
			if len(cmd.Receiver.TypeParams) > 0 {
				p.writeLit("[")
				for i, param := range cmd.Receiver.TypeParams {
					if i > 0 {
						p.writeLit(", ")
					}
					p.writeLit(param.Value)
				}
				p.writeLit("]")
			}
			p.writeLit(")")
			p.space()
		}
		p.spacedString(cmd.Name.Value, cmd.Name.Pos())
		if len(cmd.TypeParams) > 0 {
			p.writeLit("[")
			for i, param := range cmd.TypeParams {
				if i > 0 {
					p.writeLit(", ")
				}
				for j, name := range param.Names {
					if j > 0 {
						p.writeLit(", ")
					}
					p.writeLit(name.Value)
				}
				p.writeLit(" ")
				p.bashppType(param.Constraint)
			}
			p.writeLit("]")
		}
		p.bashppSignature(cmd.Params, cmd.Results, cmd.ResLparen)
		p.wantSpace = spaceRequired
		if cmd.Body != nil {
			p.command(cmd.Body, nil)
		}
	case *BashPPReturn:
		p.spacedString(cmd.Kw.Value, cmd.Kw.Pos())
		if len(cmd.ResultExprs) > 0 {
			for i, expr := range cmd.ResultExprs {
				if i > 0 {
					p.writeLit(",")
				}
				p.space()
				p.bashppExpr(expr)
			}
			break
		}
		if cmd.Expr != nil {
			p.space()
			p.bashppExpr(cmd.Expr)
			break
		}
		if cmd.Call != nil {
			p.space()
			p.command(cmd.Call, nil)
			break
		}
		if cmd.FuncLit != nil {
			p.bashppFuncLit(cmd.FuncLit)
		}
		for i, res := range cmd.Results {
			if i > 0 {
				p.writeLit(",")
			}
			p.space()
			p.word(res)
		}
	case *BashPPDefer:
		p.spacedString(cmd.Kw.Value, cmd.Kw.Pos())
		p.wantSpace = spaceRequired
		if cmd.Call != nil {
			p.command(cmd.Call, nil)
		}
	case *BashPPGo:
		p.spacedString(cmd.Kw.Value, cmd.Kw.Pos())
		p.wantSpace = spaceRequired
		p.command(cmd.Call, nil)
	case *BashPPSend:
		if cmd.ChanExpr != nil {
			p.bashppExpr(cmd.ChanExpr)
		} else {
			p.word(cmd.Chan)
		}
		p.writeLit(" <- ")
		if cmd.ValueExpr != nil {
			p.bashppExpr(cmd.ValueExpr)
		} else {
			p.word(cmd.Value)
		}
	case *BashPPReceive:
		p.writeLit("<-")
		if cmd.ChanExpr != nil {
			p.bashppExpr(cmd.ChanExpr)
		} else {
			p.word(cmd.Chan)
		}
	case *BashPPClose:
		p.spacedString(cmd.Kw.Value, cmd.Kw.Pos())
		p.writeLit("(")
		p.word(cmd.Chan)
		p.writeLit(")")
	case *BashPPSelect:
		p.writeLit("select {")
		// Canonicalize the first arm and every body onto their own line on the
		// first print. Depending on source positions here makes compact input
		// require two formatting passes to reach a fixed point.
		p.wantNewline = len(cmd.Cases) > 0
		for _, arm := range cmd.Cases {
			var before, after []Comment
			for _, c := range arm.Comments {
				if arm.Pos().After(c.Pos()) {
					before = append(before, c)
				} else {
					after = append(after, c)
				}
			}
			if len(before) > 0 {
				p.wantSpace = spaceRequired
			}
			p.comments(before...)
			p.newlines(arm.Pos())
			p.spacePad(arm.Pos())
			if arm.Default {
				p.writeLit("default:")
			} else {
				p.writeLit("case ")
				p.command(arm.Comm, nil)
				p.writeLit(":")
			}
			if len(after) > 0 {
				p.wantSpace = spaceRequired
			}
			p.comments(after...)
			p.wantNewline = true
			p.nestedStmts(arm.Stmts, arm.Last, cmd.Rbrace)
			// A following case/default must not be glued to the final command
			// in this arm. Force the same newline that Go's implicit semicolon
			// requires; this also keeps an empty arm distinct from the next one.
			p.wantNewline = true
		}
		p.newlines(cmd.Rbrace)
		p.writeLit("}")
	case *BashPPRange:
		p.writeLit("for")
		// `for range ch { … }` discards the received values, so it has no
		// names and must grow neither a `:=` nor the space before one.
		for i, name := range cmd.Names {
			if i > 0 {
				p.writeLit(",")
			}
			p.writeLit(" ")
			p.writeLit(name.Value)
		}
		if len(cmd.Names) > 0 {
			p.writeLit(" :=")
		}
		p.writeLit(" range ")
		p.word(cmd.Chan)
		p.space()
		p.command(cmd.Body, nil)
	case *BashPPSwitch:
		p.writeLit("switch")
		if cmd.Init != nil || cmd.Semicolon.IsValid() || cmd.Tag != nil {
			p.space()
		}
		if cmd.Init != nil {
			p.command(cmd.Init, nil)
		}
		if cmd.Semicolon.IsValid() {
			p.writeLit("; ")
		}
		if cmd.Tag != nil {
			p.bashppExpr(cmd.Tag)
		}
		p.space()
		p.writeLit("{")
		p.wantSpace = spaceRequired
		for _, arm := range cmd.Arms {
			p.newlines(arm.Pos())
			if len(arm.Exprs) == 0 && len(arm.Types) == 0 {
				p.writeLit("default:")
			} else {
				p.writeLit("case ")
				for i, typ := range arm.Types {
					if i > 0 {
						p.writeLit(", ")
					}
					p.bashppType(typ)
				}
				for i, expr := range arm.Exprs {
					if i > 0 || len(arm.Types) > 0 {
						p.writeLit(", ")
					}
					p.bashppExpr(expr)
				}
				p.writeLit(":")
			}
			p.wantSpace = spaceRequired
			p.nestedStmts(arm.Stmts, arm.Last, cmd.Rbrace)
		}
		p.newlines(cmd.Rbrace)
		p.writeLit("}")
	case *BashPPIf:
		p.writeLit("if ")
		if cmd.InitStmt != nil {
			p.command(cmd.InitStmt, nil)
			p.writeLit("; ")
		} else if cmd.Init != nil {
			p.command(cmd.Init, nil)
			p.writeLit("; ")
		}
		p.bashppExpr(cmd.Cond)
		p.space()
		p.command(cmd.Then, nil)
		if cmd.Else != nil {
			p.space()
			p.writeLit("else ")
			p.command(cmd.Else, nil)
		}
	case *BashPPFor:
		p.writeLit("for")
		if cmd.FirstSemi.IsValid() {
			p.space()
			if cmd.Init != nil {
				p.command(cmd.Init, nil)
			}
			p.writeLit("; ")
			if cmd.Cond != nil {
				p.bashppExpr(cmd.Cond)
			}
			p.writeLit("; ")
			if cmd.Post != nil {
				p.command(cmd.Post, nil)
			}
		} else if cmd.Cond != nil {
			p.space()
			p.bashppExpr(cmd.Cond)
		}
		p.space()
		p.command(cmd.Body, nil)
	case *BashPPBranch:
		p.writeLit(cmd.Kw.Value)
	case *BashPPLabeled:
		p.writeLit(cmd.Label.Value + ":")
		if cmd.Stmt != nil {
			p.space()
			p.stmt(cmd.Stmt)
		}
	case *BashPPGoto:
		p.spacedString(cmd.Kw.Value, cmd.Kw.Pos())
		p.space()
		p.writeLit(cmd.Label.Value)
	case *BashPPConstGroup:
		p.spacedString(cmd.Kw.Value, cmd.Kw.Pos())
		p.spacedString("(", cmd.Lparen)
		p.advanceLine(cmd.Lparen.Line())
		p.incLevel()
		for _, spec := range cmd.Specs {
			p.comments(spec.Comments...)
			if spec.Pos().Line() <= p.line {
				p.newline(spec.Pos())
				p.indent()
			} else {
				p.newlines(spec.Pos())
				p.spacePad(spec.Pos())
			}
			p.spacedString(spec.Name.Value, spec.Name.Pos())
			if spec.DeclTypeExpr != nil {
				p.space()
				p.bashppType(spec.DeclTypeExpr)
			}
			if spec.InitExpr != nil {
				p.writeLit(" = ")
				p.bashppExpr(spec.InitExpr)
			}
		}
		p.comments(cmd.Last...)
		p.flushComments()
		p.decLevel()
		if len(cmd.Specs) == 0 && len(cmd.Last) == 0 {
			p.wantSpace = spaceNotRequired
		}
		if len(cmd.Specs) > 0 && cmd.Rparen.Line() <= p.line {
			p.newline(cmd.Rparen)
			p.indent()
		} else {
			p.newlines(cmd.Rparen)
			p.spacePad(cmd.Rparen)
		}
		p.writeLit(")")
		p.wantSpace = spaceRequired
	case *BashPPImport:
		p.spacedString(cmd.Kw.Value, cmd.Kw.Pos())
		if cmd.Path != nil {
			if cmd.Language != nil {
				p.spacedString(cmd.Language.Value, cmd.Language.Pos())
				if cmd.Environment != nil {
					p.writeLit("[")
					p.writeLit(cmd.Environment.Value)
					p.writeLit("]")
				}
			} else if cmd.Alias != nil {
				p.spacedString(cmd.Alias.Value, cmd.Alias.Pos())
			}
			p.space()
			p.dblQuoted(cmd.Path)
			if cmd.Language != nil && cmd.Alias != nil {
				p.writeLit(" as ")
				p.writeLit(cmd.Alias.Value)
			}
			break
		}
		if len(cmd.Comments) > 0 {
			p.comments(cmd.Comments...)
			p.flushComments()
			p.newlines(cmd.Lparen)
			p.spacePad(cmd.Lparen)
			p.writeLit("(")
			p.wantSpace = spaceRequired
		} else {
			p.spacedString("(", cmd.Lparen)
		}
		p.advanceLine(cmd.Lparen.Line())
		p.incLevel()
		for _, spec := range cmd.Specs {
			p.comments(spec.Comments...)
			if spec.Pos().Line() <= p.line {
				p.newline(spec.Pos())
				p.indent()
			} else {
				p.newlines(spec.Pos())
				p.spacePad(spec.Pos())
			}
			if spec.Alias != nil {
				p.spacedString(spec.Alias.Value, spec.Alias.Pos())
				p.space()
			}
			p.dblQuoted(spec.Path)
			p.wantSpace = spaceRequired
		}
		p.comments(cmd.Last...)
		p.flushComments()
		p.decLevel()
		if len(cmd.Specs) == 0 && len(cmd.Last) == 0 {
			p.wantSpace = spaceNotRequired
		}
		if len(cmd.Specs) > 0 && cmd.Rparen.Line() <= p.line {
			p.newline(cmd.Rparen)
			p.indent()
		} else {
			p.newlines(cmd.Rparen)
			p.spacePad(cmd.Rparen)
		}
		p.writeLit(")")
		p.wantSpace = spaceRequired
	default:
		panic(fmt.Sprintf("syntax.Printer: unexpected node type %T", cmd))
	}
	return startRedirs
}

func (p *Printer) bashppDecorators(decorators []*BashPPDecorator, target Pos) {
	for i, decorator := range decorators {
		p.spacePad(decorator.At)
		p.writeLit("@")
		p.writeLit(decorator.Name.Value)
		p.writeLit("(")
		positional := len(decorator.Args) - len(decorator.ArgNames)
		for j, arg := range decorator.Args {
			if j > 0 {
				p.writeLit(", ")
			}
			if j >= positional {
				p.writeLit(decorator.ArgNames[j-positional].Value)
				p.writeLit(": ")
			}
			p.word(arg)
		}
		p.writeLit(")")
		p.wantSpace = spaceRequired

		next := target
		if i+1 < len(decorators) {
			next = decorators[i+1].Pos()
		}
		if decorator.Semicolon.IsValid() || p.minify || p.singleLine {
			p.writeLit(";")
			p.wantSpace = spaceRequired
		}
		p.comments(decorator.Comments...)
		p.newlines(next)
	}
}

func (p *Printer) ifClause(ic *IfClause, elif bool) {
	if !elif {
		p.spacedString("if", ic.Pos())
	}
	p.nestedStmts(ic.Cond, ic.CondLast, Pos{})
	p.semiOrNewl("then", ic.ThenPos)
	thenEnd := ic.FiPos
	el := ic.Else
	if el != nil {
		thenEnd = el.Position
	}
	p.nestedStmts(ic.Then, ic.ThenLast, thenEnd)

	if el != nil && el.ThenPos.IsValid() {
		p.comments(ic.Last...)
		p.semiRsrv("elif", el.Position)
		p.ifClause(el, true)
		return
	}
	if el == nil {
		p.comments(ic.Last...)
	} else {
		var left []Comment
		for _, c := range ic.Last {
			if c.Pos().After(el.Position) {
				left = append(left, c)
				break
			}
			p.comments(c)
		}
		p.semiRsrv("else", el.Position)
		p.comments(left...)
		p.nestedStmts(el.Then, el.ThenLast, ic.FiPos)
		p.comments(el.Last...)
	}
	p.semiRsrv("fi", ic.FiPos)
}

func (p *Printer) stmtList(stmts []*Stmt, last []Comment) {
	sep := p.wantNewline || (len(stmts) > 0 && stmts[0].Pos().Line() > p.line)
	for i, s := range stmts {
		if i > 0 && p.singleLine && p.wantNewline && !p.wroteSemi {
			// In singleLine mode, ensure we use semicolons between
			// statements.
			p.w.WriteByte(';')
			p.wantSpace = spaceRequired
		}
		pos := s.Pos()
		var midComs, endComs []Comment
		for _, c := range s.Comments {
			// Comments after the end of this command. Note that
			// this includes "<<EOF # comment".
			if s.Cmd != nil && c.End().After(s.Cmd.End()) {
				endComs = append(endComs, c)
				break
			}
			// Comments between the beginning of the statement and
			// the end of the command.
			if c.Pos().After(pos) {
				midComs = append(midComs, c)
				continue
			}
			// The rest of the comments are before the entire
			// statement.
			p.comments(c)
		}
		if p.mustNewline || !p.minify || p.wantSpace == spaceRequired {
			p.newlines(pos)
		}
		p.advanceLine(pos.Line())
		p.comments(midComs...)
		p.stmt(s)
		p.comments(endComs...)
		p.wantNewline = true
	}
	if len(stmts) == 1 && !sep {
		p.wantNewline = false
	}
	p.comments(last...)
}

func (p *Printer) nestedStmts(stmts []*Stmt, last []Comment, closing Pos) {
	p.incLevel()
	switch {
	case len(stmts) > 1:
		// Force a newline if we find:
		//     { stmt; stmt; }
		p.wantNewline = true
	case closing.Line() > p.line && len(stmts) > 0 &&
		stmtsEnd(stmts, last).Line() < closing.Line():
		// Force a newline if we find:
		//     { stmt
		//     }
		p.wantNewline = true
	case len(p.pendingComments) > 0 && len(stmts) > 0:
		// Force a newline if we find:
		//     for i in a b # stmt
		//     do foo; done
		p.wantNewline = true
	}
	p.stmtList(stmts, last)
	if closing.IsValid() {
		p.flushComments()
	}
	p.decLevel()
}

func (p *Printer) assigns(assigns []*Assign) {
	p.incLevel()
	for _, a := range assigns {
		if p.wantsNewline(a.Pos(), true) {
			p.bslashNewl()
		} else {
			p.spacePad(a.Pos())
		}
		if a.Name != nil {
			p.writeLit(a.Name.Value)
			p.wroteIndex(a.Index)
			if a.Append {
				p.w.WriteByte('+')
			}
			if !a.Naked {
				p.w.WriteByte('=')
			}
		}
		if a.Value != nil {
			// Ensure we don't use an escaped newline after '=',
			// because that can result in indentation, thus
			// splitting "foo=bar" into "foo= bar".
			p.advanceLine(a.Value.Pos().Line())
			p.word(a.Value)
		} else if a.Array != nil {
			p.wantSpace = spaceNotRequired
			p.w.WriteByte('(')
			p.elemJoin(a.Array.Elems, a.Array.Last)
			p.rightParen(a.Array.Rparen)
		}
		p.wantSpace = spaceRequired
	}
	p.decLevel()
}

type wantSpaceState uint8

const (
	spaceNotRequired wantSpaceState = iota
	spaceRequired                   // we should generally print a space or a newline next
	spaceWritten                    // we have just written a space or newline
)

// extraIndenter ensures that all lines in a '<<-' heredoc body have at least
// baseIndent leading tabs. Those that had more tab indentation than the first
// heredoc line will keep that relative indentation.
type extraIndenter struct {
	bufWriter
	baseIndent int

	firstIndent int
	firstChange int
	curLine     []byte
}

func (e *extraIndenter) WriteByte(b byte) error {
	e.curLine = append(e.curLine, b)
	if b != '\n' {
		return nil
	}
	trimmed := bytes.TrimLeft(e.curLine, "\t")
	if len(trimmed) == 1 {
		// no tabs if this is an empty line, i.e. "\n"
		e.bufWriter.Write(trimmed)
		e.curLine = e.curLine[:0]
		return nil
	}

	lineIndent := len(e.curLine) - len(trimmed)
	if e.firstIndent < 0 {
		// This is the first heredoc line we add extra indentation to.
		// Keep track of how much we indented.
		e.firstIndent = lineIndent
		e.firstChange = e.baseIndent - lineIndent
		lineIndent = e.baseIndent

	} else if lineIndent < e.firstIndent {
		// This line did not have enough indentation; simply indent it
		// like the first line.
		lineIndent = e.firstIndent
	} else {
		// This line had plenty of indentation. Add the extra
		// indentation that the first line had, for consistency.
		lineIndent += e.firstChange
	}
	e.bufWriter.WriteByte(tabwriter.Escape)
	for range lineIndent {
		e.bufWriter.WriteByte('\t')
	}
	e.bufWriter.WriteByte(tabwriter.Escape)
	e.bufWriter.Write(trimmed)
	e.curLine = e.curLine[:0]
	return nil
}

func (e *extraIndenter) WriteString(s string) (int, error) {
	for i := range len(s) {
		e.WriteByte(s[i])
	}
	return len(s), nil
}

func startsWithLparen(node Node) bool {
	switch node := node.(type) {
	case *Stmt:
		return startsWithLparen(node.Cmd)
	case *BinaryCmd:
		return startsWithLparen(node.X)
	case *Subshell:
		return true // keep ( (
	case *ArithmCmd:
		return true // keep ( ((
	}
	return false
}
