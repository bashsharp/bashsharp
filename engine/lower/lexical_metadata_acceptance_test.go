//go:build full

package lower_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Lexical scope and assignment-failure atomicity, measured at the boundary the
// compiler actually ships: Compile, an ordinary Go build, the generated source
// removed, and the artifact run with no shell tools on PATH.
//
// Every source is the unchanged public source of an existing interpreter test,
// except one case marked as a new probe. Each case establishes the
// interpreter's exact bytes, exit status and post-failure state FIRST, so a
// compiler difference cannot be read as an interpreter regression and an
// interpreter regression cannot be read as compiler parity.
//
// Unchanged public source: interp/bashpp_scope_test.go,
// TestBashPPFunctionDoesNotSeeLaterDeclaration. A function body sees the names
// visible where it was defined: `before` cannot see the declaration that
// follows it, `after` can.
const lexicalDeclarationSource = `
before() { echo "before=${x-unset}"; }
var x = 1
after() { echo "after=${x-unset}"; }
before
after
`

// Unchanged public source: interp/bashpp_scope_test.go,
// TestBashPPFunctionObservesLaterWritesToCapturedBinding. The other half of the
// same rule: a capture freezes which names are visible, not which values they
// hold, so the function must observe a later write to what it captured.
const lexicalLaterWriteSource = `
var x = 1
peek() { echo "peek=$x"; }
peek
x=42
peek
`

// Unchanged public source: interp/bashpp_story204_residual_test.go,
// TestBashPPTupleAssignFunctionTypeFailureRollsBack. The second result cannot
// be assigned, so neither target may commit.
const tupleFailureSource = "func pair() (int, bool) { return 7, true }\nfunc main() {\nvar x int = 1\nvar y int = 2\nx, y = pair()\nprintf '|%s:%s|' \"$x\" \"$y\"\n}\nmain()\n"

// New probe, not a corpus extract: the same function-result failure with rich
// cells alive around it. A rollback that restored the scalar values but dropped
// the surrounding metadata would still print the two integers, so the channel
// must still hold its buffered element and the closure must still be callable
// after the failed assignment.
const tupleFailureRichCellSource = "func pair() (int, bool) { return 7, true }\nfunc main() {\n pipe := make(chan int, 1)\n pipe <- 1\n keep := func() { printf kept }\n var x int = 2\n var y int = 3\n x, y = pair()\n got := <-pipe\n keep()\n printf '|%s:%s:%s|' \"$got\" \"$x\" \"$y\"\n}\nmain()\n"

// Unchanged public source: interp/bashpp_story204_residual_test.go,
// TestBashPPTupleAssignFunctionResultsPreserveMetadata. Both results are
// pointers, so a tuple assignment that committed values without their cells
// would not survive the dereference.
const pointerResultMetadataSource = `type Box struct { N int }
func rich() (*Box, *Box) {
 p := new(Box)
 q := new(Box)
 p.N = 5
 q.N = 6
 return p, q
}
func main() {
 var p *Box
 var q *Box
 p, q = rich()
 pv := *p
 qv := *q
 printf '%s:%s' pv.N qv.N
}
main()
`

// Unchanged public source: interp/bashpp_story204_residual_test.go,
// TestBashPPTupleAssignNamedRichResultsPreserveMetadata. Named results carry a
// pointer, a struct value and a channel through one tuple assignment.
const namedRichResultMetadataSource = `type Box struct { N int }
type Ch int
func rich() (ptr *Box, value Box, pipe Ch) {
 p := new(Box)
 p.N = 5
 b := Box{N: 6}
 ch := make(chan int, 1)
 ch <- 7
 return p, b, ch
}
func main() {
 var p *Box
 var b Box
 var ch Ch
 p, b, ch = rich()
 pv := *p
 got := <-ch
 printf '%s:%s:%s' pv.N b.N "$got"
}
main()
`

// TestLexicalMetadataAcceptance is the entry the sprint gate runs. Each subtest
// is one source taken through the interpreter and then through the compiler,
// and requires the two to agree on stdout, stderr and exit status.
func TestLexicalMetadataAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name, source, stdout, stderr string
	}{
		// Lexical visibility of a declaration from the function bodies
		// around it.
		{"declaration-visibility", lexicalDeclarationSource, "before=unset\nafter=1\n", ""},
		{"later-writes-to-capture", lexicalLaterWriteSource, "peek=1\npeek=42\n", ""},
		// Value atomicity after a function-result assignment failure.
		{"tuple-failure-values", tupleFailureSource, "|1:2|", "BASHPP-EASSIGN-TYPE: cannot assign bool to int\n"},
		// The same failure with a live channel and a live closure beside the
		// targets: metadata around the rolled-back assignment must survive it.
		{"tuple-failure-rich-cells", tupleFailureRichCellSource, "kept|1:2:3|", "BASHPP-EASSIGN-TYPE: cannot assign bool to int\n"},
		// Metadata carried through a successful tuple assignment.
		{"pointer-result-metadata", pointerResultMetadataSource, "5:6", ""},
		{"named-rich-result-metadata", namedRichResultMetadataSource, "5:6:7", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The interpreter's exact bytes and status first: a compiler
			// difference must not be readable as an interpreter regression.
			out, stderr, status := sourceBaseline(t, tc.source)
			if out != tc.stdout || stderr != tc.stderr || status != 0 {
				t.Fatalf("interpreter stdout=%q stderr=%q status=%d; want %q / %q / 0", out, stderr, status, tc.stdout, tc.stderr)
			}
			result, err := lower.Compile(parse(t, tc.source, "input.bpp"), lower.Options{})
			if err != nil {
				t.Fatalf("Compile rejected a source the interpreter runs: %v", err)
			}
			// runArtifact builds the unit, removes the generated source and
			// runs the binary with no tools on PATH, so what answers is the
			// artifact alone.
			gotOut, gotErr, gotStatus := runArtifact(t, result)
			if gotOut != out || gotErr != stderr || gotStatus != status {
				t.Fatalf("artifact stdout=%q stderr=%q status=%d; interpreter %q / %q / %d", gotOut, gotErr, gotStatus, out, stderr, status)
			}
		})
	}
}

// sourceBaseline runs one program in the source interpreter and returns its
// stdout, stderr and exit status.
func sourceBaseline(t *testing.T, source string) (string, string, int) {
	t.Helper()
	var out, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr), interp.Env(expand.ListEnviron("PATH=/no-tools")))
	if err != nil {
		t.Fatal(err)
	}
	status := 0
	if err := runner.Run(context.Background(), parse(t, source, "input.bpp")); err != nil {
		var exit interp.ExitStatus
		if !errors.As(err, &exit) {
			t.Fatalf("interpreter failed with a non-status error: %v", err)
		}
		status = int(exit)
	}
	return out.String(), stderr.String(), status
}
