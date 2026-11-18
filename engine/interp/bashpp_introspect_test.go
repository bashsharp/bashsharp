// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

// B27b (Sprint 221, story c19cb824e6bd): DescribeValue is the engine half of
// `bashy define <var>` — a bounded, read-only report of a live session
// value's Go-spelled type and fields. Source-derived fixtures, pinned
// provenance (full table in plan-b27-timed-introspection.md):
//   - PowerShell/PowerShell tag v7.4.0,
//     src/Microsoft.PowerShell.Commands.Utility/commands/utility/GetMember.cs
//     (MIT): member inspection lists name/type/definition without invoking or
//     mutating the object, describes the runtime object behind a reference,
//     and keeps hidden members hidden by default.
//   - python/cpython tag v3.12.0, Lib/timeit.py (PSF-2.0): observation reads
//     without changing the observed program's behavior.

// describeRunner runs one Bash# script and hands back the runner for
// introspection between runs — the seat `bashy define` occupies.
func describeRunner(t *testing.T, src string) *interp.Runner {
	t.Helper()
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	if stderr := out.String(); stderr != "" {
		t.Fatalf("script output %q; want none", stderr)
	}
	return r
}

// Exact inputs from PowerShell Get-Member.Tests.ps1 at
// 1393d167f54466ec60c547b56f0c4d8326da7dc8 (v7.4.0, MIT), cases
// "Should be able to be called on string objects, ints, arrays, etc" and
// "Should be able to extract a field from string objects, ints, arrays, etc".
// The adapter asserts Bash# type names rather than CLR type names.
func TestDescribeValuePowerShellOriginalInputs(t *testing.T) {
	r := describeRunner(t, `a := 1
b := 1.3
c := false
d := []int{1, 3}
e := "anoeduntodeu"
f := "asntoheusth"
`)
	for name, typ := range map[string]string{"a": "int", "b": "float64", "c": "bool", "d": "[]int", "e": "string", "f": "string"} {
		d, ok := r.DescribeValue(name)
		if !ok || d.Type != typ {
			t.Errorf("%s: %+v found=%v", name, d, ok)
		}
	}
}

func TestDescribeValueNamedOpaqueFields(t *testing.T) {
	r := describeRunner(t, `type Ptr *int
type Secret struct { P Ptr }
var s Secret
`)
	d, ok := r.DescribeValue("s")
	if !ok || len(d.Fields) != 1 {
		t.Fatalf("description: %+v", d)
	}
	for _, f := range d.Fields {
		if !f.Redacted || f.Value != "" {
			t.Errorf("opaque field: %+v", f)
		}
	}
}

const describeSessionSrc = `type Point struct { X int; Y int; secret string; Tags []string }
n := 42
s := "hello"
ok := true
p := Point{X: 1, Y: 2}
ptr := &p
var nilptr *Point
xs := []int{1, 2, 3}
m := make(map[string]int)
m["a"] = 1
ch := make(chan int, 1)
func f(v int) int { return $v }
g := f
var e error
`

func TestDescribeValue(t *testing.T) {
	r := describeRunner(t, describeSessionSrc)
	describe := func(name string) interp.ValueDescription {
		t.Helper()
		d, ok := r.DescribeValue(name)
		qt.Assert(t, qt.IsTrue(ok))
		qt.Assert(t, qt.Equals(d.Name, name))
		return d
	}

	t.Run("scalars", func(t *testing.T) {
		d := describe("n")
		qt.Assert(t, qt.Equals(d.Kind, "scalar"))
		qt.Assert(t, qt.Equals(d.Type, "int"))
		qt.Assert(t, qt.Equals(d.Value, "42"))
		d = describe("s")
		qt.Assert(t, qt.Equals(d.Type, "string"))
		qt.Assert(t, qt.Equals(d.Value, "hello"))
		d = describe("ok")
		qt.Assert(t, qt.Equals(d.Type, "bool"))
		qt.Assert(t, qt.Equals(d.Value, "true"))
	})
	t.Run("record lists declared fields in order", func(t *testing.T) {
		d := describe("p")
		qt.Assert(t, qt.Equals(d.Kind, "record"))
		qt.Assert(t, qt.Equals(d.Type, "Point"))
		qt.Assert(t, qt.Equals(len(d.Fields), 4))
		qt.Assert(t, qt.Equals(d.Fields[0], interp.ValueField{Name: "X", Type: "int", Value: "1"}))
		qt.Assert(t, qt.Equals(d.Fields[1], interp.ValueField{Name: "Y", Type: "int", Value: "2"}))
	})
	t.Run("private field is redacted", func(t *testing.T) {
		d := describe("p")
		qt.Assert(t, qt.Equals(d.Fields[2], interp.ValueField{Name: "secret", Type: "string", Redacted: true}))
	})
	t.Run("composite field renders through its type alone", func(t *testing.T) {
		d := describe("p")
		qt.Assert(t, qt.Equals(d.Fields[3], interp.ValueField{Name: "Tags", Type: "[]string"}))
	})
	t.Run("list", func(t *testing.T) {
		d := describe("xs")
		qt.Assert(t, qt.Equals(d.Kind, "list"))
		qt.Assert(t, qt.Equals(d.Type, "[]int"))
		qt.Assert(t, qt.Equals(d.Len, 3))
	})
	t.Run("map", func(t *testing.T) {
		d := describe("m")
		qt.Assert(t, qt.Equals(d.Kind, "map"))
		qt.Assert(t, qt.Equals(d.Type, "map[string]int"))
		qt.Assert(t, qt.Equals(d.Len, 1))
	})
	t.Run("channel handle is opaque", func(t *testing.T) {
		// The internal capability string must never surface: the handle's
		// identity is the answer, its spelling stays private.
		d := describe("ch")
		qt.Assert(t, qt.Equals(d.Kind, "handle"))
		qt.Assert(t, qt.IsTrue(strings.HasPrefix(d.Type, "chan")))
		qt.Assert(t, qt.IsTrue(d.Redacted))
		qt.Assert(t, qt.Equals(d.Value, ""))
		qt.Assert(t, qt.IsFalse(d.Nil))
	})
	t.Run("function handle is opaque", func(t *testing.T) {
		d := describe("g")
		qt.Assert(t, qt.Equals(d.Kind, "handle"))
		qt.Assert(t, qt.IsTrue(strings.HasPrefix(d.Type, "func")))
		qt.Assert(t, qt.IsTrue(d.Redacted))
		qt.Assert(t, qt.Equals(d.Value, ""))
	})
	t.Run("pointer handle keeps identity private", func(t *testing.T) {
		d := describe("ptr")
		qt.Assert(t, qt.Equals(d.Kind, "handle"))
		qt.Assert(t, qt.Equals(d.Type, "*Point"))
		qt.Assert(t, qt.IsTrue(d.Redacted))
		qt.Assert(t, qt.IsFalse(d.Nil))
	})
	t.Run("nil pointer", func(t *testing.T) {
		d := describe("nilptr")
		qt.Assert(t, qt.Equals(d.Kind, "handle"))
		qt.Assert(t, qt.Equals(d.Type, "*Point"))
		qt.Assert(t, qt.IsTrue(d.Nil))
		qt.Assert(t, qt.IsFalse(d.Redacted))
	})
	t.Run("nil interface", func(t *testing.T) {
		d := describe("e")
		qt.Assert(t, qt.Equals(d.Kind, "nil"))
		qt.Assert(t, qt.IsTrue(d.Nil))
	})
	t.Run("unset name reports false", func(t *testing.T) {
		_, ok := r.DescribeValue("nosuch")
		qt.Assert(t, qt.IsFalse(ok))
	})
}

func TestDescribeValueIsNotAnEvaluator(t *testing.T) {
	// The define surface answers for one plain name; anything shaped like an
	// expression is refused, never evaluated.
	r := describeRunner(t, describeSessionSrc)
	for _, name := range []string{"p.X", "xs[0]", "m[a]", "$(echo n)", "p.secret", "*ptr", "&p", "f()", ""} {
		if _, ok := r.DescribeValue(name); ok {
			t.Fatalf("DescribeValue(%q) answered; want a refusal", name)
		}
	}
}

func TestDescribeValueReadsWithoutChanging(t *testing.T) {
	// Get-Member inspects without invoking or mutating: describing a value
	// twice answers identically, and the session still computes with the
	// original values afterwards.
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, describeSessionSrc)
	first, ok := r.DescribeValue("p")
	qt.Assert(t, qt.IsTrue(ok))
	second, ok := r.DescribeValue("p")
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.DeepEquals(second, first))
	bashPPRun(t, r, "printf 'still X=%s n=%s\\n' p.X \"$n\"\n")
	qt.Assert(t, qt.Equals(out.String(), "still X=1 n=42\n"))
}

func TestDescribeValueHandlerContext(t *testing.T) {
	// The seat `define` actually occupies mid-run: an in-process command
	// reaches the session it runs in through HandlerContext and reads the
	// same answer the Runner gives between runs.
	var got interp.ValueDescription
	var ok bool
	middleware := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) > 1 && args[0] == "define" {
				got, ok = interp.HandlerCtx(ctx).DescribeValue(args[1])
				return nil
			}
			return next(ctx, args)
		}
	}
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.ExecHandlers(middleware))
	bashPPRun(t, r, describeSessionSrc+"define n\n")
	qt.Assert(t, qt.Equals(out.String(), ""))
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.DeepEquals(got, interp.ValueDescription{Name: "n", Type: "int", Kind: "scalar", Value: "42"}))
}

func TestDescribeValueClassicShellVariables(t *testing.T) {
	// A classic variable answers through the same call, so define needs one
	// seam for the whole session.
	r := describeRunner(t, describeSessionSrc+`classic=plain
arr=(one two)
declare -A assoc=([k]=v)
`)
	d, ok := r.DescribeValue("classic")
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.Equals(d.Kind, "scalar"))
	qt.Assert(t, qt.Equals(d.Type, "string"))
	qt.Assert(t, qt.Equals(d.Value, "plain"))
	d, ok = r.DescribeValue("arr")
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.Equals(d.Kind, "list"))
	qt.Assert(t, qt.Equals(d.Type, "[]string"))
	qt.Assert(t, qt.Equals(d.Len, 2))
	d, ok = r.DescribeValue("assoc")
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.Equals(d.Kind, "map"))
	qt.Assert(t, qt.Equals(d.Type, "map[string]string"))
	qt.Assert(t, qt.Equals(d.Len, 1))
}
