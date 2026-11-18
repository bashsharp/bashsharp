//go:build full

package interp_test

// Sprint: #118; Story: #50; Story-ID: cf81e4868348
// Every positive assertion below executes the unchanged source through Runner.
// A native Go build/run is the oracle, never an interpreted result substitute.
import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourcePersistentNativeBridge(t *testing.T) {
	cases := map[string]string{
		"blank_constraints": `package main
import _ "cmp"
func main(){}
`,
		"stdin_and_arguments": `package main
import "io"
import "os"
import "fmt"
func main(){data,_:=io.ReadAll(os.Stdin);fmt.Printf("%s %v\n",data,os.Args)}
`,
		"subprocess_environment": `package main
import "os"
import "os/exec"
import "fmt"
func main(){os.Setenv("S118_BRIDGE_CHILD","inherited");cmd:=exec.Command("/bin/sh","-c","printf %s \"$S118_BRIDGE_CHILD\"");output,_:=cmd.Output();fmt.Printf("%s\n",output)}
`,
		"tuple_argument": `package main
import "fmt"
func split(sum int)(x,y int){x=sum*4/9;y=sum-x;return}
func main(){fmt.Println(split(17))}
`,
		"named_scalar": `package main
import t "time"
import "fmt"
var x t.Duration = 2
var y = t.Second
func main(){fmt.Printf("%T %v %v\n",x,x,y)}
`,
		"typed_map_struct": `package main
import "fmt"
func main(){fmt.Printf("%T %v %T %v\n",map[int]string{1:"one"},map[int]string{1:"one"},struct{Name string}{"Ada"},struct{Name string}{"Ada"})}
`,
		"nil_error": `package main
import "os"
import "fmt"
func main(){if err:=os.Setenv("S118_NIL_ERROR","yes");err!=nil{panic(err)};fmt.Println(os.Getenv("S118_NIL_ERROR"))}
`,
		"environment": `package main
import "os"
import "fmt"
func main(){os.Setenv("S118_BRIDGE_STATE", "persisted"); fmt.Println(os.Getenv("S118_BRIDGE_STATE"))}
`,
		"scalar_result": `package main
import "fmt"
func main(){message:=fmt.Sprintf("%s:%d", "value", 7); fmt.Println(message)}
`,
		"interpreter_effect_order": `package main
import "fmt"
var counter int
func next() string {counter++;return fmt.Sprint(counter)}
func main(){fmt.Println(next(),next(),counter)}
`,
		"typed_slice": `package main
import "fmt"
func main(){fmt.Printf("%T %v\n", []int{1,2}, []int{3,4})}
`,
		"random_handles": `package main
import "math/rand"
import "fmt"
func main(){r:=rand.New(rand.NewSource(7)); fmt.Println(r.Intn(100),r.Intn(100))}
`,
		"time_handle": `package main
import "time"
import "fmt"
func main(){now:=time.Date(2020,time.January,2,3,4,5,0,time.UTC);fmt.Printf("%T %d\n",now,now.Year())}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "subprocess_environment" {
				if _, err := os.Stat("/bin/sh"); err != nil {
					t.Skip("requires /bin/sh")
				}
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
			oracle := filepath.Join(dir, "oracle")
			build := exec.Command(goBinary, "build", "-p", "2", "-o", oracle, path)
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("oracle build: %v %s", err, output)
			}
			var wantOut, wantErr bytes.Buffer
			native := exec.Command(oracle)
			native.Args = []string{path, "alpha", "beta"}
			native.Stdin = strings.NewReader("incoming")
			native.Dir = dir
			native.Stdout, native.Stderr = &wantOut, &wantErr
			if err := native.Run(); err != nil {
				t.Fatal(err)
			}
			program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var gotOut, gotErr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(strings.NewReader("incoming"), &gotOut, &gotErr), interp.Params("--", "alpha", "beta"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := runner.Run(ctx, program.File); err != nil {
				t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, gotOut.String(), gotErr.String())
			}
			if gotOut.String() != wantOut.String() || gotErr.String() != wantErr.String() {
				t.Fatalf("Runner stdout=%q stderr=%q; oracle stdout=%q stderr=%q", gotOut.String(), gotErr.String(), wantOut.String(), wantErr.String())
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != source {
				t.Fatal("original source changed")
			}
			leftovers, _ := filepath.Glob(filepath.Join(dir, ".bashpp*"))
			if len(leftovers) > 0 {
				t.Fatalf("bridge workspace leaked: %v", leftovers)
			}
			lowered, err := lower.Compile(program.File, lower.Options{Origin: path, Dir: dir})
			if err != nil {
				t.Fatalf("lower: %v", err)
			}
			generated := filepath.Join(dir, "generated.go")
			artifact := filepath.Join(dir, "compiled")
			os.WriteFile(generated, lowered.Source, 0600)
			build = exec.Command(goBinary, "build", "-p", "2", "-o", artifact, generated)
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("compiled build: %v %s", err, output)
			}
			gotOut.Reset()
			gotErr.Reset()
			native = exec.Command(artifact)
			native.Args = []string{path, "alpha", "beta"}
			native.Stdin = strings.NewReader("incoming")
			native.Dir = dir
			native.Stdout, native.Stderr = &gotOut, &gotErr
			if err := native.Run(); err != nil {
				t.Fatal(err)
			}
			if gotOut.String() != wantOut.String() || gotErr.String() != wantErr.String() {
				t.Fatalf("compiled stdout=%q stderr=%q; oracle stdout=%q stderr=%q", gotOut.String(), gotErr.String(), wantOut.String(), wantErr.String())
			}
		})
	}
}

func TestGoSourceBridgeModuleInitialization(t *testing.T) {
	t.Run("blank", func(t *testing.T) { testGoSourceBridgeModuleInitialization(t, false) })
	t.Run("named_with_constraints", func(t *testing.T) { testGoSourceBridgeModuleInitialization(t, true) })
}
func testGoSourceBridgeModuleInitialization(t *testing.T, named bool) {
	moduleDir, runtimeDir := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(moduleDir, "dep"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":     "module example.com/s118bridge\n\ngo 1.26\n",
		"dep/dep.go": "package dep\nimport \"os\"\ntype Constraint interface{~int}\ntype GenericAlias[T any] = []T\nfunc Exported() {}\nfunc Label()string{return \"label\"}\nfunc init(){println(\"dependency\",os.Args[1])}\n",
		"main.go": `package main
import _ "example.com/s118bridge/dep"
var value = mark()
func mark() int {println("global");return 1}
func init(){println("init")}
func main(){println("main",value)}
`,
	}
	if named {
		files["main.go"] = strings.Replace(files["main.go"], `import _`, `import dep`, 1)
		files["main.go"] = strings.Replace(files["main.go"], `println("main",value)`, `label:=dep.Label();println("main",value,label)`, 1)
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(moduleDir, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(moduleDir, "main.go")
	binary := filepath.Join(runtimeDir, "oracle")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-o", binary, ".")
	build.Dir = moduleDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("oracle build: %v %s", err, out)
	}
	var wantOut, wantErr bytes.Buffer
	cmd := exec.Command(binary, "startup-argument")
	cmd.Dir = runtimeDir
	cmd.Stdout = &wantOut
	cmd.Stderr = &wantErr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(files["main.go"]), path, gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(moduleDir)})
	if err != nil {
		t.Fatal(err)
	}
	var gotOut, gotErr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(runtimeDir), interp.GoSourceModuleDir(moduleDir), interp.Params("--", "startup-argument"), interp.StdIO(nil, &gotOut, &gotErr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v stderr=%q", err, gotErr.String())
	}
	if gotOut.String() != wantOut.String() || gotErr.String() != wantErr.String() {
		t.Fatalf("Runner %q/%q oracle %q/%q", gotOut.String(), gotErr.String(), wantOut.String(), wantErr.String())
	}
	for name, source := range files {
		after, err := os.ReadFile(filepath.Join(moduleDir, name))
		if err != nil || string(after) != source {
			t.Fatalf("changed source %s", name)
		}
	}
	for _, dir := range []string{moduleDir, runtimeDir} {
		leftovers, _ := filepath.Glob(filepath.Join(dir, ".bashpp*"))
		if len(leftovers) > 0 {
			t.Fatalf("leaked helper %v", leftovers)
		}
	}
}

type bridgeReadyWriter struct{ cancel context.CancelFunc }

func (w bridgeReadyWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("ready")) {
		time.AfterFunc(100*time.Millisecond, w.cancel)
	}
	return len(p), nil
}

func TestGoSourceBridgeCancellation(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("requires /bin/sh")
	}
	dir := t.TempDir()
	source := `package main
import "os/exec"
import "fmt"
func main(){fmt.Println("ready");exec.Command("/bin/sh","-c","echo $$ > spawned.pid; sleep 60").Run()}
`
	program, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, bridgeReadyWriter{cancel}, nil))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := runner.Run(ctx, program.File); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	pid, err := os.ReadFile(filepath.Join(dir, "spawned.pid"))
	if err != nil {
		t.Fatalf("cancellation must interrupt an active dependency subprocess: %v", err)
	}
	// The dependency worker and the child it launched share an owned process group.
	for attempt := 0; attempt < 20; attempt++ {
		if err := exec.Command("/bin/kill", "-0", strings.TrimSpace(string(pid))).Run(); err != nil {
			break
		}
		if attempt == 19 {
			t.Fatalf("dependency subprocess survived cancellation: %s", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".bashpp*"))
	if len(leftovers) > 0 {
		t.Fatalf("leaked helper %v", leftovers)
	}
}

func TestGoSourceBridgeSessionIsolation(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	key := "S118_ISOLATED_DEPENDENCY_STATE"
	t.Setenv(key, "parent")
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &output, nil))
	if err != nil {
		t.Fatal(err)
	}
	for i, body := range []string{`os.Setenv("S118_ISOLATED_DEPENDENCY_STATE","child");fmt.Println(os.Getenv("S118_ISOLATED_DEPENDENCY_STATE"))`, `fmt.Println(os.Getenv("S118_ISOLATED_DEPENDENCY_STATE"))`} {
		if i > 0 {
			runner.Reset()
		}
		source := "package main\nimport \"os\"\nimport \"fmt\"\nfunc main(){" + body + "}\n"
		program, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = runner.Run(ctx, program.File)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	if output.String() != "child\nparent\n" {
		t.Fatalf("session state leaked: %q", output.String())
	}
	if os.Getenv(key) != "parent" {
		t.Fatal("dependency changed host environment")
	}
}

type bridgeForkWriter struct {
	fork   func() error
	called bool
	err    error
}

func (w *bridgeForkWriter) Write(p []byte) (int, error) {
	if !w.called && bytes.Contains(p, []byte("fork")) {
		w.called = true
		w.err = w.fork()
	}
	return len(p), w.err
}

func TestGoSourceBridgePublicSubshellIsolation(t *testing.T) {
	t.Setenv("S118_FORK_STATE", "outside")
	dir := t.TempDir()
	parse := func(source string) *gosource.Program {
		t.Helper()
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	parentProgram := parse(`package main
import "os"
import "fmt"
func main(){os.Setenv("S118_FORK_STATE","parent-session");println("fork");fmt.Println(os.Getenv("S118_FORK_STATE"))}
`)
	childProgram := parse(`package main
import "os"
import "fmt"
func main(){os.Setenv("S118_FORK_STATE","child-session");fmt.Println(os.Getenv("S118_FORK_STATE"))}
`)
	var parentOutput, childOutput bytes.Buffer
	hook := &bridgeForkWriter{}
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &parentOutput, hook))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// This synchronous builtin write suspends the interpreter. Subshell is not
	// invoked concurrently with mutation of the parent's interpreter state.
	hook.fork = func() error {
		child := runner.Subshell()
		child.Reset()
		if err := interp.StdIO(nil, &childOutput, nil)(child); err != nil {
			return err
		}
		return child.Run(ctx, childProgram.File)
	}
	if err := runner.Run(ctx, parentProgram.File); err != nil {
		t.Fatalf("parent Run: %v; child: %v", err, hook.err)
	}
	if !hook.called || hook.err != nil {
		t.Fatalf("fork hook called=%v err=%v", hook.called, hook.err)
	}
	if parentOutput.String() != "parent-session\n" || childOutput.String() != "child-session\n" {
		t.Fatalf("parent=%q child=%q", parentOutput.String(), childOutput.String())
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".bashpp*"))
	if len(leftovers) > 0 {
		t.Fatalf("leaked sessions %v", leftovers)
	}
}
