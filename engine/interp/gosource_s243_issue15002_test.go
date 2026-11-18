//go:build full

package interp_test

// Sprint: 243; Story: 673; Story-ID: f24307569417

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// TestS243Issue15002Interpreted runs the upstream Go 1.27 root byte-for-byte.
// Its syscall.Mmap result is a two-result dependency call whose slice result is
// subsequently passed through ordinary local calls before the checked bounds
// read. In particular, the recovered bounds panic is part of the root's own
// success condition.
func TestS243Issue15002Interpreted(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue15002.go"))
	if err != nil {
		t.Fatal(err)
	}
	got, stderr, err := runGoSource(t, "issue15002", string(source))
	if err != nil {
		t.Fatalf("run: %v\nstderr:\n%s", err, stderr)
	}
	if got != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q", got, stderr)
	}
}

func TestS243Issue15002Reduction(t *testing.T) {
	const source = `package main
import "fmt"
func test(x []byte) uint16 {
	defer func() {
		r := recover()
		if r == nil { panic("no panic") }
		s := fmt.Sprintf("%s", r)
		if s != "runtime error: index out of range [1] with length 1" { panic(s) }
	}()
	return uint16(x[0]) | uint16(x[1])<<8
}
func main() { test([]byte{0}) }
`
	got, stderr, err := runGoSource(t, "issue15002-reduction", source)
	if err != nil || got != "" || stderr != "" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, got, stderr)
	}
}

func TestS243Issue15002NativeTupleReduction(t *testing.T) {
	const source = `package main
import ("fmt"; "os")
func test(x []byte) uint16 {
	defer func() { r:=recover(); if r==nil { panic("no panic") }; s:=fmt.Sprintf("%s",r); if s!="runtime error: index out of range [1] with length 1" { panic(s) } }()
	return uint16(x[0]) | uint16(x[1])<<8
}
func main(){ if err:=os.WriteFile("x",[]byte{0},0600);err!=nil { panic(err) }; b,err:=os.ReadFile("x");if err!=nil {panic(err)};x:=b[:1];test(x) }
`
	got, stderr, err := runGoSource(t, "issue15002-native-tuple", source)
	if err != nil || got != "" || stderr != "" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, got, stderr)
	}
}

// The multi-result expansion rule applies only when its call is the sole
// argument and the destination accepts every result. These must remain static
// arity errors; the native-access panic repair must not make either accepted.
func TestS243Issue15002TupleArityRejected(t *testing.T) {
	for name, source := range map[string]string{
		"local": `package main
func pair() (int, int) { return 1, 2 }
func one(int) {}
func main() { one(pair()) }
`,
		"native": `package main
import "os"
func one([]byte) {}
func main() { one(os.ReadFile("x")) }
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := gosource.Parse(strings.NewReader(source), name+".go", gosource.Options{RunMain: true}); err == nil {
				t.Fatal("accepted a multi-result argument with incompatible arity")
			}
		})
	}
}
