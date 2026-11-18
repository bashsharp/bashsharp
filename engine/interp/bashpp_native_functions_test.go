//go:build full

package interp_test

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

func TestGoSourceNativeFunctionThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"returned_closure": `package main
import "fmt"
import "strings"
func mapper()func(rune)rune{n:=rune(0);return func(r rune)rune{n++;return r+n}}
func main(){f:=mapper();fmt.Println(strings.Map(f,"aaa"),strings.Map(f,"a"))}`,
		"once_after_panic": `package main
import "fmt"
import "sync"
func main(){var once sync.Once;func(){defer func(){fmt.Println("recovered",recover())}();once.Do(func(){panic("boom")})}();once.Do(func(){fmt.Println("not-run")});fmt.Println("done")}`,
		"map_closure": `package main
import "fmt"
import "strings"
func main(){calls:=0;mapped:=strings.Map(func(r rune)rune{calls++;return r+1},"abc");fmt.Println(mapped,calls)}`,
		"predicate_value": `package main
import "fmt"
import "strings"
var calls int
func separator(r rune)bool{calls++;return r==','}
func main(){f:=separator;parts:=strings.FieldsFunc("one,two,three",f);fmt.Println(parts,calls);fmt.Println(strings.Join(parts,"/"))}`,
		"once_captured_state": `package main
import "fmt"
import "sync"
func main(){n:=1;var once sync.Once;f:=func(){n=n+4};once.Do(f);once.Do(f);fmt.Println(n)}`,
		"nested_reentry": `package main
import "fmt"
import "strings"
func main(){n:=0;out:=strings.Map(func(r rune)rune{n++;if strings.ContainsFunc("a",func(x rune)bool{return x==r}){return 'X'};return r},"abc");fmt.Println(out,n)}`,
		"panic_recovery": `package main
import "fmt"
import "strings"
func work(){defer func(){fmt.Println("recovered",recover())}();strings.Map(func(r rune)rune{panic("boom")},"abc");fmt.Println("after")}
func main(){work();fmt.Println("done")}`,
		"concurrent_tasks": `package main
import "fmt"
import "strings"
func main(){ch:=make(chan string,2);go func(){v:=strings.Map(func(r rune)rune{return r+1},"abc");ch<-v}();go func(){v:=strings.Map(func(r rune)rune{return r+1},"abc");ch<-v}();a:=<-ch;b:=<-ch;fmt.Println(a,b)}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			want := runNativeOracle(t, dir, path, nil, "")
			got := runGoSourceRunner(t, dir, path, source, nil, "")
			if got != want {
				t.Fatalf("interpreter=%+v native=%+v", got, want)
			}
			program, err := gosource.Parse(bytes.NewBufferString(source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := lower.Compile(program.File, lower.Options{Origin: path})
			if err != nil {
				t.Fatal(err)
			}
			generated, binary := filepath.Join(dir, "generated.go"), filepath.Join(dir, "compiled")
			if runtime.GOOS == "windows" {
				binary += ".exe"
			}
			if err := os.WriteFile(generated, result.Source, 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("go", "build", "-p", "2", "-o", binary, generated).CombinedOutput(); err != nil {
				t.Fatalf("compiled build: %v %s", err, out)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != source {
				t.Fatal("original source changed")
			}
			for _, file := range []string{generated, path} {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(binary)
			cmd.Dir = dir
			cmd.Env = []string{"PATH="}
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("compiled run: %v %s", err, stderr.String())
			}
			if stdout.String() != want.stdout || stderr.String() != want.stderr || want.status != 0 {
				t.Fatalf("compiled streams %q %q native=%+v", stdout.String(), stderr.String(), want)
			}
		})
	}
}

func TestGoSourceFunctionCallbackCancelReset(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output := &callbackCancelWriter{cancel: cancel}
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, output, output))
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
import "fmt"
import "strings"
import "time"
func main(){strings.Map(func(r rune)rune{println("callback-ready");time.Sleep(time.Minute);return r},"a");fmt.Println("after")}`
	load := func(source string) *gosource.Program {
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := r.Run(ctx, load(source).File); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v %q", err, output.String())
	}
	if strings.Contains(output.String(), "after") {
		t.Fatal("continued after canceled callback")
	}
	r.Reset()
	output.Reset()
	source = `package main
import "fmt"
import "strings"
func main(){n:=10;out:=strings.Map(func(r rune)rune{n++;return r},"a");fmt.Println(out,n)}`
	if err := r.Run(context.Background(), load(source).File); err != nil {
		t.Fatal(err)
	}
	if output.String() != "a 11\n" {
		t.Fatalf("callback state survived Reset: %q", output.String())
	}
	if files, _ := filepath.Glob(filepath.Join(dir, ".bashpp*")); len(files) != 0 {
		t.Fatalf("helper leaked: %v", files)
	}
}

// TestGoSourceFunctionCallbackLifetimeBoundary pins where a retained original
// callback still stops, and — since sprint118 story #54 — where it no longer
// does.
//
// The boundary itself is unchanged and is the first half below: a callback
// handle is only sound while the interpreter is blocked inside the request that
// raised it, so a dependency function which RETAINS f and calls it from a
// goroutine it owns is refused. time.AfterFunc is exactly that shape.
//
// `wg.Go(f)` used to be measured here as the same shape, and it is not one.
// It never becomes a native request at all: sync.WaitGroup.Go is answered as a
// bridge operation of its own, which runs the native Add in the launcher, spawns
// the body as an interpreted task, and runs the native Done from inside that
// task (see gosource_waitgroup.go). Nothing is retained by the dependency and no
// original body is forwarded to it. Asserting the refusal here would now be
// asserting a boundary that moved — the second half measures the program
// against the native oracle instead, which is the stronger claim and the one
// that fails if the launch is ever lost, duplicated or forwarded.
func TestGoSourceFunctionCallbackLifetimeBoundary(t *testing.T) {
	t.Run("retained callback is refused", func(t *testing.T) {
		source := `package main
import "time"
func main(){t:=time.AfterFunc(time.Millisecond,func(){println("late")});time.Sleep(10*time.Millisecond);t.Stop();println("after")}`
		dir := t.TempDir()
		path := filepath.Join(dir, "original.go")
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		p, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &output, &output))
		if err != nil {
			t.Fatal(err)
		}
		err = r.Run(context.Background(), p.File)
		if err == nil || !strings.Contains(err.Error()+output.String(), "asynchronous or retained") ||
			strings.Contains(output.String(), "late") || strings.Contains(output.String(), "after") {
			t.Fatalf("retained callback executed: %v %q", err, output.String())
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != source {
			t.Fatal("original source changed")
		}
	})

	t.Run("WaitGroup.Go matches the native oracle", func(t *testing.T) {
		source := `package main
import "sync"
func main(){var wg sync.WaitGroup;wg.Go(func(){println("callback-ran")});wg.Wait();println("after")}`
		dir := t.TempDir()
		path := filepath.Join(dir, "original.go")
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		want := runNativeOracle(t, dir, path, nil, "")
		if want.status != 0 || !strings.Contains(want.stderr, "callback-ran") {
			t.Fatalf("native callback failed: %+v", want)
		}
		p, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &stderr))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Run(context.Background(), p.File); err != nil {
			t.Fatalf("Runner: %v stdout=%q stderr=%q", err, out.String(), stderr.String())
		}
		if out.String() != want.stdout || stderr.String() != want.stderr {
			t.Fatalf("interpreted %q %q; oracle %q %q", out.String(), stderr.String(), want.stdout, want.stderr)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != source {
			t.Fatal("original source changed")
		}
	})
}
