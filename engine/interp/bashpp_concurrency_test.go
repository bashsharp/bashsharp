package interp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// concurrencyEchoCommand exercises a real external process on every OS.
func concurrencyEchoCommand(t *testing.T) string {
	t.Helper()
	t.Setenv("GOSH_PROG", "")
	t.Setenv("GOSH_CMD", "")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return shellQuote(exe) + " -test.run=^TestConcurrencyEchoHelper$ --"
}

func TestConcurrencyEchoHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" {
			fmt.Fprintln(os.Stdout, strings.Join(os.Args[i+1:], " "))
			os.Exit(0)
		}
	}
}

func runBashPPConcurrency(t *testing.T, src string) (string, error) {
	t.Helper()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "concurrency.bpp")
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), f)
	return out.String(), err
}

func TestBashPPChannelsAndGo(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func worker(ch) {
 ch <- 1
 ch <- 2
 close(ch)
}
func main() {
 ch := make(chan int)
 go worker(ch)
 for v := range ch {
  echo $v
 }
}
main()
`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "1\n2\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestBashPPClosedTwoValueReceive(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func main() {
 ch := make(chan string, 1)
 ch <- yes
 close(ch)
 first, open := <-ch
 echo "$first:$open"
 second, still := <-ch
 echo "$second:$still"
}
main()
`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "yes:true\n:false\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestBashPPChannelRefusesSubshell(t *testing.T) {
	out, _ := runBashPPConcurrency(t, `
func main() {
	ch := make(chan int)
	( ch <- one )
}
main()
`)
	if !strings.Contains(out, "channel ch cannot cross a shell-copy boundary") {
		t.Fatalf("out=%q", out)
	}
}

func TestBashPPTypedChannelAndCapacityBounds(t *testing.T) {
	out, _ := runBashPPConcurrency(t, `
func main() {
 bad := make(chan int, 65537)
 good := make(chan int, 1)
 good <- nope
}
main()
`)
	if !strings.Contains(out, "capacity must be between 0 and 65536") ||
		!strings.Contains(out, `cannot send "nope" as int channel value`) {
		t.Fatalf("out=%q", out)
	}
}

func TestBashPPSelectReceiveDeclDefaultAndClosed(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func main() {
 ch := make(chan string, 1)
 ch <- ready
 select {
 case v, ok := <-ch:
  echo "$v:$ok"
 default:
  echo missed
 }
 close(ch)
 select {
 case v, ok := <-ch:
  echo "$v:$ok"
 }
 empty := make(chan string)
 select {
 case <-empty:
  echo impossible
 default:
  echo default
 }
}
main()
`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "ready:true\n:false\ndefault\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestBashPPTaskFailureCancelsBlockedSibling(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func blocked(ch) { v := <-ch; echo $v; }
func fail() { false; }
func main() {
 ch := make(chan string)
 go blocked(ch)
 go fail()
}
main()
`)
	if err == nil {
		t.Fatal("expected task failure")
	}
	if !strings.Contains(out, "bash++: task failed: exit status 1") {
		t.Fatalf("out=%q", out)
	}
}

func TestBashPPNestedTaskRegistration(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func leaf(ch) { ch <- nested; close(ch); }
func parent(ch) { go leaf(ch); }
func main() {
 ch := make(chan string)
 go parent(ch)
 for v := range ch { echo $v; }
}
main()
`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nested") {
		t.Fatalf("out=%q", out)
	}
}

func TestBashPPChannelHandleRefusesExec(t *testing.T) {
	out, _ := runBashPPConcurrency(t, `
func main() {
 ch := make(chan string)
 /bin/echo "$ch"
}
main()
`)
	if !strings.Contains(out, "channel handles cannot cross an exec boundary") {
		t.Fatalf("out=%q", out)
	}
}

func TestBashPPConcurrentTaskOutput(t *testing.T) {
	_, err := runBashPPConcurrency(t, `
func say(v) {
 echo $v
}
func main() {
 go say(one)
 go say(two)
}
main()
`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBashPPFailureArbitratesByLaunchOrdinal(t *testing.T) {
	for range 120 {
		out, err := runBashPPConcurrency(t, `
func seven() { return 7; }
func nine() { return 9; }
func main() { go seven(); go nine(); }
main()
`)
		if err == nil || !strings.Contains(out, "exit status 7") {
			t.Fatalf("out=%q err=%v", out, err)
		}
	}
	for range 120 {
		out, err := runBashPPConcurrency(t, `
func one() { false; }
func nine() { return 9; }
func main() { go one(); go nine(); }
main()
`)
		if err == nil || !strings.Contains(out, "exit status 1") {
			t.Fatalf("code-1 concrete failure was displaced: out=%q err=%v", out, err)
		}
	}
}

func TestBashPPFastFailureRefusesLaterLaunchSideEffects(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func fail() { return 7; }
func later(ch) { ch <- launched; }
func main() {
 ch := make(chan string, 1)
 go fail()
 go later(ch)
 value := <-ch
 echo "$value"
}
main()
`)
	if err == nil || strings.Contains(out, "launched\n") || !strings.Contains(out, "exit status 7") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPReturnDefersLaunchUntilStatusSettles(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		code uint8
	}{
		{"shell status", "func f() { return 7; }", 7},
		{"nested block", "func f() { { return 7; }; }", 7},
		{"typed value", "func f() int { return 7; }", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decl := parseBashPPInternal(t, tc.src).Stmts[0].Cmd.(*syntax.BashPPFuncDecl)
			r, err := New(Lang(syntax.LangBashPP), StdIO(nil, io.Discard, io.Discard))
			if err != nil {
				t.Fatal(err)
			}
			r.Reset()
			c := newBashPPConcurrent(context.Background())
			defer c.cancel()
			r.bashPPConcurrent = c
			r.bashPPGoTask = true
			r.bashPPTaskState, _ = c.add()

			// Stop at the actual race window without relying on goroutine timing:
			// the return command has run, but its function has not settled status.
			r.cmd(c.ctx, decl.Body.Stmts[0].Cmd)
			if !r.exit.returning || !r.bashPPReturn.active {
				t.Fatal("fixture did not reach an unsettled Bash++ return")
			}
			if c.armed(r.bashPPTaskState) {
				t.Fatal("return released the launcher before its function status was settled")
			}
			r.bashPPSettleResults(&bashPPFunc{decl: decl}, nil)
			if r.exit.code != tc.code {
				t.Fatalf("settled status = %d, want %d", r.exit.code, tc.code)
			}
			var failure *bashPPTaskFailure
			if tc.code != 0 {
				failure = &bashPPTaskFailure{ordinal: r.bashPPTaskState.ordinal, code: tc.code}
			}
			c.done(r.bashPPTaskState.ordinal, failure)
			if !c.armed(r.bashPPTaskState) {
				t.Fatal("task completion did not release the launcher")
			}
			if canceled := c.ctx.Err() != nil; canceled != (tc.code != 0) {
				t.Fatalf("group canceled = %v after status %d", canceled, tc.code)
			}
		})
	}
}

func TestBashPPFastFailureCancelsOwnerReceive(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		out, err := runBashPPConcurrency(t, `
func fail() { return 7; }
func main() {
 ch := make(chan string)
 go fail()
 never := <-ch
 echo "$never"
}
main()
`)
		if err == nil || !strings.Contains(out, "exit status 7") {
			t.Errorf("out=%q err=%v", out, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("owner receive remained blocked after task failure")
	}
}

func TestBashPPEOFStopsSuccessfulBlockedTask(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		out, err := runBashPPConcurrency(t, `
func blocked(ch) { never := <-ch; echo "$never"; }
func main() { ch := make(chan string); go blocked(ch); }
main()
`)
		if err != nil || out != "" {
			t.Errorf("out=%q err=%v", out, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("File completion did not cancel and join blocked task")
	}
}

func TestCloneBashPPVariableCopiesMaps(t *testing.T) {
	orig := expand.Variable{
		ListMap: map[int]string{3: "three"},
		ListSet: map[int]bool{3: true},
		Map:     map[string]string{"key": "value"},
	}
	cloned := cloneBashPPVariable(orig)
	if cloned.ListMap[3] != "three" || !cloned.ListSet[3] || cloned.Map["key"] != "value" {
		t.Fatalf("map content lost: %#v", cloned)
	}
	cloned.ListMap[3] = "changed"
	cloned.ListSet[4] = true
	cloned.Map["key"] = "changed"
	if orig.ListMap[3] != "three" || orig.ListSet[4] || orig.Map["key"] != "value" {
		t.Fatalf("clone aliases source: orig=%#v cloned=%#v", orig, cloned)
	}
}

func TestBashPPChannelCloseCancelsRegisteredSend(t *testing.T) {
	for range 200 {
		ch := newBashPPChannel("string", 0)
		registered := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			if !ch.beginSend() {
				t.Error("fresh channel refused send registration")
				return
			}
			close(registered)
			select {
			case ch.ch <- "never":
				t.Error("unbuffered send unexpectedly completed")
			case <-ch.closing:
			}
			ch.endSend()
		}()
		<-registered
		if !ch.close() {
			t.Fatal("fresh channel refused close")
		}
		<-done
		if ch.beginSend() {
			t.Fatal("closed channel accepted send registration")
		}
	}
}

func TestBashPPTaskExitTrapRunsOnceOnSiblingCancellation(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func blocked(ch) { value := <-ch; echo "$value"; }
func fail() { return 7; }
func main() {
 trap 'echo task-exit' EXIT
 ch := make(chan string)
 go blocked(ch)
 go fail()
}
main()
`)
	if err == nil || strings.Count(out, "task-exit\n") != 3 || !strings.Contains(out, "exit status 7") {
		// Both launched tasks run their private snapshot, then the owner runs its
		// own EXIT trap at the File boundary.
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPUnbufferedParentTaskRendezvous(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func send(ch) { ch <- from-task; }
func recv(ch, ack) { value := <-ch; ack <- "$value-from-task"; }
func main() {
 from := make(chan string)
 go send(from)
 value := <-from
 echo "$value"
 to := make(chan string)
 ack := make(chan string)
 go recv(to, ack)
 to <- from-parent
 result := <-ack
 echo "$result"
}
main()
`)
	if err != nil || out != "from-task\nfrom-parent-from-task\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPGoRequiresFileOwner(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("func f() { return; }\ngo f()\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f.Stmts[1]); err == nil {
		t.Fatal("go escaped a Stmt Run without a File owner")
	}
}

func TestBashPPTaskSnapshotMutableObjectAndSubshellLevel(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
arr=(80 443)
func mutate(ch) {
 arr[0]=8080
 ch <- "$BASH_SUBSHELL:${arr[0]}"
}
func main() {
 ch := make(chan string, 1)
 go mutate(ch)
 result := <-ch
 echo "$result"
 echo "${arr[0]}"
}
main()
`)
	if err != nil || out != "0:8080\n80\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPTaskDescriptorCloseIsPrivateAndOffsetShared(t *testing.T) {
	path := t.TempDir() + "/values"
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := `
exec 8<` + shellQuote(path) + `
func first(ch) { read -u 8 value; ch <- "$value"; exec 8<&-; }
func main() {
 ch := make(chan string)
 go first(ch)
 firstValue := <-ch
 echo "$firstValue"
 read -u 8 value
 echo "$value"
}
main()
`
	out, err := runBashPPConcurrency(t, src)
	if err != nil || out != "first\nsecond\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPTaskSignalsStayLocal(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func signals(ch) {
 kill -s TERM $$
 ch <- unreachable
}
func main() { ch := make(chan string); go signals(ch); }
main()
`)
	if err == nil || !strings.Contains(out, "process signals are unavailable inside a Bash++ task") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPTaskExecTerminatesOnlyTask(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func replace() { exec /bin/echo task-exec; echo unreachable; }
func main() { go replace(); echo owner-alive; }
main()
`)
	if err != nil || !strings.Contains(out, "owner-alive\n") || strings.Contains(out, "unreachable") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPChannelNumericWidthsAndNamedTypes(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
type Small int8
type Alias = uint8
type Color enum { Red; Green }
func main() {
 a := make(chan Small, 1); a <- 128
 b := make(chan Alias, 1); b <- -1
 c := make(chan Color, 1); c <- Blue
}
main()
`)
	if err == nil || !strings.Contains(out, `cannot send "128" as Small`) {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPStaleChannelCapabilityIsInvalid(t *testing.T) {
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	parse := func(src string) *syntax.File {
		f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "stale.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if err := r.Run(context.Background(), parse("func setup() { ch := make(chan string, 1); SAVED=$ch; }\nsetup()\n")); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), parse("func stale() { SAVED <- stale; }\nstale()\n")); err == nil ||
		!strings.Contains(out.String(), "not a channel in this task group") {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
}

func TestBashPPTaskSnapshotRefusesUnsupportedObject(t *testing.T) {
	var out strings.Builder // bashpp-racegate:safe-private
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	if err := r.writeEnv.Set("OPAQUE", expand.Variable{Set: true, Kind: expand.Object, Obj: make(chan int)}); err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("func f() { return; }\ngo f()\n"), "object.bpp")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f); err == nil || !strings.Contains(out.String(), "unsupported mutable Bash++ object type") {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
}

func TestBashPPTaskObjectCloneRejectsCycle(t *testing.T) {
	object := map[string]any{}
	object["self"] = object
	if _, err := newBashPPObjectCloner().clone(object); err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("cycle clone error = %v", err)
	}
}

func TestBashPPSelectReleasesSendBeforeArmAndOnError(t *testing.T) {
	for _, src := range []string{
		`func main() { ch := make(chan string, 1); select { case ch <- ok: close(ch); } }; main()`,
		`func main() { ch := make(chan string); select { case ch <- ok: echo no; case missing <- bad: echo no; }; close(ch); }; main()`,
	} {
		done := make(chan struct{})
		go func() { _, _ = runBashPPConcurrency(t, src); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("select leaked a send registration")
		}
	}
}

func TestBashPPImmediateChannelFailureExcludesLaterTask(t *testing.T) {
	tests := []struct {
		name  string
		setup string
		body  string
	}{
		{"closed send", "close(ch)", "ch <- value"},
		{"closed select send", "close(ch)", "select { case ch <- value: return; }"},
		{"ready select", "ch <- value", "select { case <-ch: return 7; }"},
		{"default select", "", "select { case <-ch: return 8; default: return 7; }"},
		{"buffered range body", "ch <- value; close(ch)", "for range ch { return 7; }"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runBashPPConcurrency(t, `
func first(ch) { `+tc.body+`; }
func later() { echo escaped; }
func main() {
 ch := make(chan string, 1)
 `+tc.setup+`
 go first(ch)
 go later()
}
main()
`)
			if err == nil || strings.Contains(out, "escaped") {
				t.Fatalf("immediate outcome armed later task: out=%q err=%v", out, err)
			}
		})
	}
}

func TestBashPPReceiveValidatesBeforeBufferedConsume(t *testing.T) {
	const src = `
func main() {
 ch := make(chan string, 1)
 ch <- kept
 a, b := <-ch
 value := <-ch
 echo "$value"
}
main()
`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "malformed-receive.bpp")
	if err != nil {
		t.Fatal(err)
	}
	mutated := false
	syntax.Walk(f, func(node syntax.Node) bool {
		if decl, ok := node.(*syntax.BashPPShortDecl); ok && decl.Recv != nil && !mutated {
			decl.Lhs = append(decl.Lhs, &syntax.Lit{Value: "c"})
			mutated = true
		}
		return true
	})
	if !mutated {
		t.Fatal("receive declaration not found")
	}
	var out strings.Builder // bashpp-racegate:safe-private
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), f)
	if !strings.Contains(out.String(), "receive assignment mismatch") || !strings.Contains(out.String(), "kept\n") {
		t.Fatalf("malformed receive consumed buffered value: out=%q err=%v", out.String(), err)
	}
}

func TestBashPPEmptySelectCanceledByFailingSibling(t *testing.T) {
	const src = `
func blocked() { select {} }
func fail() { return 7; }
func main() { go blocked(); go fail(); }
main()
`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "empty-select.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = r.Run(context.Background(), f)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("empty select did not observe sibling cancellation")
	}
	if runErr == nil || !strings.Contains(output.String(), "exit status 7") {
		t.Fatalf("out=%q err=%v", output.String(), runErr)
	}
}

func TestBashPPOpenEmptyRangeArmsForOwnerSend(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func drain(ch, ack) {
 for value := range ch { echo "$value"; ack <- ok; }
}
func main() {
 ch := make(chan string)
 ack := make(chan string)
 go drain(ch, ack)
 ch <- ready
 <-ack
 close(ch)
}
main()
`)
	if err != nil || out != "ready\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPSelectDefaultAndReadyReleaseRegistrations(t *testing.T) {
	for _, tc := range []struct{ capacity, body string }{
		{"0", "select { default: return; }"},
		{"0", "select { case ch <- value: return 7; default: return; }"},
		{"1", "select { case ch <- value: return; default: return 7; }"},
	} {
		out, err := runBashPPConcurrency(t, `
func choose(ch) { `+tc.body+`; }
func main() { ch := make(chan string, `+tc.capacity+`); go choose(ch); close(ch); }
main()
`)
		if err != nil {
			t.Fatalf("select registration leaked: body=%q out=%q err=%v", tc.body, out, err)
		}
	}
}

func TestBashPPChannelTypeExistenceAndReceiveIdentity(t *testing.T) {
	out, err := runBashPPConcurrency(t, `func main() { ch := make(chan Missing); }; main()`)
	if err == nil || !strings.Contains(out, "undefined element type: Missing") {
		t.Fatalf("out=%q err=%v", out, err)
	}

	out, err = runBashPPConcurrency(t, `
type Small int8
func (v Small) Show() { echo "typed:$v"; }
func main() { ch := make(chan Small, 1); ch <- 7; value := <-ch; value.Show(); }
main()
`)
	if err != nil || out != "typed:7\n" {
		t.Fatalf("named receive identity: out=%q err=%v", out, err)
	}

	r := &Runner{bashPPScope: newBashPPScope(nil), bashPPTypes: map[string]bashPPType{
		"Named": {underlying: "int8"},
		"Alias": {underlying: "uint8", alias: true},
		"Color": {underlying: "enum", members: []string{"Red", "Green"}},
	}}
	for _, tc := range []struct {
		typ, good, bad, identity string
	}{
		{"Named", "127", "128", "Named"},
		{"Alias", "255", "-1", ""},
		{"Color", "Red", "Blue", "Color"},
	} {
		if !r.bashPPValueFits(tc.typ, tc.good) || r.bashPPValueFits(tc.typ, tc.bad) {
			t.Errorf("independent %s validation failed", tc.typ)
		}
		if err := r.bashPPScope.declare(tc.typ, expand.Variable{Set: true}, false); err != nil {
			t.Fatal(err)
		}
		r.bashPPSetReceivedType(tc.typ, tc.typ)
		if got := r.bashPPScope.lookup(tc.typ).typeName; got != tc.identity {
			t.Errorf("received %s identity = %q, want %q", tc.typ, got, tc.identity)
		}
	}
}

func TestBashPPKnownCapabilityCannotExfiltrateOrCrossLaterExec(t *testing.T) {
	out, _ := runBashPPConcurrency(t, `
func main() {
 ch := make(chan string, 1)
	 ( echo "prefix-${ch}-suffix" )
	 echo "$(echo prefix-${ch}-suffix)"
	 echo "$ch" | /bin/cat
}

main()
`)
	if strings.Count(out, "channel handles cannot cross a shell-copy boundary") < 1 {
		t.Fatalf("shell-copy exfiltration was not rejected: %q", out)
	}

	var later strings.Builder // bashpp-racegate:safe-synchronized
	r, _ := New(Lang(syntax.LangBashPP), StdIO(nil, &later, &later))
	parse := func(src string) *syntax.File {
		f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "stale-exec.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if err := r.Run(context.Background(), parse(`func setup() { ch := make(chan string, 1); export SAVED=$ch; }; setup()`)); err == nil ||
		!strings.Contains(later.String(), "channel capabilities cannot be exported") {
		t.Fatalf("capability export was not rejected: out=%q err=%v", later.String(), err)
	}
	if err := r.Run(context.Background(), parse(`func save() { ch := make(chan string, 1); SAVED=$ch; }; save()`)); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), parse(`func stale() { SAVED <- nope; }; stale()`)); err == nil ||
		!strings.Contains(later.String(), "not a channel in this task group") {
		t.Fatalf("stale capability gained authority: out=%q err=%v", later.String(), err)
	}
	if err := r.Run(context.Background(), parse(concurrencyEchoCommand(t)+` "prefix-${SAVED}-suffix"`)); err == nil ||
		!strings.Contains(later.String(), "channel handles cannot cross an exec boundary") {
		t.Fatalf("stale capability crossed exec: out=%q err=%v", later.String(), err)
	}
	innocent, err := runBashPPConcurrency(t, concurrencyEchoCommand(t)+` chan@bashpp:not-issued`)
	if err != nil || innocent != "chan@bashpp:not-issued\n" {
		t.Fatalf("innocent prefix rejected: out=%q err=%v", innocent, err)
	}
}

func TestBashPPCanceledTaskDoesNotPromoteTransientFailure(t *testing.T) {
	for range 120 {
		out, err := runBashPPConcurrency(t, `
func transient(ch) { false; value := <-ch; echo "$value"; }
func seven() { return 7; }
func main() { ch := make(chan string); go transient(ch); go seven(); }
main()
`)
		if err == nil || !strings.Contains(out, "exit status 7") || strings.Contains(out, "exit status 1") {
			t.Fatalf("out=%q err=%v", out, err)
		}
	}
}

func TestBashPPUnseededTaskGetsPrivateRNG(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	r.bashPPConcurrent = newBashPPConcurrent(context.Background())
	defer r.bashPPConcurrent.cancel()
	a, err := r.bashPPTaskSnapshot(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.closeBashPPTaskResources()
	b, err := r.bashPPTaskSnapshot(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.closeBashPPTaskResources()
	if !a.randomSeeded || !b.randomSeeded || a.randomSeed == b.randomSeed {
		t.Fatalf("task RNGs not private: a=%d/%v b=%d/%v", a.randomSeed, a.randomSeeded, b.randomSeed, b.randomSeeded)
	}
}

func TestBashPPTypedChannelCapabilityPropagationAndForgery(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func relay(ch) { ch <- function; }
func namedRelay(ch) { ch <- named; }
func defaultRelay(ch string = channel) { ch <- default; }
func main() {
 ch := make(chan string, 5)
	channel := ch
	copy := ch
	copy <- direct
	var assigned string
	assigned=ch
	assigned <- assigned
	relay(ch)
 namedRelay(ch: ch)
 defaultRelay()
	first := <-ch
	second := <-ch
	third := <-ch
	fourth := <-ch
	fifth := <-ch
 echo "$first"
 echo "$second"
 echo "$third"
	echo "$fourth"
	echo "$fifth"
 forged := "$ch"
 forged <- denied
}
main()
`)
	if err == nil || !strings.Contains(out, "direct\n") || !strings.Contains(out, "assigned\n") || !strings.Contains(out, "function\n") ||
		!strings.Contains(out, "named\n") || !strings.Contains(out, "default\n") ||
		!strings.Contains(out, "forged is not a channel in this task group") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPTypeAliasChainsAndInvalidUnderlying(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`type Broken Missing`, "undefined type: Missing"},
		{`type Loop Loop`, "cyclic type declaration: Loop"},
	} {
		out, err := runBashPPConcurrency(t, tc.src)
		if err == nil || !strings.Contains(out, tc.want) {
			t.Errorf("src=%q out=%q err=%v", tc.src, out, err)
		}
	}
	r := &Runner{bashPPScope: newBashPPScope(nil), bashPPTypes: map[string]bashPPType{
		"Base":  {underlying: "int8"},
		"Alias": {underlying: "Base", alias: true},
		"Chain": {underlying: "Alias", alias: true},
	}}
	_ = r.bashPPScope.declare("value", expand.Variable{Set: true}, false)
	r.bashPPSetReceivedType("value", "Chain")
	if got := r.bashPPScope.lookup("value").typeName; got != "Base" {
		t.Fatalf("alias chain identity=%q", got)
	}
	if !r.bashPPValueFits("Chain", "127") || r.bashPPValueFits("Chain", "128") {
		t.Fatal("alias chain validation did not reach underlying type")
	}
}

func TestBashPPRecursivePointerTypeAndAliasCycle(t *testing.T) {
	if out, err := runBashPPConcurrency(t, `type Node *Node`); err != nil {
		t.Fatalf("defined pointer recursion rejected: out=%q err=%v", out, err)
	}
	out, err := runBashPPConcurrency(t, `type Loop = *Loop`)
	if err == nil || !strings.Contains(out, "cyclic type declaration: Loop") {
		t.Fatalf("alias pointer cycle accepted: out=%q err=%v", out, err)
	}
}

func TestBashPPMapfileCancellationUnblocksTask(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(Lang(syntax.LangBashPP), StdIO(read, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`
func blocked() { mapfile values; }
func fail() { return 7; }
func main() { go blocked(); go fail(); }
main()
`), "mapfile-cancel.bpp")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = r.Run(ctx, f)
	if err == nil || !strings.Contains(out.String(), "blocking non-regular input is unavailable") || ctx.Err() != nil {
		t.Fatalf("out=%q err=%v ctx=%v", out.String(), err, ctx.Err())
	}
}

func TestBashPPFileBoundaryPrunesHistoryAndClosureChannels(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	handle := bashPPChanHandlePrefix + "retained"
	r.bashPPIssuedHandles = &bashPPHandleProvenance{handles: map[string]struct{}{handle: {}}}
	r.bashPPConcurrent = c
	scope := newBashPPScope(nil)
	cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: handle}, channel: newBashPPChannel("string", 0), channelOwner: c}
	scope.entries["saved"] = cell
	r.bashPPFuncScopes = map[string]*bashPPScope{"saved": scope}
	r.bashPPClearChannelRefs(c)
	if cell.channel != nil || cell.channelOwner != nil || cell.vr.Str != handle {
		t.Fatalf("channel authority not selectively revoked: %#v", cell)
	}
	r.bashPPConcurrent = nil
	r.bashPPPruneIssuedHandles(nil)
	if len(r.bashPPIssuedHandles.handles) != 1 {
		t.Fatalf("captured defense history was pruned while reachable: %#v", r.bashPPIssuedHandles.handles)
	}
	cell.vr.Str = ""
	r.bashPPPruneIssuedHandles(nil)
	if len(r.bashPPIssuedHandles.handles) != 0 {
		t.Fatalf("cleared defense history retained: %#v", r.bashPPIssuedHandles.handles)
	}
}

func TestBashPPGoFailFastOperationsDoNotReleaseLaterTask(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-input")
	tests := []struct {
		name string
		body string
	}{
		{"read option", "read -Z value"},
		{"mapfile option", "mapfile -Z values"},
		{"missing redirect", ": < " + missing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runBashPPConcurrency(t, `
func bad() { `+tc.body+`; }
func later() { echo escaped; }
func main() { go bad(); go later(); }
main()
`)
			if err == nil || strings.Contains(out, "escaped") {
				t.Fatalf("fail-fast operation released later task: out=%q err=%v", out, err)
			}
		})
	}
}

func TestCloneBashPPTaskCellsClonesReceiverObjects(t *testing.T) {
	parentObject := map[string]any{"value": "parent"}
	parentFn := &bashPPFunc{receiver: &bashPPCell{vr: expand.Variable{
		Set: true, Kind: expand.Object, Obj: parentObject,
	}}}
	cloner := newBashPPCloner()
	childFn := parentFn.cloned(cloner)
	child := &Runner{bashPPFuncs: map[string]*bashPPFunc{"bound": childFn}}
	if err := cloneBashPPTaskCells(child, newBashPPObjectCloner(), nil); err != nil {
		t.Fatal(err)
	}
	childObject := childFn.receiver.vr.Obj.(map[string]any)
	childObject["value"] = "child"
	if got := parentObject["value"]; got != "parent" {
		t.Fatalf("task receiver mutation escaped snapshot: %v", got)
	}
}

func TestBashPPFileBoundaryRetainsMethodReceiverHandleDefense(t *testing.T) {
	var out strings.Builder // bashpp-racegate:safe-private
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	run := func(src string) error {
		f, parseErr := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "method-handle.bpp")
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return r.Run(context.Background(), f)
	}
	if err := run(`
type Token string
func (v Token) Use() { /usr/bin/echo "$v"; }
func main() {
 var token Token = hello
 saved := token.Use
}
main()
`); err != nil {
		t.Fatalf("first run: out=%q err=%v", out.String(), err)
	}
	var receiver *bashPPCell
	closureIndex := -1
	for i, fn := range r.bashPPClosures {
		if fn.receiver != nil && fn.receiver.vr.Str == "hello" {
			receiver = fn.receiver
			closureIndex = i
		}
	}
	if receiver == nil {
		t.Fatal("actual bound method receiver was not retained in closure registry")
	}
	// Ordinary typing correctly refuses converting a live channel capability
	// into Token. Seed the defense-only state after creating a real bound
	// method so this regression isolates persistent receiver graph traversal.
	owner := newBashPPConcurrent(context.Background())
	defer owner.cancel()
	handle := bashPPChanHandlePrefix + "method-receiver"
	receiver.vr.Str = handle
	receiver.channel = newBashPPChannel("string", 0)
	receiver.channelOwner = owner
	r.bashPPIssuedHandles = &bashPPHandleProvenance{handles: map[string]struct{}{handle: {}}}
	r.bashPPClearChannelRefs(owner)
	r.bashPPPruneIssuedHandles(nil)
	if receiver.channel != nil || receiver.channelOwner != nil || len(r.bashPPIssuedHandles.handles) != 1 {
		t.Fatalf("persistent receiver root was not revoked and retained: receiver=%#v handles=%#v", receiver, r.bashPPIssuedHandles.handles)
	}
	if err := run(`GOSH_CMD=exit_0 "$GOSH_PROG"`); err != nil {
		t.Fatalf("later Run failed before retained invocation: out=%q err=%v", out.String(), err)
	}
	r.bashPPInvoke(context.Background(), r.bashPPClosures[closureIndex], nil)
	if r.exit.code == 0 || !strings.Contains(out.String(), "cannot cross an exec boundary") {
		t.Fatalf("retained method receiver leaked handle: out=%q exit=%v", out.String(), r.exit)
	}
}

func TestBashPPTaskCustomOpenFailsClosedBeforeHandler(t *testing.T) {
	for _, tc := range []struct{ body string }{
		{"source virtual.bpp"},
		{": > virtual.out"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var calls atomic.Int32
			var out strings.Builder // bashpp-racegate:safe-synchronized
			r, err := New(
				Lang(syntax.LangBashPP),
				StdIO(nil, &out, &out),
				OpenHandler(func(context.Context, string, int, os.FileMode) (io.ReadWriteCloser, error) {
					calls.Add(1)
					return nil, errors.New("unexpected custom open")
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`
func load() { `+tc.body+`; }
func later() { echo escaped; }
func main() { go load(); go later(); }
main()
`), "custom-open.bpp")
			if err != nil {
				t.Fatal(err)
			}
			err = r.Run(context.Background(), f)
			if err == nil || calls.Load() != 0 || strings.Contains(out.String(), "escaped") ||
				strings.Count(out.String(), "custom open handlers are unavailable") != 1 {
				t.Fatalf("out=%q err=%v custom calls=%d", out.String(), err, calls.Load())
			}
		})
	}
}

func TestBashPPTaskOpenRejectsPreCanceledSideEffects(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newBashPPConcurrent(context.Background())
	c.cancel()
	r := &Runner{Dir: dir, bashPPGoTask: true, bashPPConcurrent: c}
	r.ensureDirFile(dir)
	defer r.closeDirFile()
	for _, name := range []string{"missing", "existing"} {
		f, err := r.bashPPTaskOpen(context.Background(), name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600, false, false)
		if f != nil {
			_ = f.Close()
			t.Fatalf("pre-cancelled open returned a descriptor for %s", name)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-cancelled open %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-cancelled create changed filesystem: %v", err)
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "preserved" {
		t.Fatalf("pre-cancelled truncate changed existing file: data=%q err=%v", data, err)
	}
}

func TestBashPPTaskImmediateReadFailureExcludesLaterTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"read value < " + path + " || return $?",
		"read -u 99 value || return $?",
	} {
		t.Run(body, func(t *testing.T) {
			out, err := runBashPPConcurrency(t, `
func first() { `+body+`; }
func later() { echo escaped; }
func main() { go first(); go later(); }
main()
`)
			if err == nil || strings.Contains(out, "escaped") {
				t.Fatalf("immediate read armed later task: out=%q err=%v", out, err)
			}
		})
	}
}

func TestBashPPTaskCustomReaderFailsClosed(t *testing.T) {
	var output strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(
		Lang(syntax.LangBashPP),
		StdIO(strings.NewReader("ready\n"), &output, &output),
	)
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`
func first() { read value; }
func later() { echo escaped; }
func main() { go first(); go later(); }
main()
`), "custom-reader.bpp")
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), f)
	if err == nil || strings.Contains(output.String(), "escaped") ||
		!strings.Contains(output.String(), "non-cooperative input is unavailable") {
		t.Fatalf("out=%q err=%v", output.String(), err)
	}
}

func TestBashPPTaskZeroReadOptionsDoNotConsume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{"-t 0", "-n 0", "-N 0"} {
		t.Run(option, func(t *testing.T) {
			out, err := runBashPPConcurrency(t, `
exec 8<`+shellQuote(path)+`
func first() {
 read `+option+` -u 8 ignored
}
func main() {
 go first()
 read -u 8 value
 echo "$value"
}
main()
`)
			if err != nil || out != "ready\n" {
				t.Fatalf("zero option consumed input: out=%q err=%v", out, err)
			}
		})
	}
}

func TestReadZeroOptionsMatchBashReadyAndEOF(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(ready, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []syntax.LangVariant{syntax.LangBash, syntax.LangPOSIX} {
		for _, option := range []string{"-t 0", "-n 0", "-N 0"} {
			t.Run(lang.String()+"/"+option, func(t *testing.T) {
				var output strings.Builder // bashpp-racegate:safe-private
				r, err := New(Lang(lang), StdIO(nil, &output, &output))
				if err != nil {
					t.Fatal(err)
				}
				f, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader(`
value=old
read `+option+` value < `+ready+`
printf '<%s>:%s\n' "$value" "$?"
value=old
read `+option+` value < `+empty+`
printf '<%s>:%s\n' "$value" "$?"
`), "read-zero-count.sh")
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Run(context.Background(), f); err != nil {
					t.Fatal(err)
				}
				want := "<>:0\n<>:0\n"
				if option == "-t 0" {
					want = "<old>:0\n<old>:0\n"
				}
				if got := output.String(); got != want {
					t.Fatalf("output=%q", got)
				}
			})
		}
	}
}

func TestBashPPFileRunPrunesClearedPersistentHandle(t *testing.T) {
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	run := func(src string) error {
		f, parseErr := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "boundary.bpp")
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return r.Run(context.Background(), f)
	}
	if err := run(`func main() { ch := make(chan string); saved=$ch; }; main()`); err != nil {
		t.Fatal(err)
	}
	if r.bashPPIssuedHandles == nil || len(r.bashPPIssuedHandles.handles) == 0 {
		t.Fatal("issued handle was not retained while reachable")
	}
	if err := run(`unset saved`); err != nil {
		t.Fatal(err)
	}
	if len(r.bashPPIssuedHandles.handles) != 0 {
		t.Fatalf("cleared handle history retained: %#v", r.bashPPIssuedHandles.handles)
	}
	if err := run(`GOSH_CMD=exit_0 "$GOSH_PROG"`); err != nil {
		t.Fatalf("stale history falsely refused later exec: %v output=%q", err, out.String())
	}
}

func TestBashPPFileRunRevokesCapturedFunctionChannel(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`
func main() {
 ch := make(chan string)
 var marker = 7
 func retained() { ch <- held; echo "$marker"; }
}
main()
`), "captured-channel.bpp")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	fn := r.bashPPFuncs["retained"]
	if fn == nil || fn.scope == nil {
		t.Fatal("captured function did not persist")
	}
	channelCell := fn.scope.lookup("ch")
	markerCell := fn.scope.lookup("marker")
	if channelCell == nil || channelCell.channel != nil || channelCell.channelOwner != nil {
		t.Fatalf("captured channel authority survived File boundary: %#v", channelCell)
	}
	if markerCell == nil || markerCell.vr.Str != "7" {
		t.Fatalf("unrelated captured lexical state changed: %#v", markerCell)
	}
}

func TestBashPPRegularSourcePreservesFailureHandshake(t *testing.T) {
	path := t.TempDir() + "/failure.bpp"
	if err := os.WriteFile(path, []byte("return 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runBashPPConcurrency(t, `
func load() { source `+path+`; }
func later() { echo escaped; }
func main() { go load(); go later(); }
main()
`)
	if err == nil || strings.Contains(out, "escaped") || !strings.Contains(out, "exit status 7") {
		t.Fatalf("regular source released handshake before sourced failure: out=%q err=%v", out, err)
	}
}
