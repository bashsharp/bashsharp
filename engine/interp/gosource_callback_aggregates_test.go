//go:build full

package interp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func callbackTourModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	mod := "module example.com/callback-test\n\ngo 1.27\nrequire (\n golang.org/x/tour v0.1.0\n github.com/bashsharp/bashsharp v0.0.0\n)\nreplace github.com/bashsharp/bashsharp => " + filepath.ToSlash(repo) + "\n"
	for name, text := range map[string]string{"go.mod": mod, "go.sum": "golang.org/x/tour v0.1.0 h1:OWzbINRoGf1wwBhKdFDpYwM88NM0d1SL/Nj6PagS6YE=\ngolang.org/x/tour v0.1.0/go.mod h1:DUZC6G8mR1AXgXy73r8qt/G5RsefKIlSj6jBMc8b9Wc=\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func callbackTourLoad(t *testing.T, dir, source string) *gosource.Program {
	t.Helper()
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func callbackTourThreeModes(t *testing.T, source string) {
	t.Helper()
	dir := callbackTourModule(t)
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run := func(binary string) (string, string, error) {
		cmd := exec.CommandContext(ctx, binary)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH=")
		var out, errs bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errs
		err := cmd.Run()
		return out.String(), errs.String(), err
	}
	build := func(source, binary string) {
		cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-p", "2", "-o", binary, source)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
	}
	oracle := filepath.Join(dir, "oracle")
	build(path, oracle)
	wantOut, wantErr, err := run(oracle)
	if err != nil {
		t.Fatalf("native: %v %s", err, wantErr)
	}
	p := callbackTourLoad(t, dir, source)
	var out, errs bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &out, &errs))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(ctx, p.File); err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, out.String(), errs.String())
	}
	if out.String() != wantOut || errs.String() != wantErr {
		t.Fatalf("Runner differs: out=%q err=%q; native out=%q err=%q", out.String(), errs.String(), wantOut, wantErr)
	}
	res, err := lower.Compile(p.File, lower.Options{Origin: path, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	generated, binary := filepath.Join(dir, "generated.go"), filepath.Join(dir, "compiled")
	if err = os.WriteFile(generated, res.Source, 0600); err != nil {
		t.Fatal(err)
	}
	build(generated, binary)
	if after, err := os.ReadFile(path); err != nil || string(after) != source {
		t.Fatal("original bytes changed")
	}
	for _, name := range []string{path, generated} {
		if err = os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	gotOut, gotErr, err := run(binary)
	if err != nil || gotOut != wantOut || gotErr != wantErr {
		t.Fatalf("compiled differs: %v out=%q err=%q", err, gotOut, gotErr)
	}
}
func TestGoSourceTourCallbackAggregatesThreeModes(t *testing.T) {
	for name, digest := range map[string]string{
		"exercise-maps": "a520696047b06ca711783426e6c00e9673f8eda06cf3b6180a72a6b8cfc3a362",
		"maps":          "2573eaf263713147101f200ec239d1cc7db9657cc95d30667f5c2fb7087ff12c",
		"slices":        "95119b8cd62eb8b0998ad80be30fe642931f52a06ddf80676c684f4438728d40",
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/gosource-callback-aggregates/" + name + ".go.txt")
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(raw)) != digest {
				t.Fatal("original fixture digest changed")
			}
			callbackTourThreeModes(t, string(raw))
		})
	}
}
func TestGoSourceNativeReaderAssignableThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"interface_field": `package main
import("fmt";"io";"strings")
type holder struct{ Reader io.Reader }
func main(){r:=strings.NewReader("ABC");h:=holder{r};b:=make([]byte,2);n,e:=h.Reader.Read(b);fmt.Println(n,e,b,r.Len())}`,
		"typed_nil": `package main
import("fmt";"io";"strings")
type holder struct{ Reader io.Reader }
func main(){var p *strings.Reader;h:=holder{p};fmt.Println(p==nil,h.Reader==nil)}`,
	} {
		t.Run(name, func(t *testing.T) { callbackTourThreeModes(t, source) })
	}
}

func TestGoSourceTourCallbackEffectsThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"nested_imports_state": `package main
import("fmt";"strings";"golang.org/x/tour/wc")
func main(){calls:=0;wc.Test(func(s string)map[string]int{calls++;m:=make(map[string]int);for _,word:=range strings.Fields(s){m[word]++};return m});fmt.Println("calls",calls)}`,
		"panic": `package main
import("fmt";"golang.org/x/tour/wc")
func main(){defer func(){fmt.Println("recovered",recover())}();wc.Test(func(s string)map[string]int{panic("original callback")});fmt.Println("after")}`,
	} {
		t.Run(name, func(t *testing.T) { callbackTourThreeModes(t, source) })
	}
}
func TestGoSourceTourCallbackCancelReset(t *testing.T) {
	dir := callbackTourModule(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output := &callbackCancelWriter{cancel: cancel}
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, output, output))
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
import("time";"golang.org/x/tour/wc")
func main(){wc.Test(func(s string)map[string]int{println("callback-ready");time.Sleep(time.Minute);return nil});println("after")}`
	if err = r.Run(ctx, callbackTourLoad(t, dir, source).File); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v %q", err, output.String())
	}
	if strings.Contains(output.String(), "after") {
		t.Fatal("execution continued after cancellation")
	}
	r.Reset()
	output.Reset()
	source = `package main
import("strings";"golang.org/x/tour/wc")
func main(){calls:=0;wc.Test(func(s string)map[string]int{calls++;m:=make(map[string]int);for _,word:=range strings.Fields(s){m[word]++};return m});println("fresh",calls)}`
	if err = r.Run(context.Background(), callbackTourLoad(t, dir, source).File); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(output.String(), "fresh 4\n") {
		t.Fatalf("Reset state: %q", output.String())
	}
}

func TestGoSourceTourCallbackReferenceBoundaries(t *testing.T) {
	for name, tc := range map[string]struct{ source, diagnostic string }{
		// reflect.ValueOf only wraps an original function (Pointer, Type and a
		// synchronous Call; see reflectedOriginalFunctionUse), so these
		// boundaries are held by consumers that retain the callback.
		"aggregate_parameter": {`package main
import ("bufio";"strings")
func main(){s:=bufio.NewScanner(strings.NewReader(""));s.Split(func(b []byte,e bool)(int,[]byte,error){println("callback-ran");return 0,nil,nil});println("after")}`, "signature requires value-semantics parameters"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			oracle := runNativeOracle(t, dir, path, nil, "")
			if oracle.status != 0 || oracle.stderr != "after\n" {
				t.Fatalf("native fixture: %+v", oracle)
			}
			p, err := gosource.Parse(strings.NewReader(tc.source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			err = r.Run(context.Background(), p.File)
			if err == nil || !strings.Contains(err.Error()+out.String(), tc.diagnostic) {
				t.Fatalf("missing boundary: %v %q", err, out.String())
			}
			if strings.Contains(out.String(), "callback-ran") || strings.Contains(out.String(), "after") {
				t.Fatalf("unsupported body executed: %q", out.String())
			}
		})
	}
}

// Constructing the closure returned by OnceValue retains its callback, but
// does not invoke it. With no observable invocation, this is ordinary Go
// execution rather than a callback boundary the interpreter must reject.
func TestGoSourceTourCallbackNonInvokingConsumer(t *testing.T) {
	callbackTourThreeModes(t, `package main
import "sync"
func main(){sync.OnceValue(func()map[string]int{println("callback-ran");return nil});println("after")}`)
}
