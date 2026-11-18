package syntax

import (
	"strings"
	"testing"
)

func TestFenceUnderscoreCall(t *testing.T) {
	for _, call := range []string{"ts._load()", "x := ts._load()", "py._load()", "x := py._load()"} {
		t.Run(call, func(t *testing.T) {
			file, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader("~~~python as py\ndef _load() -> int:\n    return 7\n~~~\n~~~typescript as ts\nexport function _load(): number { return 7; }\n~~~\n"+call+"\necho after\n"), "")
			if err != nil {
				t.Fatal(err)
			}
			switch cmd := file.Stmts[2].Cmd.(type) {
			case *BashPPCall:
			case *BashPPShortDecl:
				if cmd.Call == nil {
					t.Fatalf("missing call: %#v", cmd)
				}
			default:
				t.Fatalf("parsed %T, want a Bash# call", cmd)
			}
			if len(file.Stmts) != 4 {
				t.Fatalf("got %d statements, want 4", len(file.Stmts))
			}
		})
	}
}
