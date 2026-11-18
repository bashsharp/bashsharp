// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

const bashPPEmbeddedStructSource = "type Inner struct { N int }\ntype Other struct { S string }\ntype Reader interface { Read() }\ntype Outer struct { Inner; *Other; Reader; Label string }\n"

func TestBashPPEmbeddedStructStreamingASTPrintWalk(t *testing.T) {
	parse := func(rd io.Reader) *File {
		file, err := NewParser(Variant(LangBashPP)).Parse(rd, "embed.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	buffered := parse(strings.NewReader(bashPPEmbeddedStructSource))
	streamed := parse(iotest.OneByteReader(strings.NewReader(bashPPEmbeddedStructSource)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatal("buffered and one-byte embedded struct trees differ")
	}
	outer := buffered.Stmts[3].Cmd.(*BashPPDecl).DeclTypeExpr.(*BashPPStructType)
	if len(outer.Fields) != 4 || !outer.Fields[0].Embedded || !outer.Fields[1].Embedded || !outer.Fields[2].Embedded || outer.Fields[3].Embedded {
		t.Fatalf("embedded flags = %#v", outer.Fields)
	}
	seenEmbedded := 0
	Walk(buffered, func(node Node) bool {
		if field, ok := node.(*BashPPField); ok && field.Embedded {
			seenEmbedded++
		}
		return true
	})
	if seenEmbedded != 3 {
		t.Fatalf("Walk saw %d embedded fields", seenEmbedded)
	}
	var first strings.Builder
	if err := NewPrinter().Print(&first, buffered); err != nil {
		t.Fatal(err)
	}
	reparsed := parse(strings.NewReader(first.String()))
	var second strings.Builder
	_ = NewPrinter().Print(&second, reparsed)
	if first.String() != second.String() {
		t.Fatalf("unstable print: %q then %q", first.String(), second.String())
	}
}

func TestBashPPEmbeddedStructClassicPOSIXIsolation(t *testing.T) {
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		file, _ := NewParser(Variant(lang)).Parse(strings.NewReader(bashPPEmbeddedStructSource), "embed.sh")
		if file == nil {
			continue
		}
		Walk(file, func(node Node) bool {
			if field, ok := node.(*BashPPField); ok && field.Embedded {
				t.Fatalf("%v produced embedded Bash++ field", lang)
			}
			return true
		})
	}
}
