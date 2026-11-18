//go:build full

package interp

// Sprint: #319; Story: #1087; Story-ID: 96cd7f0c400d

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// s319GCSyntaxSources returns the compiler syntax package as interpreted
// sources, so its parser and reflection-based dumper both run under Bash#.
func s319GCSyntaxSources(t *testing.T) []gosource.Source {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "gosource", "internal", "gcsyntax"))
	if err != nil {
		t.Fatal(err)
	}
	var sources []gosource.Source
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		name := filepath.Join("..", "gosource", "internal", "gcsyntax", entry.Name())
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, gosource.Source{Name: name, Data: data})
	}
	return sources
}

func s319RunSyntaxFdump(t *testing.T, parsedSource []byte) {
	t.Helper()
	sources := s319GCSyntaxSources(t)
	main := gosource.Source{Name: "main.go", Data: []byte(fmt.Sprintf(`package main
import (
	"io"
	"strings"
	"test/syntax"
)
func main() {
	ast, first := syntax.Parse(syntax.NewFileBase("small.go"), strings.NewReader(%q), nil, nil, syntax.CheckBranches)
	if first != nil { panic(first) }
	if err := syntax.Fdump(io.Discard, ast); err != nil { panic(err) }
}
`, parsedSource))}
	program, err := gosource.Load([]gosource.Source{main}, gosource.Options{
		RunMain:  true,
		Packages: []gosource.PackageSpec{{Path: "test/syntax", Sources: sources}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestS319SyntaxParseThenFdump(t *testing.T) {
	s319RunSyntaxFdump(t, []byte("package p\nfunc f() { h(a(), b.c, d) }\n"))
}

// SliceExpr.Index is an array of interfaces. Fdump formats the array through
// its variadic args slice, whose refresh must materialize adopted AST handles
// again instead of restoring their interpreter-only cells into the request.
func TestS319SyntaxSliceExprFdump(t *testing.T) {
	s319RunSyntaxFdump(t, []byte("package p\nfunc f() { h(text[1:]) }\n"))
}

// TestS319SyntaxParserFdumpThroughput reduces cmd/compile/internal/syntax's
// TestDump end to end: the interpreted parser builds a package-sized AST and
// the interpreted dumper writes it through fmt.Fprintf(p, format, args...).
// The retained *dumper.Write receiver must stay in the interpreter; one
// dependency round trip per fragment cannot finish this dump within 60s.
func TestS319SyntaxParserFdumpThroughput(t *testing.T) {
	var bridgedWrites atomic.Int64
	bashPPNativeRequestTrace = func(q bashPPBridgeRequest) {
		if q.Selector == "Write" {
			bridgedWrites.Add(1)
		}
	}
	defer func() { bashPPNativeRequestTrace = nil }()
	var parsedSource strings.Builder
	parsedSource.WriteString("package p\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&parsedSource, "func f%d(x int) int { if x > %d { return x + %d }; return x - %d }\n", i, i, i, i)
	}
	s319RunSyntaxFdump(t, []byte(parsedSource.String()))
	if got := bridgedWrites.Load(); got != 0 {
		t.Fatalf("standard output writer crossed the dependency bridge %d times", got)
	}
}

func TestS319ReflectedInterfaceSliceCapacityTail(t *testing.T) {
	source := gosource.Source{Name: "main.go", Data: []byte(`package main
import "reflect"
type E interface{ m() }
type A struct{}
type Root struct{ X []E }
func (*A) m() {}
func main() {
	var xs []E
	for i := 0; i < 3; i++ { xs = append(xs, &A{}) }
	_ = reflect.ValueOf(&Root{X: xs})
}
`)}
	program, err := gosource.Load([]gosource.Source{source}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatal(err)
	}
}

// TestS319ReflectedTraversalDoesNotResnapshotGraph reduces Fdump's hot path:
// a reflect.Value rooted at an interpreted pointer walks a graph using only
// read-only reflection. Each tiny Value operation must not structurally
// snapshot the entire retained graph for pointer writeback.
func TestS319ReflectedTraversalDoesNotResnapshotGraph(t *testing.T) {
	source := gosource.Source{Name: "main.go", Data: []byte(`package main
import (
	"fmt"
	"reflect"
)
type node struct { next *node; value int }
func main() {
	// Arm writeback for an unrelated retained origin first. Traversing root
	// must not sweep it together with the reflected graph.
	var prior int
	if _, err := fmt.Sscan("7", &prior); err != nil || prior != 7 { panic("scan") }
	root := &node{}
	tail := root
	for i := 0; i < 160; i++ {
		tail.next = &node{value: i}
		tail = tail.next
	}
	v := reflect.ValueOf(root)
	count := 0
	for !v.IsNil() {
		count++
		v = v.Elem().Field(0)
	}
	if count != 161 { panic("short traversal") }
}
`)}
	program, err := gosource.Load([]gosource.Source{source}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatal(err)
	}
}

func TestS319NativePointerReadOnlyRequests(t *testing.T) {
	req := bashPPEvalRequest{Imports: map[string]string{"r": "reflect", "f": "fmt"}}
	value := bashPPBridgeValue{Kind: "handle", NativeType: "reflect.Value"}
	typ := bashPPBridgeValue{Kind: "handle", Type: "reflect.Type"}
	for _, tc := range []struct {
		name string
		q    bashPPBridgeRequest
		want bool
	}{
		{"value-of", bashPPBridgeRequest{Op: "call", Selector: "r.ValueOf"}, true},
		{"value-field", bashPPBridgeRequest{Op: "call", Selector: "Field", Receiver: &value}, true},
		{"type-field", bashPPBridgeRequest{Op: "call", Selector: "Field", Receiver: &typ}, true},
		{"format", bashPPBridgeRequest{Op: "call", Selector: "f.Fprintf"}, true},
		{"value-set", bashPPBridgeRequest{Op: "call", Selector: "Set", Receiver: &value}, false},
		{"value-call", bashPPBridgeRequest{Op: "call", Selector: "Call", Receiver: &value}, false},
		{"ordinary-call", bashPPBridgeRequest{Op: "call", Selector: "dep.Mutate"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativePointerReadOnlyRequest(req, tc.q); got != tc.want {
				t.Fatalf("nativePointerReadOnlyRequest() = %v, want %v", got, tc.want)
			}
		})
	}
}
