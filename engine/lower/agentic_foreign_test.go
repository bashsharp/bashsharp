package lower_test

import (
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
	"testing"
)

func TestAgenticForeignLoweringRefusesByName(t *testing.T) {
	polyglot.RegisterLanguage(polyglot.TextRow("agentic_fixture", nil, polyglot.Text{Type: "agentic_fixture", Verbs: []polyglot.Verb{{Name: "run", Agentic: true}}}))
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("~~~agentic_fixture as searcher\nbinding\n~~~\n"), "oracle.bsh")
	if err != nil {
		t.Fatal(err)
	}
	_, err = lower.Compile(file, lower.Options{})
	if err == nil || !strings.Contains(err.Error(), "searcher.run") || !strings.Contains(err.Error(), "bashy --bashsharp") {
		t.Fatalf("missing named route: %v", err)
	}
}
