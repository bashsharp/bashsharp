package interp

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// tracer prints expressions like a shell would do if its
// options '-o' is set to either 'xtrace' or its shorthand, '-x'.
type tracer struct {
	buf       bytes.Buffer
	printer   *syntax.Printer
	output    io.Writer
	needsPlus bool
	// plus is the expanded PS4 prefix written before each traced
	// line (bash replicates PS4's first character to show depth).
	plus string
}

func (r *Runner) tracer(pos syntax.Pos) *tracer {
	if !r.opts[optXTrace] {
		return nil
	}

	out := r.xtraceOutput()
	if r.stmtTraceOutput != nil {
		out = r.stmtTraceOutput
	}
	out = r.bashPPWriter(out)
	return &tracer{
		printer:   syntax.NewPrinter(),
		output:    out,
		needsPlus: true,
		plus:      r.xtracePrefix(pos),
	}
}

func (r *Runner) xtraceOutput() io.Writer {
	out := r.stderr
	// Honor BASH_XTRACEFD — if it's set to a numeric fd that is
	// currently open, route xtrace output there instead of stderr.
	if s := r.envGet("BASH_XTRACEFD"); s != "" {
		if fd, err := strconv.Atoi(s); err == nil {
			if w := r.xtraceSink(fd); w != nil {
				out = w
			}
		}
	}
	return out
}

// xtraceSink resolves an fd number to the writer BASH_XTRACEFD should
// send xtrace to. Bash honors any open descriptor: one the script opened
// itself (`exec 7>file`, tracked in the fd tables) as well as one merely
// inherited from the parent (`BASH_XTRACEFD=7 sh -x … 7>file`), so we
// look past r.fdTable to r.fdWriteTable and, finally, an inherited fd.
func (r *Runner) xtraceSink(fd int) io.Writer {
	if f, ok := r.fdTable[fd]; ok && f != nil {
		return f
	}
	if w, ok := r.fdWriteTable[fd]; ok && w != nil {
		return w
	}
	if f, ok := r.inheritedFd(fd); ok {
		return f
	}
	return nil
}

// string writes s to tracer.buf if tracer is non-nil,
// prepending the PS4 prefix if tracer.needsPlus is true.
func (t *tracer) string(s string) {
	if t == nil {
		return
	}

	if t.needsPlus {
		t.buf.WriteString(t.plus)
	}
	t.needsPlus = false
	t.buf.WriteString(s)
}

func (t *tracer) stringf(f string, a ...any) {
	if t == nil {
		return
	}

	t.string(fmt.Sprintf(f, a...))
}

// expr prints x to tracer.buf if tracer is non-nil,
// prepending "+" if tracer.isFirstPrint is true.
func (t *tracer) expr(x syntax.Node) {
	if t == nil {
		return
	}

	if t.needsPlus {
		t.buf.WriteString(t.plus)
	}
	t.needsPlus = false
	if err := t.printer.Print(&t.buf, x); err != nil {
		panic(err)
	}
}

// xtracePrefix expands PS4 for an xtrace line of a command at pos. The
// first character of the expanded value is replicated once per level of
// indirection (bash: `+`, `++` inside a trap), and $LINENO reflects the
// traced command's source line.
func (r *Runner) xtracePrefix(pos syntax.Pos) string {
	ps4 := r.envGet("PS4")
	if ps4 == "" {
		ps4 = "+ "
	}
	depth := r.xtraceLevel + 1
	word, err := syntax.NewParser().Document(strings.NewReader(ps4))
	if err != nil {
		return ps4
	}
	line := int(pos.Line())
	if r.handlingTrap && r.ecfg.OverrideLineno > 0 {
		line = r.ecfg.OverrideLineno
	}
	prev := r.ecfg.OverrideLineno
	r.ecfg.OverrideLineno = line
	expanded, err := expand.Document(r.ecfg, word)
	r.ecfg.OverrideLineno = prev
	if err != nil || expanded == "" {
		return ps4
	}
	_, sz := utf8.DecodeRuneInString(expanded)
	return strings.Repeat(expanded[:sz], depth) + expanded[sz:]
}

// flush writes the contents of tracer.buf to the tracer.stdout.
func (t *tracer) flush() {
	if t == nil {
		return
	}

	t.output.Write(t.buf.Bytes())
	t.buf.Reset()
}

// newLineFlush is like flush, but with extra new line before tracer.buf gets flushed.
func (t *tracer) newLineFlush() {
	if t == nil {
		return
	}

	t.buf.WriteString("\n")
	t.flush()
	// reset state
	t.needsPlus = true
}

// call prints a command and its arguments with varying formats depending on the cmd type,
// for example, built-in command's arguments are printed enclosed in single quotes,
// otherwise, call defaults to printing with double quotes.
func (t *tracer) call(cmd string, args ...string) {
	if t == nil {
		return
	}

	s := strings.Join(args, " ")
	if strings.TrimSpace(s) == "" {
		// fields may be empty for function () {} declarations
		t.string(cmd)
	} else if IsBuiltin(cmd) {
		// bash quotes each arg separately in xtrace output:
		// `+ : '|' '&' ';'` rather than `: $'| & ;'`. Match that
		// by Quote-ing element-by-element.
		parts := make([]string, len(args))
		for i, a := range args {
			q, err := syntax.Quote(a, syntax.LangBash)
			if err != nil { // should never happen
				panic(err)
			}
			parts[i] = q
		}
		t.stringf("%s %s", cmd, strings.Join(parts, " "))
	} else {
		t.stringf("%s %s", cmd, s)
	}
}

func (r *Runner) traceTestClause(pos syntax.Pos, expr syntax.TestExpr) {
	trace := r.tracer(pos)
	if trace == nil {
		return
	}
	r.traceTestExpr(trace, expr)
}

func (r *Runner) traceTestExpr(trace *tracer, expr syntax.TestExpr) {
	switch x := expr.(type) {
	case *syntax.BinaryTest:
		switch x.Op {
		case syntax.AndTest, syntax.OrTest:
			r.traceTestExpr(trace, x.X)
			r.traceTestExpr(trace, x.Y)
			return
		}
	}
	trace.string("[[ ")
	trace.string(r.traceTestExprString(expr))
	trace.string(" ]]")
	trace.newLineFlush()
}

func (r *Runner) traceTestExprString(expr syntax.TestExpr) string {
	switch x := expr.(type) {
	case *syntax.Word:
		return xtraceQuote(r.literal(x))
	case *syntax.ParenTest:
		return "( " + r.traceTestExprString(x.X) + " )"
	case *syntax.UnaryTest:
		return x.Op.String() + " " + r.traceTestExprString(x.X)
	case *syntax.BinaryTest:
		if x.Op == syntax.AndTest || x.Op == syntax.OrTest {
			return r.traceTestExprString(x.X) + " " + x.Op.String() + " " + r.traceTestExprString(x.Y)
		}
		return r.traceTestExprString(x.X) + " " + x.Op.String() + " " + r.traceTestExprString(x.Y)
	}
	return ""
}
