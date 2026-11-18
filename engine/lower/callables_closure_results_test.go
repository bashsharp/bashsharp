//go:build full

package lower_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/lower"
)

// A runtime-backed unit has two spellings for one `func` result: the private
// half hands back a closure invoked with the caller's Program, the public
// wrapper hands back the native signature source wrote. These sources exercise
// the pair end to end — the interpreter establishes the expected bytes and the
// built artifact must reproduce them, so a compiler-side ABI slip cannot be
// mistaken for agreement.
func TestReturnedCallableRuntimeArtifact(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		// One factory, two live closures: each keeps the base its own
		// invocation captured, and both are reached through in-unit call sites
		// that forward the caller's Program.
		{"factory-under-runtime", `func mk(n int) func {
 return func(add int) int { return $((n + add)) }
}
agentic { echo ready; }
a := mk(1)
b := mk(2)
x := a(10)
y := b(10)
echo "$x $y"
`, "ready\n11 12\n"},
		// The escaped callable mutates state its factory declared, so the
		// captured cell — not a copy — has to survive the return.
		// The same creation-in-a-scope shape the host case measures, but wholly
		// in-unit, so the interpreter is the oracle for it: a callable created
		// inside an agentic scope does ordinary work when invoked outside that
		// scope, and reaches a marked action only where it opens an explicit
		// scope of its own. execute compares status, stdout and stderr.
		{"agentic-scope-capture", `agentic func assist() int {
 return 3
}
func plain() int {
 return 4
}
func unmarked() func {
 agentic {
  return func() int { return plain() }
 }
}
func rescoped() func {
 agentic {
  return func() int {
   agentic {
    n := assist()
    return n
   }
  }
 }
}
func main() {
 u := unmarked()
 a := u()
 r := rescoped()
 c := r()
 echo "$a $c"
}
main()
`, "4 3\n"},
		{"counter-under-runtime", `func counter() func {
 var n = 0
 return func() int {
  n=$((n + 1))
  return n
 }
}
func main() {
 ch := make(chan int, 1)
 ch <- 5
 v := <-ch
 next := counter()
 p := next()
 q := next()
 echo "$p $q $v"
}
main()
`, "1 2 5\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errout := execute(t, compile(t, tc.source))
			if out != tc.want || errout != "" {
				t.Fatalf("artifact stdout=%q stderr=%q; want %q and empty", out, errout, tc.want)
			}
		})
	}
}

// closureHostSource is compiled as an importable package: every declaration
// below is reached by an ordinary Go caller through the public wrapper, never
// through a source interpreter. Together they cover what a returned callable
// owes that boundary — a native signature, the state its creating invocation
// captured, the markedness decision, and channel authority that ended with the
// invocation that created it.
const closureHostSource = `func Adder(base int) func {
 return func(step int) int { return $((base + step)) }
}
func Counter() func {
 var n = 0
 return func() int {
  n=$((n + 1))
  return n
 }
}
func Marker() func {
 var marker = 7
 return func() int { return marker }
}
func Sender() func {
 ch := make(chan string, 1)
 var marker = 7
 return func(op int) int {
  if op == 1 {
   ch <- held
  }
  if op == 2 {
   close(ch)
  }
  return marker
 }
}
agentic func Guarded() func {
 return func() int { return 3 }
}
agentic func assist() int {
 return 3
}
func plain() int {
 return 4
}
func Unmarked() func {
 agentic {
  return func() int { return plain() }
 }
}
func Escalating() func {
 agentic {
  return func() int { return assist() }
 }
}
func Rescoped() func {
 agentic {
  return func() int {
   agentic {
    n := assist()
    return n
   }
  }
 }
}
`

// closureHostHarness holds the artifact to the public contract. The typed
// declarations are the signature claim: a wrapper that leaked the private ABI
// would not assign here at all.
const closureHostHarness = `package host_test
import("context";"errors";"testing";rt "github.com/bashsharp/bashsharp/engine/lower/shellrt";"closureacceptance/generated")

func TestReturnedCallableIsNativeAndKeepsItsProgram(t *testing.T){
 var add func(int) int = generated.Adder(10)
 if got:=add(5);got!=15{t.Fatalf("Adder(10)(5)=%d, want 15",got)}
 if got:=add(1);got!=11{t.Fatalf("second call of one callable=%d, want 11",got)}

 // Two invocations of one factory are two owners: neither closure may see the
 // other's captured state.
 first,second:=generated.Counter(),generated.Counter()
 if a,b,c:=first(),first(),second();a!=1||b!=2||c!=1{
  t.Fatalf("independent captures: first=%d,%d second=%d; want 1,2 and 1",a,b,c)
 }
}

// A marked callable an unmarked native caller reached is refused, and a
// refused entry has no callable to hand back.
func TestMarkedReturnedCallableIsRefused(t *testing.T){
 if guarded:=generated.Guarded();guarded!=nil{
  t.Fatalf("refused marked entry returned a live callable %p",guarded)
 }
}

// The channel a public invocation created belongs to that invocation. That
// invocation has finished, so the escaped callable's channel operations are
// refused rather than served by some revived program — while the non-channel
// state the same closure captured is still exactly what it captured.
//
// The two refusals are distinct halves of one shutdown, in the order Run
// performs it: the owner's execution is cancelled first, which is what an
// operation that waits on the context reports, and only then is channel
// authority revoked, which is what an operation that consults the scope alone
// reports. Both name the creating owner; neither is a fresh program.
func TestEscapedCallableLosesChannelButKeepsLexicalState(t *testing.T){
 held:=generated.Sender()
 if got:=held(0);got!=7{t.Fatalf("retained marker=%d, want 7",got)}
 if abort:=abortOf(t,held,1);!errors.Is(abort.Err,context.Canceled){
  t.Fatalf("send abort = %v, want %v",abort.Err,context.Canceled)
 }
 if abort:=abortOf(t,held,2);!errors.Is(abort.Err,rt.ErrChannelScopeClosed){
  t.Fatalf("close abort = %v, want %v",abort.Err,rt.ErrChannelScopeClosed)
 }
 if got:=held(0);got!=7{t.Fatalf("marker after the refused operations=%d, want 7",got)}
}

// abortOf runs one refused channel operation and returns how it was refused.
func abortOf(t *testing.T,held func(int) int,op int) (abort rt.ChannelAbort) {
 t.Helper()
 defer func(){
  value:=recover()
  if value==nil{t.Fatalf("operation %d on a finished owner was served",op)}
  got,ok:=value.(rt.ChannelAbort)
  if !ok{t.Fatalf("operation %d aborted with %#v, want rt.ChannelAbort",op,value)}
  abort=got
 }()
 held(op)
 return
}

// A callable created inside an agentic scope keeps that scope's lexical
// capture, not its permission. Invoked later by a native caller it is an
// ordinary unmarked invocation: ordinary work runs, a marked action is refused
// exactly as it would be anywhere outside a scope, and that action becomes
// reachable again only where the callable itself opens an explicit one.
// Guarded, above, measures the other direction — a marked factory refused at
// its own entry — and says nothing about what an escaped callable carries.
func TestEscapedCallableDoesNotInheritCreationPermission(t *testing.T){
 var ordinary func() int = generated.Unmarked()
 if got:=ordinary();got!=4{
  t.Fatalf("ordinary work from a callable created in an agentic scope=%d, want 4",got)
 }
 if got:=generated.Escalating()();got!=0{
  t.Fatalf("marked action ran from an escaped callable=%d; creation permission persisted",got)
 }
 if got:=generated.Rescoped()();got!=3{
  t.Fatalf("marked action inside the callable's own agentic scope=%d, want 3",got)
 }
}
`

// TestReturnedCallableHostBoundary compiles the source as an importable
// package and links it into a host that only ever holds native Go values. No
// interpreter, no source file and no runtime type reaches the host's own
// declarations: whatever the wrapper returns has to stand on its own.
func TestReturnedCallableHostBoundary(t *testing.T) {
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBinary); err != nil {
		t.Skipf("no go toolchain at %s: %v", goBinary, err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := lower.Compile(parse(t, closureHostSource, "closures.bpp"), lower.Options{Package: "generated", Entry: "Execute", Origin: "closures.bpp"})
	if err != nil {
		t.Fatalf("compiling returned callables as an importable entry: %v", err)
	}
	module := t.TempDir()
	body := "module closureacceptance\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\n\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + filepath.ToSlash(root) + "\n"
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(module, "generated", "program.go")
	host := filepath.Join(module, "host", "closures_test.go")
	writeAgenticFile(t, generated, string(compiled.Source))
	writeAgenticFile(t, host, closureHostHarness)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	binary := filepath.Join(module, "closures.test")
	build := exec.CommandContext(ctx, goBinary, "test", "-mod=mod", "-race", "-c", "-o", binary, "./host")
	build.Dir = module
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the host: %v\n%s\n--- generated ---\n%s", err, output, compiled.Source)
	}
	// The artifact carries the program; nothing may read the source back.
	for _, path := range []string{generated, host} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	execDir := t.TempDir()
	assertNoSource(t, execDir)
	command := exec.CommandContext(ctx, binary, "-test.v", "-test.timeout=120s")
	command.Dir = execDir
	command.Env = []string{"PATH=/no-tools", "GORACE=halt_on_error=1"}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("public returned-callable contract: %v\n%s", err, output)
	}
	// The host's own report is the evidence: it ran against the binary alone.
	t.Logf("host contract under -race:\n%s", output)
}
