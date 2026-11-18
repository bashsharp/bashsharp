package lower_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// Sprint 270 (G5): gc reports an operation — a bounds or nil check, an
// inlining or escape note, a liveness or stack-object note, the assembly of
// a case comparison — on the line the line directive in effect names. The
// generated file must therefore keep every operand on its source line even
// where it lays the source out differently: an if/switch initializer stays
// in the statement header, each case clause is positioned at its case
// keyword, an operand written on a later line of a multi-line expression
// carries an inline /*line*/ directive, and so does the '[' or selector of
// an index/selector operation written on a later line than its operand.
//
// The reproducer gives every source line its own identifiers; the check is
// that each identifier of the generated file resolves (through the emitted
// directives, as go/scanner and gc resolve them) to a source line that
// contains it. The one-line spellings are the negatives: they must stay free
// of inline directives.
func TestGoSourceExpressionLines(t *testing.T) {
	src := `package main

func f(xa []int, yb []int, ic int, sd string, ae any) int {
	if vf := func() int {
		return 1
	}(); vf != 1 &&
		xa[ic] ==
			yb[ic] {
		return 0
	}
	switch sd {
	case "ga":
		return 1
	case "hb":
		return 2
	}
	xa[ic],
		yb[ic] =
		yb[ic],
		xa[ic]
	if _, ok := ae.(int); ok {
		return 3
	}
	zz := map[string][]int{
		"kk": xa,
	}["kk"][ic]
	return zz
}

func g(xa []int, ic int) int {
	if n := len(xa); n > ic {
		return xa[ic]
	}
	return xa[ic] + xa[0]
}

func main() {
	f(nil, nil, 0, "", nil)
	g(nil, 0)
}
`
	program, err := gosource.Parse(strings.NewReader(src), "lines.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: "lines.go"})
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "generated.go", result.Source, parser.ParseComments)
	if err != nil {
		t.Fatalf("generated Go does not parse: %v\n%s", err, result.Source)
	}
	lines := strings.Split(src, "\n")
	checked := 0
	ast.Inspect(file, func(n ast.Node) bool {
		var name string
		switch x := n.(type) {
		case *ast.Ident:
			name = x.Name
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			name = x.Value
		default:
			return true
		}
		if len(name) != 2 && !(len(name) == 4 && name[0] == '"') {
			return true
		}
		pos := fset.Position(n.Pos())
		if pos.Filename != "lines.go" {
			return true
		}
		if pos.Line < 1 || pos.Line > len(lines) || !strings.Contains(lines[pos.Line-1], name) {
			got := ""
			if pos.Line >= 1 && pos.Line <= len(lines) {
				got = strings.TrimSpace(lines[pos.Line-1])
			}
			t.Errorf("%s at %s: source line is %q\n%s", name, pos, got, result.Source)
		}
		checked++
		return true
	})
	if checked < 30 {
		t.Fatalf("only %d identifiers checked\n%s", checked, result.Source)
	}
	// Negatives: g is written one operand per line already.
	body := result.Source[bytes.Index(result.Source, []byte("func g(")):]
	body = body[:bytes.Index(body, []byte("func main("))]
	if bytes.Contains(body, []byte("/*line")) {
		t.Errorf("one-line expressions gained inline directives:\n%s", body)
	}
	if !bytes.Contains(body, []byte("if n := len(xa); n > ic {")) {
		t.Errorf("if initializer not kept in the header:\n%s", body)
	}
}
