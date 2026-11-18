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

const bashPPUpdateFixture = "func f() {\n\tx += 2\n\tx++\n\ta[i] *= 3\n\ts.N--\n\t*p += 1\n}\n"

func TestBashPPUpdateStreamingWalkPrint(t *testing.T) {
	var trees []*File
	for _, wrap := range []func(io.Reader) io.Reader{
		func(r io.Reader) io.Reader { return r },
		func(r io.Reader) io.Reader { return iotest.OneByteReader(r) },
	} {
		file, err := NewParser(Variant(LangBashPP)).Parse(wrap(strings.NewReader(bashPPUpdateFixture)), "update.bpp")
		if err != nil {
			t.Fatal(err)
		}
		trees = append(trees, file)
		var out strings.Builder
		if err := NewPrinter().Print(&out, file); err != nil {
			t.Fatal(err)
		}
		if out.String() != bashPPUpdateFixture {
			t.Fatalf("print = %q", out.String())
		}
	}
	if !reflect.DeepEqual(trees[0], trees[1]) {
		t.Fatal("buffered and streamed update trees differ")
	}
	seenUpdate, seenInc := 0, 0
	Walk(trees[0], func(node Node) bool {
		switch node.(type) {
		case *BashPPUpdate:
			seenUpdate++
		case *BashPPIncDec:
			seenInc++
		}
		return true
	})
	if seenUpdate != 3 || seenInc != 2 {
		t.Fatalf("walk updates/incdec = %d/%d", seenUpdate, seenInc)
	}
}

func TestBashPPUpdateClassicPOSIXIsolation(t *testing.T) {
	const src = "x += 2\nx++\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		for _, wrap := range []func(io.Reader) io.Reader{
			func(r io.Reader) io.Reader { return r },
			func(r io.Reader) io.Reader { return iotest.OneByteReader(r) },
		} {
			file, err := NewParser(Variant(lang)).Parse(wrap(strings.NewReader(src)), "")
			if err != nil {
				t.Fatal(err)
			}
			Walk(file, func(node Node) bool {
				switch node.(type) {
				case *BashPPUpdate, *BashPPIncDec:
					t.Fatalf("%v claimed Bash++ update", lang)
				}
				return true
			})
		}
	}
}

func TestBashPPCompactIndexedIncDecStreamingPrint(t *testing.T) {
	const src = "func f() {\n\tm[k]++\n}\n"
	parse := func(rd io.Reader) *File {
		file, err := NewParser(Variant(LangBashPP)).Parse(rd, "map-inc.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	buffered := parse(strings.NewReader(src))
	streamed := parse(iotest.OneByteReader(strings.NewReader(src)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatal("buffered and one-byte indexed inc-dec trees differ")
	}
	body := buffered.Stmts[0].Cmd.(*BashPPFuncDecl).Body
	if _, ok := body.Stmts[0].Cmd.(*BashPPIncDec); !ok {
		t.Fatalf("indexed inc-dec parsed as %T", body.Stmts[0].Cmd)
	}
	var printed strings.Builder
	if err := NewPrinter().Print(&printed, buffered); err != nil {
		t.Fatal(err)
	}
	if printed.String() != src {
		t.Fatalf("print = %q, want %q", printed.String(), src)
	}
}
