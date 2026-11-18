//go:build full

package lower

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

type cancellationOutput struct {
	sync.Mutex
	bytes.Buffer // bashpp-racegate:safe-synchronized — writes lock; reads follow Runner.Run's task join.
}

func (b *cancellationOutput) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}

func (b *cancellationOutput) WriteString(s string) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.WriteString(s)
}

// These are unchanged public source conditions from the named interpreter
// tests. Each source becomes an importable native package with an injected
// entry; the host runs only after all generated and host Go files are removed.
func TestCompiledEntryCancellationAcceptance(t *testing.T) {
	cases := []struct{ name, source string }{
		// interp/bashpp_concurrency_test.go:TestBashPPTaskFailureCancelsBlockedSibling
		{"blocked_sibling", "\nfunc blocked(ch) { v := <-ch; echo $v; }\nfunc fail() { false; }\nfunc main() {\n ch := make(chan string)\n go blocked(ch)\n go fail()\n}\nmain()\n"},
		// interp/bashpp_concurrency_test.go:TestBashPPFastFailureCancelsOwnerReceive
		{"owner_receive", "\nfunc fail() { return 7; }\nfunc main() {\n ch := make(chan string)\n go fail()\n never := <-ch\n echo \"$never\"\n}\nmain()\n"},
		// interp/bashpp_concurrency_test.go:TestBashPPEmptySelectCanceledByFailingSibling
		{"empty_select", "\nfunc blocked() { select {} }\nfunc fail() { return 7; }\nfunc main() { go blocked(); go fail(); }\nmain()\n"},
		// interp/bashpp_concurrency_test.go:TestBashPPTaskExitTrapRunsOnceOnSiblingCancellation
		{"exit_traps", "\nfunc blocked(ch) { value := <-ch; echo \"$value\"; }\nfunc fail() { return 7; }\nfunc main() {\n trap 'echo task-exit' EXIT\n ch := make(chan string)\n go blocked(ch)\n go fail()\n}\nmain()\n"},
	}
	// interp/bashpp_concurrency_test.go:TestBashPPImmediateChannelFailureExcludesLaterTask
	for _, tc := range []struct{ name, setup, body string }{
		{"closed_send", "close(ch)", "ch <- value"},
		{"closed_select_send", "close(ch)", "select { case ch <- value: return; }"},
		{"ready_select", "ch <- value", "select { case <-ch: return 7; }"},
		{"default_select", "", "select { case <-ch: return 8; default: return 7; }"},
		{"buffered_range_body", "ch <- value; close(ch)", "for range ch { return 7; }"},
	} {
		cases = append(cases, struct{ name, source string }{tc.name, "\nfunc first(ch) { " + tc.body + "; }\nfunc later() { echo escaped; }\nfunc main() {\n ch := make(chan string, 1)\n " + tc.setup + "\n go first(ch)\n go later()\n}\nmain()\n"})
	}
	dir := t.TempDir()
	writeEntryModule(t, dir)
	var imports, entries strings.Builder // bashpp-racegate:safe-private — subtests run sequentially while generating source.
	var paths []string
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.source), "input.bpp")
			if err != nil {
				t.Fatal(err)
			}
			out, diagnostic, status := cancellationOracle(t, file)
			t.Logf("interpreter stdout=%q stderr=%q status=%d", out, diagnostic, status)
			name := fmt.Sprintf("unit%d", i)
			compiled, err := Compile(file, Options{Package: name, Entry: "Execute"})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name, "program.go")
			writeEntryFile(t, path, string(compiled.Source))
			paths = append(paths, path)
			fmt.Fprintf(&imports, "%s %q\n", name, "entryartifact/"+name)
			fmt.Fprintf(&entries, "{%q,%s.Execute,%q,%q,%d},\n", tc.name, name, out, diagnostic, status)
		})
	}
	host := filepath.Join(dir, "host", "acceptance_test.go")
	writeEntryFile(t, host, strings.ReplaceAll(strings.ReplaceAll(cancellationHost, "IMPORTS", imports.String()), "ENTRIES", entries.String()))
	paths = append(paths, host)
	binary := filepath.Join(dir, "acceptance.test")
	runEntryCommand(t, dir, "go", "test", "-mod=mod", "-race", "-c", "-o", binary, "./host")
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-test.v", "-test.timeout=25s")
	command.Dir = t.TempDir()
	command.Env = []string{"PATH=/no-tools", "GORACE=halt_on_error=1"} // bashpp-racegate:safe-private — child command configured before CombinedOutput starts it.
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compiled cancellation acceptance: %v\n%s", err, output)
	}
}

func cancellationOracle(t *testing.T, file *syntax.File) (string, string, int) {
	t.Helper()
	var out, diagnostic cancellationOutput
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &diagnostic), interp.Env(expand.ListEnviron("PATH=/no-tools")), interp.Dir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status := 0
	if err := runner.Run(ctx, file); err != nil {
		var exit interp.ExitStatus
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		status = int(exit)
	}
	if ctx.Err() != nil {
		t.Fatal("interpreter cancellation oracle exceeded deadline")
	}
	return out.String(), diagnostic.String(), status
}

const cancellationHost = `package host_test
import("bytes";"context";"fmt";"runtime";"sync";"testing";"time"; rt "github.com/bashsharp/bashsharp/engine/lower/shellrt"
IMPORTS
)
type lockedOutput struct { sync.Mutex; bytes.Buffer } // bashpp-racegate:safe-synchronized — writes lock; each invocation reads after its task join.
func(b *lockedOutput)Write(p []byte)(int,error){b.Lock();defer b.Unlock();return b.Buffer.Write(p)}
func(b *lockedOutput)WriteString(s string)(int,error){b.Lock();defer b.Unlock();return b.Buffer.WriteString(s)}
func TestEntries(t *testing.T) {
 cases:=[]struct{name string; run func(...rt.SessionOption)(int,error); stdout,stderr string;status int}{ENTRIES}
 for _,processors:=range []int{1,2,4}{
  old:=runtime.GOMAXPROCS(processors)
  for _,tc:=range cases {t.Run(fmt.Sprintf("%d/%s",processors,tc.name),func(t *testing.T){
   var group sync.WaitGroup
   for range 8 {group.Go(func(){
    var out,diagnostic lockedOutput
    ctx,cancel:=context.WithTimeout(context.Background(),2*time.Second);defer cancel()
    status,err:=tc.run(rt.WithContext(ctx),rt.WithStdio(nil,&out,&diagnostic))
    if err!=nil {fmt.Fprintln(&diagnostic,err)}
    if ctx.Err()!=nil {t.Errorf("invocation exceeded deadline: %v",ctx.Err())}
    if status!=tc.status || out.String()!=tc.stdout || diagnostic.String()!=tc.stderr {
     t.Errorf("status=%d stdout=%q stderr=%q; want status=%d stdout=%q stderr=%q",status,out.String(),diagnostic.String(),tc.status,tc.stdout,tc.stderr)
    }
   })}
   group.Wait()
  })}
  runtime.GOMAXPROCS(old)
 }
}
`
