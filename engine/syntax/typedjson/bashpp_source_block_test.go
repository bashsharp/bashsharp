package typedjson_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestSourceBlockRoundTrip(t *testing.T) {
	source := "~~~python as py\ndef value() -> int:\n    return 7\n~~~\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "source.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := typedjson.Encode(&encoded, file); err != nil {
		t.Fatal(err)
	}
	node, err := typedjson.Decode(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	block, ok := node.(*syntax.File).Stmts[0].Cmd.(*syntax.SourceBlock)
	if !ok || block.Language.Value != "python" || block.Alias.Value != "py" || !strings.Contains(block.Body, "def value") {
		t.Fatalf("block = %#v", block)
	}
}
