package typedjson_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPPythonImportRoundTrip(t *testing.T) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`import python[training] "pkg.mod" as py`), "")
	if err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	if err := typedjson.Encode(&first, file); err != nil {
		t.Fatal(err)
	}
	node, err := typedjson.Decode(strings.NewReader(first.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := typedjson.Encode(&second, node); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("typed JSON changed:\n%s\n%s", first.String(), second.String())
	}
	imp := node.(*syntax.File).Stmts[0].Cmd.(*syntax.BashPPImport)
	if imp.Language.Value != "python" || imp.Environment.Value != "training" || imp.Alias.Value != "py" {
		t.Fatalf("decoded import = %#v", imp)
	}
}
