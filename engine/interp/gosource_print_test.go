//go:build full

package interp_test

// Sprint: #209; Story: #463; Story-ID: a6f104b906d9
//
// Go's predeclared print/println render their operands as the runtime does;
// the interpreter's cells carry floats as exact rational text and nil
// references as transport values, and the direct and deferred print paths
// each leaked that carrier into the program's output. Every positive case is
// an authored outside-corpus reproducer compared byte for byte with the native
// oracle on both streams; the corpus roots each unblocks are named on it.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGoSourcePrintRenderingMatchesGo(t *testing.T) {
	t.Parallel()
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBinary); err != nil {
		t.Skipf("no Go toolchain: %v", err)
	}
	cases := map[string]string{
		// deferprint.go: a deferred println captured its float operand as
		// the exact rational `3/2` and its typed nil operands as their
		// transport values.
		"deferred_println_operands": `package main

func main() {
	defer println(42, true, false, true, 1.5, "world", (chan int)(nil), []int(nil), (map[string]int)(nil), (func())(nil), byte(255))
	defer println(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20)
	defer print("printing: ")
}
`,
		// The direct path: scalars print at their own width and nil
		// references use Go's nil address forms.
		"direct_println_operands": `package main

type T struct{ a int }

func main() {
	var s []int
	var m map[string]int
	var c chan int
	var fn func()
	var p *T
	var e error
	println(s, m, c, fn, p, e)
	println(1.5, float32(0.1), 1e21, 100.0, 1.0/3.0, 2+3i, complex64(1+2i))
	println((chan int)(nil), []int(nil), (map[string]int)(nil), (func())(nil), (*T)(nil))
	v := 0.0
	v += 0.1
	var w float32 = 0.1
	print(v, " ", w, "\n")
	func() { defer println(v, w, s, m, c, fn, p, e) }()
}
`,
		// fixedbugs/bug364.go: a float64 variable declared from an untyped
		// constant is a float64 — its arithmetic rounds at every step — and
		// it crosses into an interface{} variadic as a float64 rather than
		// as its rational text, whether the call is deferred or not.
		"float_variable_into_interface_variadic": `package main

import "fmt"

var s string

func accum(args ...interface{}) {
	s += fmt.Sprintln(args...)
}

func f() {
	v := 0.0
	for i := 0; i < 3; i++ {
		v += 0.1
		defer accum(v)
	}
}

func main() {
	f()
	print(s)
	s = ""
	x := 0.1
	accum(x, x+0.2, 0.375, 1.5)
	print(s)
	if s != "0.1 0.30000000000000004 0.375 1.5\n" {
		println("BUG:", s)
	}
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			oracle := filepath.Join(dir, "oracle")
			if output, err := exec.Command(goBinary, "build", "-o", oracle, path).CombinedOutput(); err != nil {
				t.Fatalf("oracle build: %v %s", err, output)
			}
			var wantOut, wantErr bytes.Buffer
			native := exec.Command(oracle)
			native.Dir = dir
			native.Stdout, native.Stderr = &wantOut, &wantErr
			if err := native.Run(); err != nil {
				t.Fatalf("oracle run: %v", err)
			}
			gotOut, gotErr, runErr := bashPPRunGoSource(t, dir, path, source)
			if runErr != nil {
				t.Fatalf("Runner: %v; stdout=%q stderr=%q", runErr, gotOut, gotErr)
			}
			if gotOut != wantOut.String() || gotErr != wantErr.String() {
				t.Fatalf("Runner %q/%q; oracle %q/%q", gotOut, gotErr, wantOut.String(), wantErr.String())
			}
		})
	}
}

// Negative controls: a struct operand is still refused — Go rejects it at
// compile time as an illegal print operand — and a scalar that is not a
// float keeps its exact rendering, so the float path did not widen into a
// general fmt-style formatter.
func TestGoSourcePrintRejectsStructOperand(t *testing.T) {
	t.Parallel()
	source := `package main

type T struct{ a int }

func main() {
	v := T{1}
	println("before")
	println(v)
	println("after")
}
`
	dir := t.TempDir()
	_, errOut, runErr := bashPPRunGoSource(t, dir, filepath.Join(dir, "original.go"), source)
	if runErr == nil {
		t.Fatalf("struct operand printed: stderr=%q", errOut)
	}
	if !strings.Contains(errOut, "before\n") || strings.Contains(errOut, "after") {
		t.Fatalf("stderr=%q", errOut)
	}
	source = `package main

func main() {
	big := 1 << 62
	small := uint8(200)
	println(big/2, small+100, "1/2", '1')
	defer println(big, "3/2", 7)
}
`
	_, errOut, runErr = bashPPRunGoSource(t, dir, filepath.Join(dir, "original.go"), source)
	if runErr != nil {
		t.Fatalf("Runner: %v; stderr=%q", runErr, errOut)
	}
	if want := "2305843009213693952 44 1/2 49\n4611686018427387904 3/2 7\n"; errOut != want {
		t.Fatalf("stderr=%q want %q", errOut, want)
	}
}
