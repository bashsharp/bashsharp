package gosource_test

import (
	"bytes"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
	"strings"
	"testing"
)

func TestGoSimpleInitializerMetadata(t *testing.T) {
	const source = "package main\nfunc touch(){}\nfunc main(){n:=0;if n=1;n==1{};if touch();true{};if n++;n==2{}}\n"
	p, err := gosource.Parse(strings.NewReader(source), "original.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	syntax.Walk(p.File, func(n syntax.Node) bool {
		if i, ok := n.(*syntax.BashPPIf); ok {
			count++
			if i.InitStmt == nil || i.Init != nil || !i.Semicolon.IsValid() {
				t.Fatalf("missing initializer: %#v", i)
			}
			if i.InitStmt.Pos().Line() != 3 {
				t.Fatal("lost position")
			}
		}
		return true
	})
	if count != 3 {
		t.Fatalf("if count %d", count)
	}
	var b bytes.Buffer
	if err := typedjson.Encode(&b, p.File); err != nil {
		t.Fatal(err)
	}
	decoded, err := typedjson.Decode(&b)
	if err != nil {
		t.Fatal(err)
	}
	var printed bytes.Buffer
	if err := syntax.NewPrinter().Print(&printed, decoded); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"if n = 1;", "if touch();", "if n++;"} {
		if !strings.Contains(printed.String(), part) {
			t.Fatalf("missing %s: %s", part, printed.String())
		}
	}
}
func TestRuntimeNumericChangesDoNotAdmitIllegalConstants(t *testing.T) {
	for _, source := range []string{"package main\nfunc main(){println(uint16(4294967295))}", "package main\nfunc main(){println(int(3.9))}", "package main\nfunc main(){const x uint8=255;println(x+1)}"} {
		if _, err := gosource.Parse(strings.NewReader(source), "original.go", gosource.Options{RunMain: true}); err == nil || !strings.Contains(err.Error(), "original.go:2:") {
			t.Fatalf("illegal constant accepted or unpositioned: %v", err)
		}
	}
}
