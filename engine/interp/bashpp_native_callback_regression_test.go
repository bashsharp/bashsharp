//go:build full

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGoSourceCallbackEffects(t *testing.T) {
	for name, source := range map[string]string{
		"large_return_once": `package main
import "fmt"
import "strings"
type Tag int
var calls int
func(t Tag)String()string{calls++;return strings.Repeat("x",40000)}
func main(){s:=fmt.Sprint(Tag(1));fmt.Println(len(s),calls)}`,

		"wrapped_callback": `package main
import "fmt"
type Tag int
func(t Tag)Error()string{return "original"}
func main(){err:=fmt.Errorf("wrap: %w",Tag(1));fmt.Println(err.Error())}`,
		"tagged_struct": `package main
import "fmt"
import "encoding/json"
type Record struct{Value string ` + "`json:\"label\"`" + `}
func main(){data,err:=json.Marshal(Record{"kept"});fmt.Printf("%s %v\n",data,err)}`,
		"nested_exit": `package main
import "fmt"
import "os"
type Tag int
func(t Tag)String()string{os.Exit(7);return "late"}
func main(){fmt.Println(Tag(1));fmt.Println("after")}`,
		"concurrent_tasks": `package main
import "fmt"
type Tag int
func(t Tag)String()string{return fmt.Sprint(int(t))}
func main(){ch:=make(chan string,2);go func(){v:=fmt.Sprint(Tag(7));ch<-v}();go func(){v:=fmt.Sprint(Tag(7));ch<-v}();a:=<-ch;b:=<-ch;fmt.Println(a,b)}`,
		"delayed_callback_reply": `package main
import("fmt";"time")
type Tag int
func(t Tag)String()string{time.Sleep(20*time.Millisecond);return fmt.Sprint("tag",int(t))}
func main(){fmt.Println(Tag(5));fmt.Println("after")}`,
		"pointer_mutation": `package main
import "fmt"
type Counter struct{ N int }
func(c *Counter)String()string{c.N++;return fmt.Sprint(c.N)}
func main(){c:=Counter{};fmt.Println(&c,&c);fmt.Println(c.N)}`,
		"nested_reentry": `package main
import "fmt"
type Inner int
type Outer int
func(i Inner)String()string{return "inner"}
func(o Outer)String()string{return fmt.Sprint(Inner(1))}
func main(){fmt.Println(Outer(1))}`,
		"fmt_panic": `package main
import "fmt"
type Bad int
func(b Bad)String()string{panic("boom")}
func main(){fmt.Println(Bad(1));fmt.Println("after")}`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}

func TestGoSourceCallbackCancelReset(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output := &callbackCancelWriter{cancel: cancel}
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, output, output))
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
import "fmt"
import "time"
type Tag int
func(t Tag)String()string{println("callback-ready");time.Sleep(time.Minute);return "late"}
func main(){fmt.Println(Tag(1));println("after")}`
	load := func(source string) *gosource.Program {
		t.Helper()
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	err = runner.Run(ctx, load(source).File)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v, output %q", err, output.String())
	}
	if strings.Contains(output.String(), "late") || strings.Contains(output.String(), "after") {
		t.Fatalf("continued after cancellation: %q", output.String())
	}
	runner.Reset()
	output.Reset()
	source = `package main
import "fmt"
type Tag int
func(t Tag)String()string{return "reset-ok"}
func main(){fmt.Println(Tag(1))}`
	if err := runner.Run(context.Background(), load(source).File); err != nil {
		t.Fatal(err)
	}
	if output.String() != "reset-ok\n" {
		t.Fatalf("reset state %q", output.String())
	}
	if files, _ := filepath.Glob(filepath.Join(dir, ".bashpp*")); len(files) > 0 {
		t.Fatalf("leaked helper %v", files)
	}
}

type callbackCancelWriter struct {
	mu sync.Mutex
	bytes.Buffer
	cancel context.CancelFunc
	once   sync.Once
}

func (w *callbackCancelWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.Buffer.Write(p)
	ready := strings.Contains(w.Buffer.String(), "callback-ready")
	w.mu.Unlock()
	if ready {
		w.once.Do(func() { time.AfterFunc(100*time.Millisecond, w.cancel) })
	}
	return n, err
}
func (w *callbackCancelWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Buffer.String()
}
func (w *callbackCancelWriter) Reset() { w.mu.Lock(); defer w.mu.Unlock(); w.Buffer.Reset() }

func TestGoSourceCallbackReferenceBoundary(t *testing.T) {
	// supported cases run the original body faithfully since the S153.2 full
	// method mirror: a mirrored fmt.Formatter writes through the dependency-
	// owned State, and a typed-nil pointer receiver runs the original body
	// with a nil receiver, exactly as native Go invokes it. The remaining
	// case pins the boundary: a value receiver writing through its copied
	// slice storage must stay refused, never silently diverge.
	supported := map[string]bool{"mirrored_formatter": true, "nil_pointer_callback": true}
	for name, source := range map[string]string{
		"mirrored_formatter": `package main
import "fmt"
type V int
func(v V)Format(s fmt.State,verb rune){fmt.Fprint(s,"custom")}
func main(){fmt.Println(V(1));println("after")}`,
		"nil_pointer_callback": `package main
import "fmt"
type V int
func(v *V)String()string{return "nil-original"}
func main(){var v *V;fmt.Println(v);println("after")}`,
		"value_slice_receiver": `package main
import "fmt"
type Slice []int
func(s Slice)String()string{s[0]++;return "changed"}
func main(){s:=Slice{1};fmt.Println(s);println("after")}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			want := runNativeOracle(t, dir, path, nil, "")
			if want.status != 0 {
				t.Fatalf("invalid oracle: %+v", want)
			}
			p, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			err = r.Run(context.Background(), p.File)
			if supported[name] {
				if err != nil || out.String() != want.stdout+want.stderr {
					t.Fatalf("supported case diverged: %v %q; native %+v", err, out.String(), want)
				}
				return
			}
			if err == nil || strings.Contains(out.String(), "after") || strings.Contains(out.String(), "changed") {
				t.Fatalf("unsupported execution accepted: %v %q", err, out.String())
			}
			if !strings.Contains(fmt.Sprint(err)+out.String(), "reference") && !strings.Contains(fmt.Sprint(err)+out.String(), "original method") {
				t.Fatalf("unrelated failure: %v %q", err, out.String())
			}
		})
	}
}

// A panic inside a String method called back from fmt is recovered by fmt
// itself (`%!v(PANIC=String method: …)`) and the program continues. Since
// Sprint 162 an out-of-range index is Go's runtime panic in the interpreter
// too, so the interpreted program must match the native oracle exactly:
// the placeholder line, then "after", exit status 0.
func TestGoSourceCallbackBodyFailure(t *testing.T) {
	source := `package main
import "fmt"
type V int
func(v V)String()string{var xs []int;return fmt.Sprint(xs[0])}
func main(){fmt.Println(V(1));println("after")}`
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	want := runNativeOracle(t, dir, path, nil, "")
	if want.status != 0 {
		t.Fatalf("native fmt did not recover: %+v", want)
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
	if err != nil || output.String() != want.stdout+want.stderr {
		t.Fatalf("interpreted run differs from the native oracle: err=%v\ngot:  %q\nwant: %q", err, output.String(), want.stdout+want.stderr)
	}
}

func TestGoSourceCallbackUnsupportedBodyPropagation(t *testing.T) {
	source := `package main
import("fmt";"sort")
type V int
func(v V)String()string{println("entered");sort.Ints([]int{2,1});return "finished"}
func main(){fmt.Println(V(1));println("after")}`
	differGoSource(t, source, nil, "")
}
