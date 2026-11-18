package gosource_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
import (
	"bytes"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
	"strings"
	"testing"
)

func TestGenericTypeParametersKeepObjectIdentity(t *testing.T) {
	source := `package main
 type T int
 type List[T any] struct{next *List[T];value T}
 func First[T any](xs []T)T{return xs[0]}
 func main(){var x T;_=x;_=First([]int{1})}`
	result, err := gosource.Parse(strings.NewReader(source), "unchanged.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := typedjson.Encode(&encoded, result.File); err != nil {
		t.Fatal(err)
	}
	file, err := typedjson.Decode(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	parameters, named := 0, 0
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.BashPPTypeParamType:
			if n.Name.Value == "T" {
				parameters++
				if !n.Pos().IsValid() {
					t.Fatal("lost position")
				}
			}
		case *syntax.BashPPNamedType:
			if n.Name.Value == "T" {
				named++
			}
		}
		return true
	})
	if parameters < 4 || named < 1 {
		t.Fatalf("type parameter identity lost or ordinary T conflated: params=%d named=%d", parameters, named)
	}
}
