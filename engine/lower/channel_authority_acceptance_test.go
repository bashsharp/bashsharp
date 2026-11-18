//go:build full

package lower

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The Bash++ source below is the acceptance program: every way a task group
// can name the same channel (direct binding, plain assignment, positional
// argument, named argument, parameter default) must keep channel authority,
// and a string copy of a channel must not. It is compiled by the real
// compiler and executed as a native artifact with no source on disk.
const channelAuthoritySource = `
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
`

// These are the measured public interpreter results for channelAuthoritySource,
// recorded from the source oracle below. They are asserted before anything is
// compiled, so a change in the public source contract fails here rather than
// silently redefining what the artifact is compared against.
const (
	channelAuthorityWantStdout = "direct\nassigned\nfunction\nnamed\ndefault\n"
	channelAuthorityWantStderr = "bash++: forged is not a channel in this task group\n"
	channelAuthorityWantStatus = 2
)

func TestChannelAuthorityAcceptance(t *testing.T) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(channelAuthoritySource), "input.bpp")
	if err != nil {
		t.Fatal(err)
	}
	out, diagnostic, status := channelAuthorityOracle(t, file)
	t.Logf("interpreter stdout=%q stderr=%q status=%d", out, diagnostic, status)
	if out != channelAuthorityWantStdout || diagnostic != channelAuthorityWantStderr || status != channelAuthorityWantStatus {
		t.Fatalf("public source contract changed: stdout=%q stderr=%q status=%d; want stdout=%q stderr=%q status=%d",
			out, diagnostic, status, channelAuthorityWantStdout, channelAuthorityWantStderr, channelAuthorityWantStatus)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	dir := t.TempDir()
	writeEntryModule(t, dir)

	// A rejection here is a real compiler gap on a source the interpreter
	// accepts and runs. It is reported, never skipped or reclassified.
	compiled, err := Compile(file, Options{Package: "unit", Entry: "Execute"})
	if err != nil {
		t.Fatalf("Compile rejected a source the interpreter executes: %v", err)
	}

	generated := filepath.Join(dir, "unit", "program.go")
	writeEntryFile(t, generated, string(compiled.Source))

	host := filepath.Join(dir, "host", "acceptance_test.go")
	entries := fmt.Sprintf("{%q,unit.Execute,%q,%q,%d},", "channel_authority", out, diagnostic, status)
	hostSrc := strings.ReplaceAll(strings.ReplaceAll(acceptanceEntryHost, "IMPORTS", `unit "entryartifact/unit"`), "ENTRIES", entries)
	writeEntryFile(t, host, hostSrc)

	binary := filepath.Join(dir, "acceptance.test")
	buildAcceptanceHost(t, ctx, dir, binary)

	for _, path := range []string{generated, host} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	runAcceptanceHost(t, ctx, dir, binary)
}

func channelAuthorityOracle(t *testing.T, file *syntax.File) (string, string, int) {
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
		t.Fatal("interpreter oracle exceeded deadline")
	}
	return out.String(), diagnostic.String(), status
}

// buildAcceptanceHost compiles the host test binary under the caller's bounded
// context. The build inherits the manager's environment, so the toolchain,
// module cache and build cache are whoever runs the gate; only GOWORK is
// pinned off so the temporary module resolves through its own replace.
func buildAcceptanceHost(t *testing.T, ctx context.Context, dir, binary string) {
	t.Helper()
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBinary); err != nil {
		t.Fatalf("no go toolchain at %s: %v", goBinary, err)
	}
	buildCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	build := exec.CommandContext(buildCtx, goBinary, "test", "-mod=mod", "-race", "-c", "-o", binary, "./host")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off")
	output, err := build.CombinedOutput()
	if buildCtx.Err() != nil {
		t.Fatalf("host build exceeded deadline: %v\n%s", buildCtx.Err(), output)
	}
	if err != nil {
		t.Fatalf("host build failed: %v\n%s", err, output)
	}
}

// runAcceptanceHost proves the artifact carries no source and then runs it in
// a bounded, empty working directory with a fixed environment, so the native
// program cannot reach the host toolchain or any Bash++ input.
func runAcceptanceHost(t *testing.T, ctx context.Context, moduleDir, binary string) {
	t.Helper()
	assertNoAcceptanceSource(t, moduleDir)
	execDir := t.TempDir()
	assertNoAcceptanceSource(t, execDir)
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(runCtx, binary, "-test.v", "-test.timeout=25s")
	command.Dir = execDir
	command.Env = []string{"PATH=/no-tools", "GORACE=halt_on_error=1"}
	output, err := command.CombinedOutput()
	if runCtx.Err() != nil {
		t.Fatalf("compiled acceptance exceeded deadline: %v\n%s", runCtx.Err(), output)
	}
	if err != nil {
		t.Fatalf("compiled acceptance: %v\n%s", err, output)
	}
}

// assertNoAcceptanceSource walks a tree the artifact can see and fails if any
// Bash++ or Go source survives there.
func assertNoAcceptanceSource(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".bpp") {
			return fmt.Errorf("source still present: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// acceptanceEntryHost is the caller of the compiled entry. It reproduces the
// boundary that the generated process main uses: the entry never prints the
// error it returns, so the caller renders it to stderr after whatever the
// program itself wrote. The two are kept apart in the comparison and in the
// failure report, and an error that merely repeats a diagnostic the program
// already emitted is reported as duplication rather than normalized away.
const acceptanceEntryHost = `package host_test
import("bytes";"context";"fmt";"strings";"sync";"testing";"time"; rt "github.com/bashsharp/bashsharp/engine/lower/shellrt"
IMPORTS
)
type lockedOutput struct { sync.Mutex; bytes.Buffer }
func(b *lockedOutput)Write(p []byte)(int,error){b.Lock();defer b.Unlock();return b.Buffer.Write(p)}
func(b *lockedOutput)WriteString(s string)(int,error){b.Lock();defer b.Unlock();return b.Buffer.WriteString(s)}
func TestEntries(t *testing.T) {
 cases:=[]struct{name string; run func(...rt.SessionOption)(int,error); stdout,stderr string;status int}{ENTRIES}
 for _,tc:=range cases {t.Run(tc.name,func(t *testing.T){
  var out,written lockedOutput
  ctx,cancel:=context.WithTimeout(context.Background(),2*time.Second);defer cancel()
  status,err:=tc.run(rt.WithContext(ctx),rt.WithStdio(nil,&out,&written))
  program:=written.String()
  rendered:=""
  if err!=nil {rendered=fmt.Sprintln(err)}
  diagnostic:=program+rendered
  if ctx.Err()!=nil {t.Errorf("invocation exceeded deadline: %v",ctx.Err())}
  if status!=tc.status || out.String()!=tc.stdout || diagnostic!=tc.stderr {
   t.Errorf("status=%d stdout=%q stderr(program writes)=%q stderr(entry error)=%q; want status=%d stdout=%q stderr=%q",status,out.String(),program,rendered,tc.status,tc.stdout,tc.stderr)
  }
  if rendered!="" && strings.Contains(program,strings.TrimSuffix(rendered,"\n")) {
   t.Errorf("entry error %q duplicates a diagnostic the program already wrote: %q",rendered,program)
  }
 })}
}
`
