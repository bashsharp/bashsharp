package typedjson_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPScalarCallArgumentsRoundTrip(t *testing.T) {
	const source = "func main() {\n result := true && yes(\"text\", add(1, 2))\n}\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "scalar.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var data strings.Builder
	if err := typedjson.Encode(&data, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data.String(), "ArgExprs") {
		t.Fatal("typed argument edge absent")
	}
	decoded, err := typedjson.Decode(strings.NewReader(data.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(file, decoded) {
		t.Fatal("typed arguments or preserved legacy words changed")
	}
	count := 0
	syntax.Walk(decoded, func(node syntax.Node) bool {
		if _, ok := node.(*syntax.BashPPCall); ok {
			count++
		}
		return true
	})
	if count != 2 {
		t.Fatalf("nested call walked %d times", count)
	}
}
