package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPGroupedConstantsIotaRepetitionAndTypes(t *testing.T) {
	const src = `type Count int
const (
 A = iota
 B
 C = iota + 3
 D Count = iota
 E
)
func same[T any](a T, b T) { printf 'same:%s:%s\n' "$a" "$b" }
func main() {
 same(D, E)
 printf '%s:%s:%s:%s:%s\n' "$A" "$B" "$C" "$D" "$E"
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	if err != nil || stderr != "" || out != "same:3:4\n0:1:5:3:4\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestBashPPGroupedConstantsDiagnostics(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"typed overflow", "const (\n A int8 = iota + 127\n B\n)\n", "overflows int8"},
		{"non constant", "x := 1\nconst (\n A = x\n)\n", "BASHPP-ECONST-EXPR"},
		{"duplicate", "const (\n A = iota\n A\n)\n", "redeclared"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := runBashSharpCall(t, tc.src)
			if err == nil || !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr=%q err=%v", stderr, err)
			}
		})
	}
}

func TestBashPPGroupedConstantsPositionedDiagnostic(t *testing.T) {
	const src = "const (\n A int8 = iota + 127\n B\n)\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "const.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	r := bashPPRunner(t, &stderr, interp.StdIO(nil, nil, &stderr), interp.Lang(syntax.LangBashPP), interp.WithBashCompatErrors(true))
	if err := r.Run(context.Background(), f); err == nil {
		t.Fatal("overflow unexpectedly succeeded")
	}
	if got := stderr.String(); !strings.Contains(got, "const.bpp: line 3: BASHPP-EEXPR-CONVERT") {
		t.Fatalf("stderr=%q", got)
	}
}

func TestBashPPGroupedConstantsIotaShadowing(t *testing.T) {
	const src = `const (
 iota = iota
 A = iota
)
func main() { printf '%s:%s\n' "$iota" "$A" }
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	if err != nil || stderr != "" || out != "0:0\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestBashPPGroupedConstantsTypedExpressionIdentity(t *testing.T) {
	const src = `type Count int
const (
 A Count = 1
 B = A + 1
)
func same[T any](a T, b T) { printf 'same:%s:%s\n' "$a" "$b" }
func main() {
 same(A, B)
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	if err != nil || stderr != "" || out != "same:1:2\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestBashPPGroupedConstantsBlankSpecsAdvanceIota(t *testing.T) {
	const src = `const (
 _ = iota
 A
 _
 B
)
func main() { printf '%s:%s:%s\n' "$A" "$B" "${_-unbound}" }
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	if err != nil || stderr != "" || out != "1:3:unbound\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestBashPPGroupedConstantsFunctionShadowsIota(t *testing.T) {
	const src = "func iota() {}\nconst (\n A = iota\n)\n"
	_, stderr, err := runBashSharpCall(t, src)
	if err == nil || !strings.Contains(stderr, "BASHPP-ECONST-EXPR") {
		t.Fatalf("stderr=%q err=%v", stderr, err)
	}
}
