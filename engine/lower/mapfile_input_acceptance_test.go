//go:build full

package lower

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The Bash++ source below is the acceptance program: a task that reads with
// mapfile from non-regular input must refuse promptly rather than block, so a
// failing sibling can cancel the group. The compiled artifact has to reach the
// same refusal, on a pipe, with no source on disk.
const mapfileInputSource = `
func blocked() { mapfile values; }
func fail() { return 7; }
func main() { go blocked(); go fail(); }
main()
`

// These are the measured public interpreter results for mapfileInputSource,
// recorded from the source oracle below. They are asserted before anything is
// compiled, so a change in the public source contract fails here rather than
// silently redefining what the artifact is compared against.
const (
	mapfileInputWantStdout = ""
	mapfileInputWantStderr = "mapfile: blocking non-regular input is unavailable inside a Bash++ task\nbash++: task failed: exit status 2\n"
	mapfileInputWantStatus = 2
)

func TestMapfileInputAcceptance(t *testing.T) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(mapfileInputSource), "input.bpp")
	if err != nil {
		t.Fatal(err)
	}
	out, diagnostic, status := mapfileInputOracle(t, file)
	t.Logf("interpreter stdout=%q stderr=%q status=%d", out, diagnostic, status)
	if out != mapfileInputWantStdout || diagnostic != mapfileInputWantStderr || status != mapfileInputWantStatus {
		t.Fatalf("public source contract changed: stdout=%q stderr=%q status=%d; want stdout=%q stderr=%q status=%d",
			out, diagnostic, status, mapfileInputWantStdout, mapfileInputWantStderr, mapfileInputWantStatus)
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
	entries := fmt.Sprintf("{%q,unit.Execute,%q,%q,%d},", "mapfile_input", out, diagnostic, status)
	hostSrc := strings.ReplaceAll(strings.ReplaceAll(acceptanceEntryPipeHost, "IMPORTS", `unit "entryartifact/unit"`), "ENTRIES", entries)
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

// mapfileInputOracle runs the source against a pipe whose write end stays
// open, which is exactly the non-regular input the refusal is about. The
// bounded context is the proof of promptness: a blocking read exhausts it.
func mapfileInputOracle(t *testing.T, file *syntax.File) (string, string, int) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	defer read.Close()

	var out, diagnostic cancellationOutput
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(read, &out, &diagnostic), interp.Env(expand.ListEnviron("PATH=/no-tools")), interp.Dir(t.TempDir()))
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

// acceptanceEntryPipeHost is acceptanceEntryHost with the same explicit caller
// boundary as the generated process main, feeding the entry a pipe whose write
// end stays open so the artifact faces the same non-regular input the source
// oracle did. Program stderr writes and the returned entry error stay
// distinguishable in the comparison and in the failure report.
const acceptanceEntryPipeHost = `package host_test
import("bytes";"context";"fmt";"os";"strings";"sync";"testing";"time"; rt "github.com/bashsharp/bashsharp/engine/lower/shellrt"
IMPORTS
)
type lockedOutput struct { sync.Mutex; bytes.Buffer }
func(b *lockedOutput)Write(p []byte)(int,error){b.Lock();defer b.Unlock();return b.Buffer.Write(p)}
func(b *lockedOutput)WriteString(s string)(int,error){b.Lock();defer b.Unlock();return b.Buffer.WriteString(s)}
func TestEntries(t *testing.T) {
 cases:=[]struct{name string; run func(...rt.SessionOption)(int,error); stdout,stderr string;status int}{ENTRIES}
 for _,tc:=range cases {t.Run(tc.name,func(t *testing.T){
  read,write,err:=os.Pipe()
  if err!=nil {t.Fatal(err)}
  defer write.Close()
  defer read.Close()

  var out,written lockedOutput
  ctx,cancel:=context.WithTimeout(context.Background(),2*time.Second);defer cancel()
  status,err:=tc.run(rt.WithContext(ctx),rt.WithStdio(read,&out,&written))
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
