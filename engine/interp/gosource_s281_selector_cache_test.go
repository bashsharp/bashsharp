//go:build full

package interp_test

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// Performance guard for the memoized Go selector resolution
// ([Runner.bashPPResolveSelectionIn]). Interpreting a struct-heavy Go program
// — the shape cmd/compile/internal/inline/inlheur stresses, where wide
// property and call-site structs are read in tight loops — used to re-run the
// breadth-first field/method lookup on every selector evaluation, reallocating
// edge slices, ancestor maps and struct-field views and rescanning every field
// each time. The lookup is a pure function of the runner's static type/method
// tables, so it is now cached per resolved type node.
//
// The guard is a scaling test: a struct whose field count grows is read a
// fixed number of times, so the marginal per-field-count cost of a selector
// read is what varies. Without the cache the read walks every field and the
// total time grows steeply with the field count; with it the per-read cost is
// amortized O(1) and the total stays within a small constant factor.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func itoaSelCache(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// genWideFieldRead builds a program with a struct of numFields int fields whose
// last field is read iters times in a hot loop. Each read resolves the field
// selector; unmemoized, that resolution walks every field.
func genWideFieldRead(numFields, iters int) string {
	var sb strings.Builder
	sb.WriteString("package main\n\nimport \"fmt\"\n\ntype wide struct {\n")
	for i := 0; i < numFields; i++ {
		sb.WriteString("\tf")
		sb.WriteString(itoaSelCache(i))
		sb.WriteString(" int\n")
	}
	last := itoaSelCache(numFields - 1)
	sb.WriteString("}\n\nfunc main() {\n\tw := wide{}\n\tw.f")
	sb.WriteString(last)
	sb.WriteString(" = 1\n\tsum := 0\n\tfor i := 0; i < ")
	sb.WriteString(itoaSelCache(iters))
	sb.WriteString("; i++ {\n\t\tsum += w.f")
	sb.WriteString(last)
	sb.WriteString("\n\t}\n\tfmt.Println(sum)\n}\n")
	return sb.String()
}

func runSelCacheProgram(tb testing.TB, src string) (time.Duration, string) {
	tb.Helper()
	program, err := gosource.Parse(strings.NewReader(src), "selcache.go", gosource.Options{RunMain: true})
	if err != nil {
		tb.Fatalf("parse: %v", err)
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(tb.TempDir()), interp.StdIO(nil, &out, &errout))
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	start := time.Now()
	err = runner.Run(ctx, program.File)
	d := time.Since(start)
	if err != nil || errout.Len() > 0 {
		tb.Fatalf("err=%v stderr=%s", err, errout.String())
	}
	return d, strings.TrimSpace(out.String())
}

func TestSelectorResolutionScales(t *testing.T) {
	const iters = 40000
	const narrow, wide = 4, 1024

	// A fixed field-read workload over a narrow and a very wide struct. Both
	// run the same number of selector reads; only the field count differs.
	narrowDur, narrowOut := runSelCacheProgram(t, genWideFieldRead(narrow, iters))
	wideDur, wideOut := runSelCacheProgram(t, genWideFieldRead(wide, iters))
	if narrowOut != itoaSelCache(iters) || wideOut != itoaSelCache(iters) {
		t.Fatalf("wrong result: narrow=%q wide=%q want %q", narrowOut, wideOut, itoaSelCache(iters))
	}
	t.Logf("iters=%d narrow(%d fields)=%v wide(%d fields)=%v ratio=%.2f",
		iters, narrow, narrowDur, wide, wideDur, float64(wideDur)/float64(narrowDur))

	// A 256x wider struct must not cost anywhere near 256x. With the selector
	// cache the marginal per-read cost is amortized O(1); measured ratio is
	// ~2.5 with the cache versus ~5 without, so a factor of 4 both fails the
	// unmemoized path and leaves ample headroom for machine and GC noise.
	if ratio := float64(wideDur) / float64(narrowDur); ratio > 4.0 {
		t.Fatalf("selector read cost scales with field count: %.2fx from %d to %d fields (want < 4x)",
			ratio, narrow, wide)
	}
}

// BenchmarkSelectorResolutionWide exercises the cached selector path on a wide
// struct so future regressions in resolution cost surface under `go test
// -bench`. See the scaling guard above for the correctness-of-speed assertion.
func BenchmarkSelectorResolutionWide(b *testing.B) {
	src := genWideFieldRead(512, 20000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, out := runSelCacheProgram(b, src); out == "" {
			b.Fatal("empty output")
		}
	}
}
