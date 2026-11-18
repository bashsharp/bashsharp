//go:build full

// Sprint: #209; Story: #464; Story-ID: d993e87f95b7
//
// Focused lowering wave after S209.7, covering three corpus keys:
//
//   - testdir:iota.go (both modes): the converter flattened a multi-name
//     ConstSpec (`abit, amask = 1<<iota, 1<<iota-1`) into one BashPPConstSpec
//     per name with a per-name Iota index, and the lowerer emitted one name
//     per line, so iota advanced per name instead of per spec in both the
//     interpreter and gc.
//   - testdir:noinit.go (compiled): the converter renames each `func init`
//     to a callable and the lowerer wrapped the dispatcher calls in one
//     synthetic `func init`, which gc cannot optimize away, so the main
//     init task kept one live init func.
//   - testdir:range.go (compiled): `for i, x[i] = range y` was split into
//     two sequential assignments, so x[i] observed the new i instead of the
//     value the assignment statement's operand evaluation requires.
package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// compileGoSourceArtifact lowers source through the compiled pipeline —
// gosource.Parse, lower.Compile, a real SDK build — and runs the artifact,
// returning its stdout, stderr and the generated Go text.
func compileGoSourceArtifact(t *testing.T, source string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource rejected the source: %v", err)
	}
	lowered, err := lower.Compile(program.File, lower.Options{Origin: path, Dir: dir})
	if err != nil {
		t.Fatalf("lower rejected the program: %v", err)
	}
	generated := filepath.Join(dir, "generated.go")
	if err := os.WriteFile(generated, lowered.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	artifact := filepath.Join(dir, "compiled")
	sdk := filepath.Join(runtime.GOROOT(), "bin", "go")
	build := exec.CommandContext(ctx, sdk, "build", "-p", "2", "-o", artifact, generated)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s\ngenerated:\n%s", err, out, lowered.Source)
	}
	var out, errout bytes.Buffer
	run := exec.CommandContext(ctx, artifact)
	run.Dir = dir
	run.Stdout, run.Stderr = &out, &errout
	if err := run.Run(); err != nil {
		t.Fatalf("artifact: %v %q %q", err, out.String(), errout.String())
	}
	return out.String(), errout.String(), string(lowered.Source)
}

// Positive: every name of a multi-name ConstSpec shares one iota value —
// the testdir:iota.go shape — in the interpreter, the compiled artifact and
// the native oracle alike.
func TestStory464ConstGroupMultiNameIota(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
const (
	abit, amask = 1 << iota, 1<<iota - 1
	bbit, bmask = 1 << iota, 1<<iota - 1
	cbit, cmask = 1 << iota, 1<<iota - 1
)
func main() { fmt.Println(abit, amask, bbit, bmask, cbit, cmask) }`)
}

// Positive: a function-local const group takes the same one-line-per-spec
// emission through the local declaration regrouping path.
func TestStory464ConstGroupMultiNameIotaLocal(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
func main() {
	const (
		abit, amask = 1 << iota, 1<<iota - 1
		bbit, bmask = 1 << iota, 1<<iota - 1
	)
	fmt.Println(abit, amask, bbit, bmask)
}`)
}

// Negative guard: single-name specs and their implicit repetition keep the
// per-spec iota sequence they always had.
func TestStory464ConstGroupSingleNameRepetitionUnchanged(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
const (
	x = 1 << iota
	y
	z
)
func main() { fmt.Println(x, y, z) }`)
}

// Positive, compiled pipeline: a multi-name spec with no written initializer
// repeats the previous expression list implicitly; the emitted group must
// keep the bare names on one line so gc performs that repetition itself.
func TestStory464ConstGroupMultiNameImplicitRepetitionCompiled(t *testing.T) {
	out, errout, generated := compileGoSourceArtifact(t, `package main
import "fmt"
const (
	a, b = iota, iota * 10
	c, d
)
func main() { fmt.Println(a, b, c, d) }`)
	qt.Assert(t, qt.Equals(errout, ""))
	qt.Assert(t, qt.Equals(out, "0 0 1 10\n"))
	qt.Assert(t, qt.IsTrue(strings.Contains(generated, "c, d\n")), qt.Commentf("generated:\n%s", generated))
}

// Positive: the parallel range assignment evaluates x[i]'s operands before
// either iteration variable is stored — the testdir:range.go shape.
func TestStory464RangeParallelAssignIndexTarget(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
func main() {
	x := []int{10, 20}
	y := []int{99}
	i := 1
	for i, x[i] = range y {
		break
	}
	fmt.Println(i, x[0], x[1])
}`)
}

// Positive: a pointer-indirection value target binds its pointer before the
// key variable changes, like any other non-plain target.
func TestStory464RangeParallelAssignPointerTarget(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
func main() {
	vals := []int{7}
	n := 0
	target := 0
	p := &target
	for n, *p = range vals {
	}
	fmt.Println(n, target)
}`)
}

// Negative guard: all-plain targets keep their existing lowering and
// semantics — assignment left to right of the iteration temporaries.
func TestStory464RangePlainTargetsUnchanged(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
func main() {
	var i, v int
	sum := 0
	for i, v = range []int{3, 4} {
		sum += i + v
	}
	fmt.Println(i, v, sum)
}`)
}

// Negative guard: a single non-plain target never had the ordering defect
// and stays on the one-target path.
func TestStory464RangeSingleIndexTargetUnchanged(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
func main() {
	x := []int{0, 0}
	for x[1] = range []int{5, 6, 7} {
	}
	fmt.Println(x[0], x[1])
}`)
}

// Positive: renamed init functions still run, in declaration order, before
// main, in all three modes.
func TestStory464InitFunctionsRunInOrder(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
func init() { fmt.Println("first") }
func init() { fmt.Println("second") }
func main() { fmt.Println("main") }`)
}

// Positive, compiled pipeline: trivial init functions are emitted back as
// real func init declarations, so gc's dead-init elimination leaves the main
// init task empty — the testdir:noinit.go observation.
func TestStory464TrivialInitFunctionsLeaveNoInitTask(t *testing.T) {
	out, errout, generated := compileGoSourceArtifact(t, `package main
import "unsafe"
var keep unsafe.Pointer
func init() {}
func init() {
	if false {
	}
}
type initTask struct {
	state uint32
	nfns  uint32
}
//go:linkname main_inittask main..inittask
var main_inittask initTask
func main() { println(main_inittask.nfns) }`)
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(errout, "0\n"), qt.Commentf("generated:\n%s", generated))
	qt.Assert(t, qt.IsFalse(strings.Contains(generated, "__gosource_init_")), qt.Commentf("generated:\n%s", generated))
}

// Negative guard: a source function that merely shares the converter's init
// naming is not an init entry — only the synthetic top-level dispatcher call
// marks one — so it keeps its name and its explicit call.
func TestStory464UserFunctionWithConverterInitNameUntouched(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
func __gosource_init_0() { fmt.Println("called") }
func main() { __gosource_init_0() }`)
}
