//go:build full

package interp_test

// Sprint: #243; Story: #674; Story-ID: 63073886bfce
//
// Original Go controls for launching a callee that has no original function
// body: a predeclared builtin, a dependency function or method, a dependency
// function value, a nil function value. Every program here is legal Go and
// is measured against the native oracle; the launch fixes its operands in
// the launching goroutine and runs the call in the task.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
)

func readTaskNativeFixture(t *testing.T, name, want string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "gosource-task-native", name))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
		t.Fatalf("original fixture %s bytes changed: %s", name, got)
	}
	return string(data)
}

// TestGoSourceLaunchOriginalGoTests runs the two Go distribution tests whose
// root cause was the refused launch of a bodiless callee.
func TestGoSourceLaunchOriginalGoTests(t *testing.T) {
	t.Run("goprint", func(t *testing.T) {
		source := readTaskNativeFixture(t, "goprint.go.txt", "707e00f08110b8a9968037634a9da70f19fc2f649dbf68ade6e04cbc70d011bd")
		want := readTaskNativeFixture(t, "goprint.out.txt", "8cf6a0958d362f5632998197339610f83828ea7f1a5a815038cd077fd17f5c27")
		// The toolchain's own expectation first: println writes to stderr,
		// and the eleven operands — constants, typed nils, a byte — render
		// exactly as Go renders them.
		out, errs, err := runGoSource(t, "goprint", source)
		if err != nil || out != "" || errs != want {
			t.Fatalf("goprint: err=%v stdout=%q stderr=%q; want stderr %q", err, out, errs, want)
		}
		typedSendThreeModes(t, source)
	})
	t.Run("issue25897b", func(t *testing.T) {
		source := readTaskNativeFixture(t, "issue25897b.go.txt", "43e7dac9d14271a6782bb5770f8f06b0d98f36ed9d5dc40b5420e8c43212cb50")
		typedSendThreeModes(t, source)
	})
}

// TestGoSourceLaunchBodilessCalleesThreeModes authors the launch shapes one
// at a time. Where a task's effect is observed without synchronization the
// program sleeps well past the launch, as an original Go program would have
// to; every other case is synchronized by the launched call itself.
func TestGoSourceLaunchBodilessCalleesThreeModes(t *testing.T) {
	cases := map[string]string{
		// The launched close is the synchronization: the receive completes
		// only once the task has run it.
		"close_builtin": `package main
import "fmt"
func main(){done:=make(chan bool);go close(done);<-done;fmt.Println("closed")}`,
		// A method on a native receiver: the method value is bound at the
		// launch, and Wait observes the task's Done.
		"waitgroup_done_method_value": `package main
import ("fmt";"sync")
func main(){var wg sync.WaitGroup;wg.Add(1);go wg.Done();wg.Wait();fmt.Println("done")}`,
		"mutex_unlock_method_value": `package main
import ("fmt";"sync")
func main(){var mu sync.Mutex;mu.Lock();go mu.Unlock();mu.Lock();fmt.Println("unlocked")}`,
		// The operand is the VALUE x held at the launch, not the variable:
		// the parent's later assignment is invisible to the task.
		"operands_fixed_at_launch": `package main
import ("fmt";"time")
func main(){x:=1;go println(x);x=2;time.Sleep(300*time.Millisecond);fmt.Println("parent",x)}`,
		// Computed operands run once, in order, in the launching goroutine:
		// calls is already 2 on the statement after the go, whatever the
		// task's schedule.
		"operands_evaluated_once_in_order": `package main
import ("fmt";"time")
var calls int
func next()int{calls++;fmt.Println("next",calls);return calls}
func main(){go println(next(),next());fmt.Println("launched",calls);time.Sleep(300*time.Millisecond)}`,
		// A panic while evaluating an operand is the parent's panic, and no
		// task is ever started.
		"operand_panic_launches_nothing": `package main
import "fmt"
func boom()int{panic("operand")}
func main(){defer func(){fmt.Println("recovered:",recover())}();go println(boom());fmt.Println("unreachable")}`,
		// A print operand that is a reference keeps Go's print form.
		"println_reference_operands": `package main
import ("fmt";"time")
type T struct{n int}
func main(){var p *T;var m map[string]int;var s []int;go println(p==nil,m==nil,len(s));time.Sleep(300*time.Millisecond);fmt.Println("parent")}`,
		"dependency_function": `package main
import ("fmt";"time")
func main(){go fmt.Println("from task");time.Sleep(300*time.Millisecond);fmt.Println("parent")}`,
		"computed_dependency_callee_once": `package main
import ("fmt";"time")
var calls int
func factory()func(...any)(int,error){calls++;return fmt.Println}
func main(){go factory()("computed");time.Sleep(300*time.Millisecond);fmt.Println("calls",calls)}`,
		"dependency_function_value": `package main
import ("fmt";"time")
func main(){f:=fmt.Println;go f("via value");time.Sleep(300*time.Millisecond);fmt.Println("parent")}`,
		// delete and clear address the map the parent holds, as Go's map
		// reference semantics require.
		"delete_builtin_shares_map": `package main
import ("fmt";"time")
func main(){m:=map[string]int{"a":1,"b":2};go delete(m,"a");time.Sleep(300*time.Millisecond);fmt.Println(len(m),m["b"])}`,
		"clear_builtin_shares_map": `package main
import ("fmt";"time")
func main(){m:=map[string]int{"a":1,"b":2};go clear(m);time.Sleep(300*time.Millisecond);fmt.Println(len(m))}`,
		"copy_builtin_shares_backing": `package main
import ("fmt";"time")
func main(){dst:=make([]int,3);go copy(dst,[]int{7,8,9});time.Sleep(300*time.Millisecond);fmt.Println(dst)}`,
		// recover outside a deferred call returns nil and the task completes.
		"recover_launch_is_noop": `package main
import ("fmt";"time")
func main(){go recover();time.Sleep(100*time.Millisecond);fmt.Println("alive")}`,
		// A binding that shadows a predeclared name is an original closure,
		// and its launch keeps the lexical-capture path.
		"shadowed_builtin_is_original": `package main
import ("fmt";"time")
func main(){println:=func(s string){fmt.Println("shadow",s)};go println("x");time.Sleep(300*time.Millisecond);fmt.Println("parent")}`,
		// A deferred launch runs as the frame unwinds; its operands are the
		// values at that point, fixed by the go statement itself.
		"launch_from_deferred_function": `package main
import ("fmt";"time")
func work(done chan bool){defer func(){go close(done)}();fmt.Println("body")}
func main(){done:=make(chan bool);work(done);<-done;time.Sleep(50*time.Millisecond);fmt.Println("closed")}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

// TestGoSourceLaunchPanicsInTask covers the panic controls: a call that
// panics when it runs panics in the task, after the launch returned, and the
// parent cannot recover it. Go crashes with a goroutine trace no two runs
// spell alike, so these are measured on the interpreter alone: the panic
// value is reported and the program's status is the panic status.
func TestGoSourceLaunchPanicsInTask(t *testing.T) {
	for name, tc := range map[string]struct{ source, want string }{
		"panic_builtin": {`package main
import ("fmt";"time")
func main(){defer func(){fmt.Println("parent recover:",recover())}();go panic("boom");time.Sleep(300*time.Millisecond)}`, "panic: boom"},
		"panic_typed_operand": {`package main
import ("errors";"time")
func main(){go panic(errors.New("typed value"));time.Sleep(300*time.Millisecond)}`, "typed value"},
		// A program-declared type keeps its identity in the report, as the
		// direct call's does.
		"panic_declared_type": {`package main
import "time"
type MyInt int
func main(){go panic(MyInt(4));time.Sleep(300*time.Millisecond)}`, "panic: main.MyInt(4)"},
		"close_nil_channel": {`package main
import "time"
func main(){var c chan int;go close(c);time.Sleep(300*time.Millisecond)}`, "close of nil channel"},
		"close_closed_channel": {`package main
import "time"
func main(){c:=make(chan int);close(c);go close(c);time.Sleep(300*time.Millisecond)}`, "close of closed channel"},
		"nil_function_value": {`package main
import "time"
func main(){var f func(int);go f(1);time.Sleep(300*time.Millisecond)}`, "invalid memory address or nil pointer dereference"},
	} {
		t.Run(name, func(t *testing.T) {
			out, errs, err := runGoSource(t, name, tc.source)
			code, ok := interp.IsExitStatus(err)
			if !ok || code != 2 {
				t.Fatalf("launched panic must be the program's failure: err=%v stdout=%q stderr=%q", err, out, errs)
			}
			if !strings.Contains(errs, tc.want) {
				t.Fatalf("stderr does not report the task's panic %q: %q", tc.want, errs)
			}
			if strings.Contains(out, "parent recover:") && !strings.Contains(out, "parent recover: <nil>") {
				t.Fatalf("a task's panic must not be recoverable by the parent: %q", out)
			}
		})
	}
}

// TestGoSourceLaunchDiscardedBuiltinResultIsRejected: a value-producing
// builtin is not permitted in statement context, and the front end rejects
// it as the Go compiler does before any launch could be prepared.
func TestGoSourceLaunchDiscardedBuiltinResultIsRejected(t *testing.T) {
	_, err := gosource.Parse(strings.NewReader("package main\nfunc main(){s:=[]int{1};go len(s)}\n"), "discard.go", gosource.Options{RunMain: true})
	if err == nil || !strings.Contains(err.Error(), "go discards result of len(s)") {
		t.Fatalf("front end must reject a launched value builtin: %v", err)
	}
}

// TestGoSourceLaunchRefusals keeps the explicit refusals explicit: each is a
// diagnostic naming the reason, never a launch with a different meaning.
func TestGoSourceLaunchRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ source, want string }{
		"closure_callback_operand": {`package main
import ("fmt";"sort";"time")
func main(){s:=[]int{3,1,2};go sort.Slice(s,func(i,j int)bool{return s[i]<s[j]});time.Sleep(100*time.Millisecond);fmt.Println(s)}`, "original closure passed to a launched dependency call"},
		"computed_closure_callback_operand": {`package main
import ("fmt";"sort";"time")
func factory()func(any,func(int,int)bool){return sort.Slice}
func main(){s:=[]int{3,1,2};go factory()(s,func(i,j int)bool{return s[i]<s[j]});time.Sleep(100*time.Millisecond);fmt.Println(s)}`, "original closure passed to a launched dependency call"},
		"atomic_over_interpreter_storage": {`package main
import ("sync/atomic";"time")
func main(){var n int64;go atomic.AddInt64(&n,1);time.Sleep(100*time.Millisecond)}`, "interpreter-owned storage"},
	} {
		t.Run(name, func(t *testing.T) {
			out, errs, err := runGoSource(t, name, tc.source)
			if err == nil {
				t.Fatalf("refused launch must fail: stdout=%q stderr=%q", out, errs)
			}
			text := err.Error() + "\n" + errs
			if !strings.Contains(text, "unsupported task launch") || !strings.Contains(text, tc.want) {
				t.Fatalf("diagnostic does not name the refusal %q: err=%v stderr=%q", tc.want, err, errs)
			}
		})
	}
}

// A panicking task cannot recover into its launcher, but evaluating its
// operand still belongs to the launcher and must happen exactly once.
func TestGoSourceLaunchFailureOperandsOnce(t *testing.T) {
	for name, statement := range map[string]string{
		"panic":        "go panic(next())",
		"nil_function": "var f func(int);go f(next())",
	} {
		t.Run(name, func(t *testing.T) {
			source := "package main\nimport \"time\"\nfunc next()int{println(\"operand-once\");return 7}\nfunc main(){" + statement + ";time.Sleep(300*time.Millisecond)}"
			out, errs, err := runGoSource(t, name, source)
			if code, ok := interp.IsExitStatus(err); !ok || code != 2 || strings.Count(errs, "operand-once\n") != 1 {
				t.Fatalf("operand must execute once before task panic: err=%v stdout=%q stderr=%q", err, out, errs)
			}
		})
	}
}
