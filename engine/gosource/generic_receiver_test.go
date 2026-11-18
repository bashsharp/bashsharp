package gosource_test

// Sprint: #118; Story: #58; Story-ID: b4186a7bd92a
import (
	"bytes"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestGenericReceiverKeepsDeclarationAndBindings(t *testing.T) {
	const source = `package main
type Pair[A, B any] struct{ first A; second B }
func (p Pair[A, B]) First() A { return p.first }
func (p *Pair[A, B]) Second() B { return p.second }
func main() {}`
	program, err := gosource.Parse(strings.NewReader(source), "unchanged.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	verify := func(node syntax.Node) {
		t.Helper()
		methods := 0
		syntax.Walk(node, func(node syntax.Node) bool {
			decl, ok := node.(*syntax.BashPPFuncDecl)
			if !ok || decl.Receiver == nil {
				return true
			}
			methods++
			if decl.Receiver.RecvType.Value != "Pair" {
				t.Fatalf("receiver declaration name = %q", decl.Receiver.RecvType.Value)
			}
			if len(decl.Receiver.TypeParams) != 2 {
				t.Fatalf("receiver binding count = %d", len(decl.Receiver.TypeParams))
			}
			if got := []string{decl.Receiver.TypeParams[0].Value, decl.Receiver.TypeParams[1].Value}; got[0] != "A" || got[1] != "B" {
				t.Fatalf("receiver bindings = %q", got)
			}
			return true
		})
		if methods != 2 {
			t.Fatalf("methods = %d", methods)
		}
	}
	verify(program.File)
	var wire bytes.Buffer
	if err := typedjson.Encode(&wire, program.File); err != nil {
		t.Fatal(err)
	}
	decoded, err := typedjson.Decode(&wire)
	if err != nil {
		t.Fatal(err)
	}
	verify(decoded)
}
