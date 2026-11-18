package gosource

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"regexp"
	"sort"
	"strings"

	gcsyntax "github.com/bashsharp/bashsharp/engine/gosource/internal/gcsyntax"
)

// gcError is one diagnostic from the vendored gc parser, rendered exactly as
// gc prints it on stderr: the relative position (so //line directives are
// honored, and the column is omitted when a line directive gave none),
// followed by gc's message verbatim, including its "syntax error: " prefix.
type gcError struct {
	pos gcsyntax.Pos
	msg string
}

func (e gcError) Error() string {
	name, line, col := e.pos.RelFilename(), e.pos.RelLine(), e.pos.RelCol()
	if line == 0 {
		return fmt.Sprintf("%s: %s", name, e.msg)
	}
	if col == 0 {
		return fmt.Sprintf("%s:%d: %s", name, line, e.msg)
	}
	return fmt.Sprintf("%s:%d:%d: %s", name, line, col, e.msg)
}

type gcTooManyErrors struct {
	pos string
}

func (e gcTooManyErrors) Error() string {
	return e.pos + ": too many errors"
}

// gcSyntaxVerdict parses src with gc's own parser (the vendored
// cmd/compile/internal/syntax) and returns its diagnostics. Branch checking is
// enabled for gc behavior unless checkerBranchErrors leaves it to go/types.
// Diagnostics are filtered the way gc's base.ErrorfAt filters them before they
// reach stderr (cmd/compile/internal/base/print.go, Go 1.27.0):
//
//	if strings.HasPrefix(msg, "syntax error") {
//		// only one syntax error per line, no matter what error
//		if sameline(lasterror.syntax, pos) { return }
//		lasterror.syntax = pos
//	} else {
//		// only one of multiple equal non-syntax errors per line
//		if sameline(lasterror.other, pos) && msg == lasterror.msg { return }
//		lasterror.other = pos
//		lasterror.msg = msg
//	}
//
// where sameline compares position base and line. Errors are returned in the
// order gc emits them (source order). An empty result is gc accepting the
// file at syntax stage.
//
// The filter is gc's stderr's, not the parser's: with gcStderr false (the
// checker-test policy) every diagnostic the parser emits is returned, as the
// check_test runners' own parsers hand them to the runner (types2's errh
// appends each one; go/parser runs with AllErrors).
func gcSyntaxVerdict(name string, src []byte, checkerBranchErrors, gcStderr bool) (ErrorList, *gcsyntax.File) {
	var out ErrorList
	var lastSyntax, lastOther gcsyntax.Pos
	lastMsg := ""
	sameline := func(a, b gcsyntax.Pos) bool {
		return a.Base() == b.Base() && a.Line() == b.Line()
	}
	errh := func(err error) {
		e, ok := err.(gcsyntax.Error)
		if !ok {
			out = append(out, err)
			return
		}
		if !gcStderr {
			out = append(out, gcError{pos: e.Pos, msg: e.Msg})
			return
		}
		if strings.HasPrefix(e.Msg, "syntax error") {
			if sameline(lastSyntax, e.Pos) {
				return
			}
			lastSyntax = e.Pos
		} else {
			if sameline(lastOther, e.Pos) && e.Msg == lastMsg {
				return
			}
			lastOther, lastMsg = e.Pos, e.Msg
		}
		out = append(out, gcError{pos: e.Pos, msg: e.Msg})
	}
	mode := gcsyntax.CheckBranches
	if checkerBranchErrors {
		mode = 0
	}
	file, _ := gcsyntax.Parse(gcsyntax.NewFileBase(name), bytes.NewReader(src), errh, nil, mode)
	return out, file
}

// syntaxVerdict runs gcSyntaxVerdict over every source. A non-empty result is
// gc's complete syntax-stage diagnostic set; Load decides whether policy stops
// there or continues with go/parser recovery and go/types. An empty result
// means every file passed gc's parser. The trees are gc's own, index-aligned
// with sources (nil where gc produced none); mirrorGCTree reads them.
func syntaxVerdict(sources []Source, checker checkerOptions) (ErrorList, []*gcsyntax.File) {
	var out ErrorList
	files := make([]*gcsyntax.File, len(sources))
	for i, s := range sources {
		var errs ErrorList
		errs, files[i] = gcSyntaxVerdict(s.Name, s.Data, checker.checkerBranchErrors, checker.gcStderr())
		out = append(out, errs...)
	}
	return out, files
}

// goParserSyntaxVerdict is the parser half of the upstream go/types
// check_test runner. Unlike gc, that runner reports go/parser's recovery
// positions before invoking go/types; callers opt into it with the explicit
// GoTypesParserDiagnostics runner fact.
func goParserSyntaxVerdict(sources []Source) ErrorList {
	fset := token.NewFileSet()
	var out ErrorList
	for _, source := range sources {
		_, err := parseGoFile(fset, source.Name, source.Data, parser.ParseComments|parser.AllErrors)
		out = appendDiagnostics(out, err)
	}
	return out
}

// checksAfterSyntaxVerdict reports whether gc type-checks the files after a
// non-empty syntax verdict. gc counts only diagnostics that begin with
// "syntax error" (base.SyntaxErrors, cmd/compile/internal/base/print.go)
// and exits before the checker when there is one
// (cmd/compile/internal/noder/irgen.go checkFiles); every other parser or
// scanner diagnostic — a missing 3-index slice bound, a parenthesized go/defer
// expression, an invalid NUL or UTF-8 byte, a malformed literal — is
// reported and the checker still runs over the tree and reports its own.
func checksAfterSyntaxVerdict(verdict ErrorList) bool {
	for _, err := range verdict {
		if e, ok := err.(gcError); ok && strings.HasPrefix(e.msg, "syntax error") {
			return false
		}
	}
	return true
}

// checkerDiagnostics collects one go/types Check's diagnostics under gc's
// division of labour. gc's parser checks branches (syntax.CheckBranches) and
// types2 is configured with IgnoreBranchErrors, so a label or branch error is
// reported once, by the parser. go/types has no such switch; when the gc
// verdict carried the branch check, the checker's branch diagnostics are
// dropped here. They are recognised structurally, not by wording: go/types
// anchors every one of them (labels.go, stmt.go BranchStmt) at a labeled
// statement's label, a branch statement's keyword or a branch statement's
// label, and nothing else the checker reports is anchored there.
//
// With gcStderr set the collector also renders the checker's diagnostics as
// gc's conf.Error handler does (noder/irgen.go checkFiles): a language
// version error ("requires go1.22 or later") names its cause — the file's
// //go:build version when that is what set the version, otherwise the -lang
// setting — and gc's stderr filter applies as base.ErrorfAt applies it to
// every non-syntax error: only one of multiple equal messages per line is
// printed (the position base and line are compared, so //line directives
// are honored).
//
// A short variable declaration in a for statement's post clause is the one
// diagnostic gc's parser reports under both policies (parser.go header:
// "syntax error: cannot declare in post statement of for loop", at the
// ":=") that go/types reports again: types2 relies on the parser (stmt.go
// ForStmt: "The parser already reported an error") and only marks the
// left-hand side used, go/types has no such parser and reports
// InvalidPostDecl itself (stmt.go ForStmt) before marking the left-hand
// side used. That row is recognised structurally: it is the soft
// diagnostic anchored at the post statement itself, and the checker's
// other diagnostics at that position — the assignment's own, from the
// same left-hand side both checkers evaluate ("non-name a.b on left side
// of :=", "undefined: a") — are not soft.
//
// go/types reports the continuation lines of a message ("\tother declaration
// of x") as separate errors that follow their primary; they stay with it:
// dropped with it, and never compared or sorted on their own.
type checkerDiagnostics struct {
	list                     ErrorList
	dropped                  int
	anchors                  map[token.Pos]bool
	postDecls                map[token.Pos]bool
	goTypesParserDiagnostics bool
	gcStderr                 bool
	fset                     *token.FileSet
	files                    []*ast.File
	info                     *types.Info
	goVersion                string
	lastLine                 token.Position
	lastMsg                  string
	lastDropped              bool
}

func newCheckerDiagnostics(fset *token.FileSet, files []*ast.File, info *types.Info, checker checkerOptions) *checkerDiagnostics {
	d := &checkerDiagnostics{gcStderr: checker.gcStderr(), fset: fset, files: files, info: info, goVersion: checker.goVersion, goTypesParserDiagnostics: checker.goTypesParserDiagnostics, postDecls: map[token.Pos]bool{}}
	if !checker.checkerBranchErrors {
		d.anchors = map[token.Pos]bool{}
	}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ForStmt:
				if post, ok := n.Post.(*ast.AssignStmt); ok && post.Tok == token.DEFINE {
					d.postDecls[post.Pos()] = true
				}
			case *ast.LabeledStmt:
				if d.anchors != nil {
					d.anchors[n.Label.Pos()] = true
				}
			case *ast.BranchStmt:
				if d.anchors != nil {
					d.anchors[n.Pos()] = true
					if n.Label != nil {
						d.anchors[n.Label.Pos()] = true
					}
				}
			}
			return true
		})
	}
	return d
}

// report is the types.Config.Error handler.
func (d *checkerDiagnostics) report(err error) {
	e, ok := err.(types.Error)
	if ok && isContinuation(e) {
		if d.lastDropped {
			d.dropped++
			return
		}
		d.list = append(d.list, err)
		return
	}
	d.lastDropped = false
	if ok && (d.anchors[e.Pos] || (!d.goTypesParserDiagnostics && e.Soft && d.postDecls[e.Pos])) {
		d.dropped++
		d.lastDropped = true
		return
	}
	if ok && d.gcStderr {
		pos := d.fset.Position(e.Pos)
		if pos.Filename == d.lastLine.Filename && pos.Line == d.lastLine.Line && e.Msg == d.lastMsg {
			d.dropped++
			d.lastDropped = true
			return
		}
		d.lastLine, d.lastMsg = pos, e.Msg
		if versionErrorRx.MatchString(e.Msg) {
			if file := d.fileAt(e.Pos); file != nil {
				if fileVersion := d.info.FileVersions[file]; file.GoVersion == fileVersion {
					e.Msg = fmt.Sprintf("%s (file declares //go:build %s)", e.Msg, fileVersion)
				} else {
					e.Msg = fmt.Sprintf("%s (-lang was set to %s; check go.mod)", e.Msg, d.goVersion)
				}
				err = e
			}
		}
	}
	d.list = append(d.list, err)
}

// versionErrorRx is gc's (noder/irgen.go): the checker's language version
// errors, whose cause gc names.
var versionErrorRx = regexp.MustCompile(`requires go[0-9]+\.[0-9]+ or later`)

func (d *checkerDiagnostics) fileAt(pos token.Pos) *ast.File {
	for _, file := range d.files {
		if file.FileStart <= pos && pos <= file.FileEnd {
			return file
		}
	}
	return nil
}

// isContinuation reports whether a go/types error is a continuation line of
// the error reported just before it (go/types errors.go: secondary lines are
// indented with a tab).
func isContinuation(e types.Error) bool {
	return strings.HasPrefix(e.Msg, "\t")
}

// result returns the collected diagnostics. Check's returned first error is
// already reported through Error and is not duplicated; it is added only when
// nothing at all reached the handler.
func (d *checkerDiagnostics) result(first error) ErrorList {
	if first != nil && len(d.list) == 0 && d.dropped == 0 {
		return appendDiagnostics(nil, first)
	}
	return d.list
}

// sortGCStderr orders diagnostics as gc prints them: base.FlushErrors sorts
// stably by position before writing stderr, so the parser's and the checker's
// rows of one file interleave in source order. Files keep their relative
// order (sources, the compiler's operand order); a diagnostic without a
// position sorts first, as an unknown src.XPos does in gc.
func sortGCStderr(fset *token.FileSet, sources []Source, diagnostics ErrorList) ErrorList {
	index := map[string]int{}
	for i, s := range sources {
		index[s.Name] = i + 1
	}
	type key struct{ file, line, col int }
	keyOf := func(err error) key {
		switch e := err.(type) {
		case gcError:
			return key{index[e.pos.FileBase().Filename()], int(e.pos.Line()), int(e.pos.Col())}
		case types.Error:
			p := fset.PositionFor(e.Pos, false)
			return key{index[p.Filename], p.Line, p.Column}
		case *scanner.Error:
			return key{index[e.Pos.Filename], e.Pos.Line, e.Pos.Column}
		case scanner.Error:
			return key{index[e.Pos.Filename], e.Pos.Line, e.Pos.Column}
		}
		return key{}
	}
	// A message and its continuation lines sort as one unit.
	type group struct {
		key  key
		list ErrorList
	}
	var groups []group
	for _, d := range diagnostics {
		if e, ok := d.(types.Error); ok && isContinuation(e) && len(groups) > 0 {
			groups[len(groups)-1].list = append(groups[len(groups)-1].list, d)
			continue
		}
		groups = append(groups, group{keyOf(d), ErrorList{d}})
	}
	sort.SliceStable(groups, func(a, b int) bool {
		ka, kb := groups[a].key, groups[b].key
		if ka.file != kb.file {
			return ka.file < kb.file
		}
		if ka.line != kb.line {
			return ka.line < kb.line
		}
		return ka.col < kb.col
	})
	out := make(ErrorList, 0, len(diagnostics))
	for _, g := range groups {
		out = append(out, g.list...)
	}
	return out
}

func limitGCStderr(fset *token.FileSet, diagnostics ErrorList, limit int) ErrorList {
	if limit <= 0 {
		return diagnostics
	}
	out := make(ErrorList, 0, min(len(diagnostics), limit+1))
	primaries := 0
	lastPrimaryPos := ""
	for _, err := range diagnostics {
		if e, ok := err.(types.Error); ok && isContinuation(e) {
			if primaries <= limit {
				out = append(out, err)
			}
			continue
		}
		if primaries == limit {
			out = append(out, gcTooManyErrors{pos: lastPrimaryPos})
			break
		}
		primaries++
		out = append(out, err)
		lastPrimaryPos = diagnosticPosition(fset, err)
	}
	return out
}

func diagnosticPosition(fset *token.FileSet, err error) string {
	switch e := err.(type) {
	case gcError:
		name, line, col := e.pos.RelFilename(), e.pos.RelLine(), e.pos.RelCol()
		if line == 0 {
			return name
		}
		if col == 0 {
			return fmt.Sprintf("%s:%d", name, line)
		}
		return fmt.Sprintf("%s:%d:%d", name, line, col)
	case types.Error:
		return fset.PositionFor(e.Pos, false).String()
	case *scanner.Error:
		return e.Pos.String()
	case scanner.Error:
		return e.Pos.String()
	}
	return ""
}
