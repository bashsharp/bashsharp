//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceOriginalEnvironment(t *testing.T) {
	source, err := os.ReadFile("testdata/gosource-environment/environment-variables.go")
	if err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string][]string{
		"empty":                 {},
		"absent_shell_values":   {"LAST=last", "BAR=caller-bar", "FIRST=first"},
		"supplied_shell_values": {"LAST=last", "BASH=caller-bash", "SHELL=caller-shell", "SHLVL=17", "UID=caller-uid", "EUID=caller-euid", "IFS=caller-ifs", "OPTIND=caller-optind", "BASH_VERSION=caller-version", "BASHY_AGENT_MANIFEST=caller-manifest", "FIRST=first"},
	} {
		t.Run(name, func(t *testing.T) { testGoSourceEnvironment(t, string(source), env) })
	}
}

func TestGoSourceEnvironmentMutations(t *testing.T) {
	testGoSourceEnvironment(t, `package main
import "fmt"
import "os"
import "os/exec"
func main(){fmt.Println(os.Getenv("KEEP"));os.Setenv("ADDED","value");os.Setenv("KEEP","changed");os.Unsetenv("REMOVE");fmt.Println(os.Environ());cmd:=exec.Command("/usr/bin/env");data,err:=cmd.Output();if err!=nil{panic(err)};fmt.Printf("%s",data);os.Clearenv();fmt.Println(len(os.Environ()))}
`, []string{"KEEP=initial", "REMOVE=discard", "FINAL=last"})
}

func testGoSourceEnvironment(t *testing.T, source string, env []string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	oracle := filepath.Join(dir, "oracle")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-o", oracle, path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native build: %v %s", err, output)
	}
	var wantOut, wantErr bytes.Buffer
	cmd = exec.Command(oracle)
	cmd.Dir = dir
	cmd.Env = append([]string{}, env...)
	cmd.Stdout = &wantOut
	cmd.Stderr = &wantErr
	if err := cmd.Run(); err != nil {
		t.Fatalf("native: %v %s", err, wantErr.String())
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	configured := append([]string{}, env...)
	option := interp.GoSourceEnv(configured)
	// Neither the caller's slice nor host process mutations may alter a snapshot.
	if len(configured) > 0 {
		configured[0] = "FORGED=late"
	}
	before := os.Getenv("ADDED")
	var gotOut, gotErr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &gotOut, &gotErr), option)
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 2; iteration++ {
		if iteration > 0 {
			r.Reset()
			gotOut.Reset()
			gotErr.Reset()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		err = r.Run(ctx, program.File)
		cancel()
		if err != nil {
			t.Fatalf("Runner: %v %s", err, gotErr.String())
		}
		if gotOut.String() != wantOut.String() || gotErr.String() != wantErr.String() {
			t.Fatalf("run %d got %q %q; native %q %q", iteration, gotOut.String(), gotErr.String(), wantOut.String(), wantErr.String())
		}
	}
	if os.Getenv("ADDED") != before {
		t.Fatal("Go program changed embedding process environment")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != source {
		t.Fatal("original source changed")
	}
}

type goEnvironmentWithPrivate struct{ expand.Environ }

func (env goEnvironmentWithPrivate) Get(name string) expand.Variable {
	if name == "PRIVATE" {
		return expand.Variable{Set: true, Kind: expand.String, Str: "not-exported"}
	}
	return env.Environ.Get(name)
}
func (env goEnvironmentWithPrivate) Each(fn func(string, expand.Variable) bool) {
	env.Environ.Each(fn)
	fn("PRIVATE", env.Get("PRIVATE"))
}

func TestGoSourceEnvironmentDefaultAndShellIsolation(t *testing.T) {
	source := `package main
import "fmt"
import "os"
func main(){fmt.Println(os.Environ())}
`
	program, err := gosource.Parse(strings.NewReader(source), "env.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	initial := goEnvironmentWithPrivate{expand.ListEnviron("SHELL=provided-shell", "BASH=provided-bash", "KEY=value", "PRIVATE=overridden-export")}
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Env(initial), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v %s", err, stderr.String())
	}
	wantEnv := "[BASH=provided-bash KEY=value SHELL=provided-shell GOROOT=" + runtime.GOROOT() + " GOTOOLCHAIN=" + runtime.Version() + "]\n"
	if out.String() != wantEnv {
		t.Fatalf("default exported initial environment: %q", out.String())
	}
	out.Reset()
	stderr.Reset()
	// An explicitly empty Go environment must have no effect on ordinary Bash.
	r, err = interp.New(interp.Env(expand.ListEnviron("KEY=shell-value")), interp.StdIO(nil, &out, &stderr), interp.GoSourceEnv([]string{}))
	if err != nil {
		t.Fatal(err)
	}
	shell, err := syntax.NewParser().Parse(strings.NewReader(`export ADDED=shell-added; /usr/bin/env`), "shell.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, shell); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !slices.Contains(lines, "KEY=shell-value") || !slices.Contains(lines, "ADDED=shell-added") {
		t.Fatalf("Go option changed shell environment: %q", out.String())
	}
}

func TestGoSourceDefaultEnvironmentPinsSelectedToolchain(t *testing.T) {
	goRoot := runtime.GOROOT()
	source := `package main
import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)
func main(){
	fmt.Println("runtime", runtime.GOROOT())
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GOROOT=") || strings.HasPrefix(entry, "GOTOOLCHAIN=") {
			fmt.Println("env", entry)
		}
	}
	out, err := exec.Command("go", "env", "GOROOT", "GOTOOLCHAIN").CombinedOutput()
	if err != nil { panic(err) }
	fmt.Printf("child\n%s", out)
}
`
	program, err := gosource.Parse(strings.NewReader(source), "toolchain-env.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Env(expand.ListEnviron("PATH="+os.Getenv("PATH"))), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v %s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for _, want := range []string{
		"runtime " + goRoot,
		"env GOROOT=" + goRoot,
		"env GOTOOLCHAIN=" + runtime.Version(),
		"child",
		goRoot,
		runtime.Version(),
	} {
		if !slices.Contains(lines, want) {
			t.Fatalf("missing %q in output lines %q", want, lines)
		}
	}
}
