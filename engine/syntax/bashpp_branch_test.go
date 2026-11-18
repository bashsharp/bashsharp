// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

const bashppBranchFixture = `func control(ch) {
	for i := 0; i < 3; i++ {
		switch i {
		case 0:
			continue
		case 1:
			fallthrough
		default:
			break
		}
		break
	}
	for v := range ch {
		continue
	}
	select {
	default:
		break
	}
}
`

func parseBashPPBranch(t *testing.T, rd io.Reader) *File {
	t.Helper()
	f, err := NewParser(Variant(LangBashPP)).Parse(rd, "branch.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestBashPPBranchPositionsWalkPrintAndStreaming(t *testing.T) {
	buffered := parseBashPPBranch(t, strings.NewReader(bashppBranchFixture))
	streamed := parseBashPPBranch(t, iotest.OneByteReader(strings.NewReader(bashppBranchFixture)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatalf("buffered and one-byte trees differ:\n%#v\n%#v", buffered, streamed)
	}
	var branches []*BashPPBranch
	Walk(buffered, func(n Node) bool {
		if branch, ok := n.(*BashPPBranch); ok {
			branches = append(branches, branch)
		}
		return true
	})
	wantKinds := []string{"continue", "fallthrough", "break", "break", "continue", "break"}
	if len(branches) != len(wantKinds) {
		t.Fatalf("branches = %#v, want %v", branches, wantKinds)
	}
	searchFrom := 0
	for i, branch := range branches {
		if branch.Kw.Value != wantKinds[i] {
			t.Errorf("branch %d = %q, want %q", i, branch.Kw.Value, wantKinds[i])
		}
		off := strings.Index(bashppBranchFixture[searchFrom:], wantKinds[i])
		if off < 0 {
			t.Fatalf("fixture lacks branch %q", wantKinds[i])
		}
		off += searchFrom
		if branch.Pos().Offset() != uint(off) || branch.End().Offset() != uint(off+len(wantKinds[i])) {
			t.Errorf("branch %d positions = %d..%d, want %d..%d", i, branch.Pos().Offset(), branch.End().Offset(), off, off+len(wantKinds[i]))
		}
		searchFrom = off + len(wantKinds[i])
	}
	var printed bytes.Buffer
	if err := NewPrinter().Print(&printed, buffered); err != nil {
		t.Fatal(err)
	}
	if printed.String() != bashppBranchFixture {
		t.Fatalf("print = %q, want %q", printed.String(), bashppBranchFixture)
	}
	reparsed := parseBashPPBranch(t, strings.NewReader(printed.String()))
	if !reflect.DeepEqual(buffered, reparsed) {
		t.Fatal("parse/print/reparse changed tree")
	}
}

func TestBashPPBranchStableDiagnostics(t *testing.T) {
	for _, test := range []struct{ src, want string }{
		{"func f() { switch { default: continue } }", "bash++ continue is not inside a typed for or range loop"},
		{"func f() { for { fallthrough } }", "bash++ fallthrough is not inside an expression switch clause"},
		{"func f() { switch { case true: fallthrough; echo no; default: } }", "bash++ fallthrough must be the final non-empty statement of a switch clause"},
		{"func f() { switch { case true: fallthrough } }", "bash++ fallthrough cannot appear in the final switch clause"},
		{"func f() { switch { case true: if true { fallthrough }; default: } }", "bash++ fallthrough must be the final non-empty statement of a switch clause"},
	} {
		parseErr := func(rd io.Reader) string {
			_, err := NewParser(Variant(LangBashPP)).Parse(rd, "bad.bpp")
			if err == nil {
				t.Fatalf("Parse(%q) succeeded", test.src)
			}
			return err.Error()
		}
		buffered := parseErr(strings.NewReader(test.src))
		streamed := parseErr(iotest.OneByteReader(strings.NewReader(test.src)))
		if buffered != streamed || !strings.Contains(buffered, test.want) {
			t.Errorf("Parse(%q): buffered=%q streamed=%q, want %q", test.src, buffered, streamed, test.want)
		}
	}
}

func TestBashPPBranchPreservesShellCommands(t *testing.T) {
	const top = `break
continue
fallthrough
`
	for _, lang := range []LangVariant{LangBashPP, LangBash, LangPOSIX} {
		f, err := NewParser(Variant(lang)).Parse(strings.NewReader(top), "")
		if err != nil {
			t.Fatalf("%v: %v", lang, err)
		}
		Walk(f, func(n Node) bool {
			if _, ok := n.(*BashPPBranch); ok {
				t.Fatalf("%v produced a typed branch in top-level compatibility input", lang)
			}
			return true
		})
	}

	const typed = `func f() {
	break
	continue
	fallthrough
	for { break 2; continue 2 }
	for { nested := func() { break; continue; fallthrough }; break }
	for x in a; do break; continue 2; done
	case x in x) break;; esac
}
`
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(typed), "")
	if err != nil {
		t.Fatal(err)
	}
	var typedBranches int
	Walk(f, func(n Node) bool {
		if _, ok := n.(*BashPPBranch); ok {
			typedBranches++
		}
		return true
	})
	if typedBranches != 1 {
		t.Fatalf("typed branches = %d, want only the outer loop's final break", typedBranches)
	}
}
