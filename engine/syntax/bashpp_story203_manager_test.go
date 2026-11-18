// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"strings"
	"testing"
)

func TestBashPPGenericFuncOneByteMultipleParams(t *testing.T) {
	const src = "func choose[A any, B comparable](a A, b B) B {\n\treturn b\n}\nchoose[string, int](x, 7)\n"
	for _, reader := range []interface{ Read([]byte) (int, error) }{
		strings.NewReader(src),
		funcOneByteReader{strings.NewReader(src)},
	} {
		file, err := NewParser(Variant(LangBashPP)).Parse(reader, "generic.bpp")
		if err != nil {
			t.Fatal(err)
		}
		decl, ok := file.Stmts[0].Cmd.(*BashPPFuncDecl)
		if !ok || len(decl.TypeParams) != 2 {
			t.Fatalf("declaration = %#v", file.Stmts[0].Cmd)
		}
		call, ok := file.Stmts[1].Cmd.(*BashPPCall)
		if !ok || len(call.TypeArgs) != 2 {
			t.Fatalf("call = %#v", file.Stmts[1].Cmd)
		}
	}
}
