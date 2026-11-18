package gosource_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// sprint165Diagnostics loads one source under options and returns its
// rendered diagnostics (nil for an accepted program).
func sprint165Diagnostics(t *testing.T, name string, data []byte, options gosource.Options) []string {
	t.Helper()
	_, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, options)
	if err == nil {
		return nil
	}
	list, ok := err.(gosource.ErrorList)
	if !ok {
		t.Fatalf("diagnostics lost types: %T", err)
	}
	out := make([]string, len(list))
	for i, diagnostic := range list {
		out[i] = diagnostic.Error()
	}
	return out
}

func sprint165Read(t *testing.T, mechanism, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "gosource-diag", mechanism, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// checkerTestPolicy is what the go/types and types2 check_test runners pass
// to the check interface (bashpp-tests types-backend hooks).
var checkerTestPolicy = gosource.Options{CheckAfterSyntaxErrors: true, CheckerBranchErrors: true}

// TestSprint165RepeatedParserDiagnostic is an outside-corpus reproducer for
// the typechecker cluster's first cause: under the checker-test policy the
// checker ran on go/parser's unmirrored tree and repeated what gc's parser
// had already diagnosed — a method with no receiver (go/types "method has
// no receiver" at the receiver list, gc's parser at the name), a method with
// several receivers, a receiver written ...T (go/types "invalid syntax
// tree: invalid use of ...") and a malformed literal (go/types "malformed
// constant"). Both check_test runners see each of these once, from their
// own parser, because their checker runs on that parser's recovered tree;
// so does gc (types2 checks gc's tree). The expected list is `go tool
// compile -e` verbatim, and the checker-test policy reports the same rows:
// the parser's first, then the checker's real error at its own column.
func TestSprint165RepeatedParserDiagnostic(t *testing.T) {
	const mechanism = "typechecker/repeated-parser-diagnostic"
	for _, options := range []gosource.Options{{}, checkerTestPolicy} {
		if got := sprint165Diagnostics(t, "positive.go", sprint165Read(t, mechanism, "positive.go"), options); got != nil {
			t.Fatalf("positive control rejected: %q", got)
		}
	}
	want := []string{
		"reject.go:5:9: method has no receiver",
		"reject.go:7:15: method has multiple receivers",
		"reject.go:9:7: invalid use of ...",
		"reject.go:11:13: hexadecimal literal has no digits",
		`reject.go:15:13: cannot use "x" (untyped string constant) as int value in variable declaration`,
	}
	reject := sprint165Read(t, mechanism, "reject.go.src")
	got := sprint165Diagnostics(t, "reject.go", reject, gosource.Options{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gc stderr mismatch\ngot: %q\nwant: %q", got, want)
	}
	policy := sprint165Diagnostics(t, "reject.go", reject, checkerTestPolicy)
	if !reflect.DeepEqual(policy, want) {
		t.Fatalf("checker-test policy repeats or loses a parser diagnostic\ngot: %q\nwant: %q", policy, want)
	}
	negatives := map[string][]string{
		"missing":            got[1:],
		"parser-only":        got[:4],
		"extra":              append(append([]string{}, got...), "reject.go:16:1: extra"),
		"duplicate":          append(append([]string{}, got...), got[4]),
		"repeated-receiver":  append(append([]string{}, got...), "reject.go:5:6: method has no receiver"),
		"repeated-receivers": append(append([]string{}, got...), "reject.go:7:10: method has multiple receivers"),
		"repeated-dots":      append(append([]string{}, got...), "reject.go:9:7: invalid syntax tree: invalid use of ..."),
		"repeated-literal":   append(append([]string{}, got...), "reject.go:11:11: malformed constant: 0x"),
		"wrong-line":         append([]string{strings.Replace(got[0], ":5:", ":4:", 1)}, got[1:]...),
		"wrong-column":       append(append([]string{}, got[:4]...), strings.Replace(got[4], ":15:13:", ":15:5:", 1)),
		"wrong-wording":      append([]string{strings.Replace(got[0], "no receiver", "no receivers", 1)}, got[1:]...),
		"unexpected-success": nil,
	}
	for name, candidate := range negatives {
		if reflect.DeepEqual(candidate, want) {
			t.Fatalf("negative %s was accepted", name)
		}
	}
	// The mirror removes repeats only: a receiver gc accepted keeps its
	// type, so the method on line 13 is declared and its checker rows, if
	// any, would be anchored there.
	for _, row := range policy {
		if strings.HasPrefix(row, "reject.go:13:") {
			t.Fatalf("mirrored receiver lost a declaration: %q", policy)
		}
	}
	if slices.Contains(policy, "reject.go:9:7: invalid syntax tree: invalid use of ...") {
		t.Fatalf("receiver ...T reached go/types: %q", policy)
	}
}

// TestSprint165ParserStream is an outside-corpus reproducer for the
// typechecker cluster's second cause: gc's per-line stderr filter
// (base.ErrorfAt: one syntax error per line, one of multiple equal other
// errors per line) was applied to the parser's diagnostics under the
// checker-test policy too, but the check_test runners' own parsers hand
// every diagnostic to the runner — types2's errh appends each one and
// go/parser runs with AllErrors (issue47996: six syntax errors on one
// line, each with its ERROR comment). gc's stderr keeps the filter; the
// policy reports the parser's whole stream first, in source order, then
// the checker's rows (the real error on line 3 at its own column).
func TestSprint165ParserStream(t *testing.T) {
	const mechanism = "typechecker/parser-stream"
	for _, options := range []gosource.Options{{}, checkerTestPolicy} {
		if got := sprint165Diagnostics(t, "positive.go", sprint165Read(t, mechanism, "positive.go"), options); got != nil {
			t.Fatalf("positive control rejected: %q", got)
		}
	}
	reject := sprint165Read(t, mechanism, "reject.go.src")
	wantGC := []string{
		"reject.go:5:14: hexadecimal literal has no digits",
		"reject.go:7:10: syntax error: missing type constraint",
	}
	got := sprint165Diagnostics(t, "reject.go", reject, gosource.Options{})
	if !reflect.DeepEqual(got, wantGC) {
		t.Fatalf("gc stderr mismatch\ngot: %q\nwant: %q", got, wantGC)
	}
	stream := []string{
		"reject.go:5:14: hexadecimal literal has no digits",
		"reject.go:5:18: hexadecimal literal has no digits",
		"reject.go:7:10: syntax error: missing type constraint",
		"reject.go:7:12: syntax error: unexpected name m, expected (",
		"reject.go:7:15: syntax error: unexpected ), expected type",
		"reject.go:7:17: syntax error: unexpected { in parameter list; possibly missing comma or )",
		"reject.go:7:18: syntax error: unexpected } after top level declaration",
	}
	policy := sprint165Diagnostics(t, "reject.go", reject, checkerTestPolicy)
	if len(policy) < len(stream) || !reflect.DeepEqual(policy[:len(stream)], stream) {
		t.Fatalf("checker-test policy filtered the parser's stream\ngot: %q\nwant prefix: %q", policy, stream)
	}
	const checkerRow = `reject.go:3:13: cannot use "x" (untyped string constant) as int value in variable declaration`
	if !slices.Contains(policy[len(stream):], checkerRow) {
		t.Fatalf("checker-test policy lost the checker's row %q in %q", checkerRow, policy)
	}
	checker := policy[len(stream):]
	negatives := map[string][]string{
		"filtered":           append(append([]string{}, wantGC...), checker...),
		"missing":            policy[1:],
		"extra":              append(append([]string{}, policy...), "reject.go:8:1: extra"),
		"duplicate":          append(append([]string{}, policy...), policy[0]),
		"wrong-line":         append([]string{strings.Replace(policy[0], ":5:", ":4:", 1)}, policy[1:]...),
		"wrong-column":       append([]string{strings.Replace(policy[0], ":5:14:", ":5:12:", 1)}, policy[1:]...),
		"wrong-wording":      append([]string{strings.Replace(policy[0], "has no digits", "has no digit", 1)}, policy[1:]...),
		"unsorted":           append(append(append([]string{}, stream[1:]...), stream[0]), checker...),
		"unexpected-success": nil,
	}
	for name, candidate := range negatives {
		if reflect.DeepEqual(candidate, policy) {
			t.Fatalf("negative %s was accepted", name)
		}
	}
}

// TestSprint165ScannerImage is an outside-corpus reproducer for nul1.go's
// extra row: gc's source layer drops a NUL byte, an invalid UTF-8 byte and
// a byte order mark after the first character (source.go nextch), so its
// scanner tokenises the source without them while positions keep counting
// them; go/scanner keeps them as ILLEGAL tokens, after which go/parser's
// recovery loses the declaration (`var \xc2A int` became an expression and
// go/types reported "undefined: A"). Load now parses the image gc's
// scanner sees and positions the tree in the original file. A token's text
// stays gc's raw segment: `z\xc1\x81w` is one identifier whose name keeps
// the bytes, so `zw` on line 11 is undefined for go/types as it is for
// types2, and the segment runs to the character that ends the token, so
// `q\xff` declares `q\xff` and `q` is undefined. The expected list is
// `go tool compile -e` verbatim; under the checker-test policy the
// parser's stream is unfiltered (5:7 and 9:21 repeat) and the checker's
// rows are the same.
func TestSprint165ScannerImage(t *testing.T) {
	const mechanism = "scanner-image"
	for _, options := range []gosource.Options{{}, checkerTestPolicy} {
		if got := sprint165Diagnostics(t, "positive.go", sprint165Read(t, mechanism, "positive.go"), options); got != nil {
			t.Fatalf("positive control rejected: %q", got)
		}
	}
	reject := sprint165Read(t, mechanism, "reject.go.src")
	want := []string{
		"reject.go:3:26: invalid UTF-8 encoding",
		"reject.go:5:6: invalid UTF-8 encoding",
		"reject.go:7:22: invalid NUL character",
		"reject.go:9:20: invalid UTF-8 encoding",
		"reject.go:11:5: invalid BOM in the middle of the file",
		"reject.go:11:16: undefined: zw",
		"reject.go:13:6: invalid UTF-8 encoding",
		"reject.go:15:9: undefined: q",
		`reject.go:17:13: cannot use "x" (untyped string constant) as int value in variable declaration`,
	}
	got := sprint165Diagnostics(t, "reject.go", reject, gosource.Options{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gc stderr mismatch\ngot: %q\nwant: %q", got, want)
	}
	without := func(i int) []string { return append(append([]string{}, got[:i]...), got[i+1:]...) }
	negatives := map[string][]string{
		"missing":            got[1:],
		"extra":              append(append([]string{}, got...), "reject.go:3:27: undefined: A"),
		"lost-declaration":   append(append([]string{}, got[:5]...), append([]string{"reject.go:3:27: undefined: A"}, got[5:]...)...),
		"split-identifier":   without(5),
		"trailing-byte":      without(7),
		"duplicate":          append(append([]string{}, got...), got[0]),
		"wrong-line":         append([]string{strings.Replace(got[0], ":3:", ":2:", 1)}, got[1:]...),
		"wrong-column":       append(append([]string{}, got[:8]...), strings.Replace(got[8], ":17:13:", ":17:12:", 1)),
		"image-column":       append(append([]string{}, got[:5]...), append([]string{strings.Replace(got[5], ":11:16:", ":11:13:", 1)}, got[6:]...)...),
		"wrong-wording":      append([]string{strings.Replace(got[0], "invalid UTF-8 encoding", "illegal UTF-8 encoding", 1)}, got[1:]...),
		"unexpected-success": nil,
	}
	for name, candidate := range negatives {
		if reflect.DeepEqual(candidate, want) {
			t.Fatalf("negative %s was accepted", name)
		}
	}
	// The policy lists the parser's stream first (unfiltered: 5:7 and
	// 9:21 repeat), then the checker's rows.
	wantPolicy := []string{
		want[0],
		want[1],
		"reject.go:5:7: invalid UTF-8 encoding",
		want[2],
		want[3],
		"reject.go:9:21: invalid UTF-8 encoding",
		want[4],
		want[6],
		want[5],
		want[7],
		want[8],
	}
	if policy := sprint165Diagnostics(t, "reject.go", reject, checkerTestPolicy); !reflect.DeepEqual(policy, wantPolicy) {
		t.Fatalf("checker-test policy mismatch\ngot: %q\nwant: %q", policy, wantPolicy)
	}
	// The image is a parsing aid only: the source identity Load records is
	// the caller's bytes, and a source that drops nothing is parsed as is.
	clean := sprint165Read(t, mechanism, "positive.go")
	prog, err := gosource.Load([]gosource.Source{{Name: "positive.go", Data: clean}}, gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := prog.Sources[0].Size; got != uint(len(clean)) {
		t.Fatalf("source size %d, want %d", got, len(clean))
	}
}

// TestSprint165BlankMethods is an outside-corpus reproducer for blank.go's
// first line: the lowered-name guard (checkLoweredNames) exempted a blank
// function, which the spec says is not declared, but keyed a blank method
// by its receiver and rejected the second `func (T) _()` as "declared
// twice in the lowered file". A blank method is not declared either (spec:
// Method declarations) and cannot be referenced, so any number lower side
// by side; a real duplicate is still the checker's.
func TestSprint165BlankMethods(t *testing.T) {
	const mechanism = "blank-method"
	prog, err := gosource.Load([]gosource.Source{{Name: "positive.go", Data: sprint165Read(t, mechanism, "positive.go")}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("blank methods rejected: %v", err)
	}
	if prog.Main == "" {
		t.Fatal("main not found")
	}
	want := []string{
		"reject.go:7:10: method T.named already declared at reject.go:5:10",
	}
	got := sprint165Diagnostics(t, "reject.go", sprint165Read(t, mechanism, "reject.go.src"), gosource.Options{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("duplicate method\ngot: %q\nwant: %q", got, want)
	}
	for name, candidate := range map[string][]string{
		"unexpected-success": nil,
		"guard-wording":      {"gosource: package-level name T.named declared twice in the lowered file"},
		"wrong-line":         {strings.Replace(want[0], ":7:", ":6:", 1)},
	} {
		if reflect.DeepEqual(candidate, want) {
			t.Fatalf("negative %s was accepted", name)
		}
	}
}

// TestSprint165SelectReceiveParenthesized is an outside-corpus reproducer
// for chan/select3.go's first line ("invalid select receive declaration"):
// a RecvStmt's receive may be parenthesized (`case x, ok := (<-c):`, spec
// RecvExpr; go/types unparens it before its receive check), but the
// converter switched on the parenthesized expression and lowered an
// ordinary short declaration without the receive the select runtime
// requires. Every comm clause now carries its receive whether written
// bare or in parentheses; a parenthesized non-receive is still the
// checker's error.
func TestSprint165SelectReceiveParenthesized(t *testing.T) {
	const mechanism = "select-recv-paren"
	prog, err := gosource.Load([]gosource.Source{{Name: "positive.go", Data: sprint165Read(t, mechanism, "positive.go")}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("parenthesized receives rejected: %v", err)
	}
	var cases []*syntax.BashPPSelectCase
	syntax.Walk(prog.File, func(n syntax.Node) bool {
		if n, ok := n.(*syntax.BashPPSelectCase); ok {
			cases = append(cases, n)
		}
		return true
	})
	if len(cases) != 6 {
		t.Fatalf("got %d select cases, want 6", len(cases))
	}
	// The fixture pairs each bare form with its parenthesized form; both
	// lower to the same statement, printed the same way.
	printed := func(sc *syntax.BashPPSelectCase) string {
		var buf bytes.Buffer
		if err := syntax.NewPrinter().Print(&buf, sc.Comm); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	for i := 0; i < len(cases); i += 2 {
		bare, paren := cases[i], cases[i+1]
		if reflect.TypeOf(bare.Comm) != reflect.TypeOf(paren.Comm) {
			t.Fatalf("case %d: bare %T, parenthesized %T", i/2, bare.Comm, paren.Comm)
		}
		if got, want := printed(paren), printed(bare); got != want {
			t.Fatalf("case %d: parenthesized form lowers to %q, bare to %q", i/2, got, want)
		}
	}
	decl, ok := cases[1].Comm.(*syntax.BashPPShortDecl)
	if !ok || decl.Recv == nil || decl.Expr != nil || len(decl.Rhs) != 0 {
		t.Fatalf("parenthesized short declaration lost its receive: %T", cases[1].Comm)
	}
	want := []string{"reject.go:7:7: select case must be send or receive (possibly with assignment)"}
	got := sprint165Diagnostics(t, "reject.go", sprint165Read(t, mechanism, "reject.go.src"), gosource.Options{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parenthesized non-receive\ngot: %q\nwant: %q", got, want)
	}
	for name, candidate := range map[string][]string{
		"unexpected-success": nil,
		"runtime-wording":    {"invalid select receive declaration"},
		"wrong-column":       {strings.Replace(want[0], ":7:7:", ":7:12:", 1)},
	} {
		if reflect.DeepEqual(candidate, want) {
			t.Fatalf("negative %s was accepted", name)
		}
	}
}
