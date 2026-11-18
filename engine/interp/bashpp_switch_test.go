// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPSwitchFirstMatchTaglessAndClauseScopes(t *testing.T) {
	const src = `func main() {
	n := 2
	switch x := n + 1; x {
	case 1, 3:
		seen := "first"
		echo "$seen:$x"
	case x:
		echo wrong
	default:
		echo fallback
	}
	echo "after:${x-unset}:${seen-unset}"
	switch {
	case n < 0:
		echo wrong
	case n == 2, true:
		inside := "yes"
		echo "tagless:$inside"
	default:
		echo wrong
	}
	echo "scope:${inside-unset}"
	switch n = n + 1; {
	case n == 3:
		echo "assigned:$n"
	default:
		echo wrong
	}
}
main()
`
	for _, bytewise := range []bool{false, true} {
		var rd interface{ Read([]byte) (int, error) } = strings.NewReader(src)
		if bytewise {
			rd = iotest.OneByteReader(strings.NewReader(src))
		}
		f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(rd, "")
		if err != nil {
			t.Fatalf("bytewise=%v: %v", bytewise, err)
		}
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		if err := r.Run(context.Background(), f); err != nil {
			t.Fatalf("bytewise=%v: run: %v; output: %s", bytewise, err, out.String())
		}
		const want = "first:3\nafter:unset:unset\ntagless:yes\nscope:unset\nassigned:3\n"
		if out.String() != want {
			t.Fatalf("bytewise=%v: output = %q, want %q", bytewise, out.String(), want)
		}
	}
}

func TestBashPPSwitchLazyCaseEvaluation(t *testing.T) {
	const src = `package main

import "fmt"

type terms map[string]int

func (t *terms) nonempty() bool { return len(*t) != 0 }

var calls int

func later() bool {
	calls++
	return true
}

func tag() int {
	calls++
	return 2
}

func laterInt() int {
	calls += 100
	return 3
}

func main() {
	var nilTerms *terms
	switch {
	case true:
		fmt.Println("first")
	case later(), nilTerms.nonempty():
		fmt.Println("wrong")
	}
	fmt.Println("after-first", calls)
	switch tag() {
	default:
		fmt.Println("default")
	case 1, 2, laterInt():
		fmt.Println("matched", calls)
	}
	fmt.Println("after-tag", calls)
}
`
	out, stderr, err := bashPPRunGoSource(t, t.TempDir(), "switch_lazy.go", src)
	if err != nil {
		t.Fatalf("run: %v; stderr: %s; output: %s", err, stderr, out)
	}
	const want = "first\nafter-first 0\nmatched 1\nafter-tag 1\n"
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestBashPPSwitchTypeDiagnostics(t *testing.T) {
	for _, test := range []struct{ src, want string }{
		{"func main() { switch 1 { case \"1\": echo wrong } }\nmain()\n", "BASHPP-ESWITCH-TYPE: case expression type String does not match switch tag type Int\n"},
		{"func main() { switch 1 { case 1: echo matched; case \"later\": echo wrong } }\nmain()\n", "BASHPP-ESWITCH-TYPE: case expression type String does not match switch tag type Int\n"},
		{"func main() { switch { case 1: echo wrong } }\nmain()\n", "BASHPP-ESWITCH-TYPE: tagless switch case must be boolean, got Int\n"},
		{"func main() { switch 1 { case 1: echo first; case 1.0: echo second } }\nmain()\n", "BASHPP-ESWITCH-DUPLICATE: duplicate case constant 1\n"},
	} {
		for _, bytewise := range []bool{false, true} {
			var rd interface{ Read([]byte) (int, error) } = strings.NewReader(test.src)
			if bytewise {
				rd = iotest.OneByteReader(strings.NewReader(test.src))
			}
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(rd, "")
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			err = r.Run(context.Background(), f)
			var status interp.ExitStatus
			if !errors.As(err, &status) || status != 2 || out.String() != test.want {
				t.Errorf("bytewise=%v: Run error/output = %v/%q, want status 2/%q", bytewise, err, out.String(), test.want)
			}
		}
	}
}
