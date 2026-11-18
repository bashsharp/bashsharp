//go:build full

// Sprint: #248; Story: #702; Story-ID: f330582c10c8

package interp

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// runS248GoSource runs one program and counts, per step, the reflected-method
// chain steps answered in the interpreter ("local:…") and the requests
// forwarded to the dependency helper (by selector).
func runS248GoSource(t *testing.T, source string) (stdout, stderr string, steps map[string]int, err error) {
	t.Helper()
	var mu sync.Mutex
	steps = map[string]int{}
	goSourceReflectTrace = func(step string) {
		mu.Lock()
		steps[step]++
		mu.Unlock()
	}
	defer func() { goSourceReflectTrace = nil }()
	var out, errout bytes.Buffer
	r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err = r.Run(ctx, p.File)
	mu.Lock()
	defer mu.Unlock()
	counts := make(map[string]int, len(steps))
	for k, v := range steps {
		counts[k] = v
	}
	return out.String(), errout.String(), counts, err
}

// TestS248ReflectedMethodValueConcurrent is the outside-corpus shape of
// fixedbugs/issue27695.go: 20 goroutines each build
// reflect.ValueOf(&T{}).MethodByName("Run").Interface() per iteration, assert
// it to its func type and call it. The result must equal the native answer
// (receiver identity and writes observed through the original pointer, the
// []byte argument delivered), and the chain must not cross the dependency
// helper per iteration: at most one bootstrap per racing goroutine.
func TestS248ReflectedMethodValueConcurrent(t *testing.T) {
	const goroutines, iterations = 20, 20
	src := `package main

import (
	"fmt"
	"reflect"
	"sync"
)

type Stt struct{ Data interface{} }

type My struct{ b byte }

func (this *My) Run(raw []byte) (Stt, error) {
	this.b += byte(len(raw)) + 1
	return Stt{Data: "hello"}, nil
}

func one() (int, interface{}) {
	m := &My{}
	f := reflect.ValueOf(m).MethodByName("Run")
	method, ok := f.Interface().(func([]byte) (Stt, error))
	if !ok {
		return -1, nil
	}
	s, e := method([]byte("ab"))
	if e != nil {
		return -2, nil
	}
	return int(m.b), interface{}(s)
}

func main() {
	var wg sync.WaitGroup
	var mu sync.Mutex
	sum, hello := 0, 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				b, v := one()
				mu.Lock()
				sum += b
				if v.(Stt).Data == "hello" {
					hello++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	fmt.Println(sum, hello)
}
`
	stdout, stderr, steps, err := runS248GoSource(t, src)
	if err != nil {
		t.Fatalf("run: %v\nstderr=%q", err, stderr)
	}
	// go run: 1200 400
	if stdout != "1200 400\n" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want the native %q", stdout, stderr, "1200 400\n")
	}
	total := goroutines * iterations
	for _, step := range []string{"local:ValueOf", "local:MethodByName", "local:Interface", "local:call"} {
		if steps[step] != total {
			t.Errorf("%s answered %d times, want %d (steps %v)", step, steps[step], total, steps)
		}
	}
	for _, step := range []string{"reflect.ValueOf", "MethodByName", "Interface", ""} {
		if steps[step] > goroutines {
			t.Errorf("%q forwarded to the helper %d times for %d iterations (steps %v)", step, steps[step], total, steps)
		}
	}
}

// TestS248ReflectedMethodValueMaterialized pins every other use of the lazy
// values to the native answer: reflect operations replay the chain against
// the helper and observe the pointee as it is at use (the eager bridge read a
// snapshot from ValueOf time: "3 1"); a missing, unexported or value-receiver
// selection is the helper's; Method(i) indexes the sorted exported set; and
// the func handed to fmt is the helper's reflected func.
func TestS248ReflectedMethodValueMaterialized(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type C struct{ n int }

func (c *C) Inc(d int) int { c.n += d; return c.n }
func (c *C) Get() int      { return c.n }
func (c C) Val() int       { return c.n * 10 }
func (c *C) hidden()       {}

func main() {
	p := &C{n: 1}
	v := reflect.ValueOf(p)
	p.n = 5
	fmt.Println(v.Kind(), v.Type(), v.NumMethod(), v.Elem().Field(0).Int())
	fmt.Println(v.MethodByName("Inc").Call([]reflect.Value{reflect.ValueOf(2)})[0].Int(), p.n)
	fmt.Println(v.MethodByName("missing").IsValid(), v.MethodByName("hidden").IsValid())
	get := v.Method(0).Interface().(func() int)
	inc := v.MethodByName("Inc").Interface().(func(int) int)
	fmt.Println(get(), inc(3), p.n)
	val := v.MethodByName("Val").Interface().(func() int)
	p.n = 7
	fmt.Println(val(), get())
	x := v.MethodByName("Get").Interface()
	fmt.Printf("%T\n", x)
	_, isInc := x.(func(int) int)
	fmt.Println(isInc, v.MethodByName("Inc").Type())
}
`
	// go run output.
	want := "ptr *main.C 3 5\n7 7\nfalse false\n7 10 10\n70 7\nfunc() int\nfalse func(int) int\n"
	stdout, stderr, steps, err := runS248GoSource(t, src)
	if err != nil {
		t.Fatalf("run: %v\nstderr=%q", err, stderr)
	}
	if stdout != want || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want %q", stdout, stderr, want)
	}
	// Val has a value receiver: its selection stays with the helper.
	if steps["local:call"] != 3 {
		t.Errorf("local calls = %d, want 3 (get twice, inc once; never the value-receiver Val; steps %v)", steps["local:call"], steps)
	}
}

// TestS248ReflectedMethodValueControls: a dependency-owned receiver and an
// original value (not pointer) receiver still go through the helper exactly
// as before, and a lazily built pointer-receiver func handed to an unreviewed
// retaining API is refused as the helper's reflected func always was.
func TestS248ReflectedMethodValueControls(t *testing.T) {
	t.Run("dependency receiver", func(t *testing.T) {
		src := `package main

import (
	"bytes"
	"fmt"
	"reflect"
)

func main() {
	b := bytes.NewBufferString("x")
	f := reflect.ValueOf(b).MethodByName("WriteString").Interface().(func(string) (int, error))
	n, err := f("yz")
	fmt.Println(n, err, b.String())
}
`
		stdout, stderr, steps, err := runS248GoSource(t, src)
		if err != nil || stdout != "2 <nil> xyz\n" {
			t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
		if steps["local:ValueOf"] != 0 || steps["reflect.ValueOf"] != 1 || steps["MethodByName"] != 1 || steps["Interface"] != 1 {
			t.Errorf("dependency receiver was not left to the helper: %v", steps)
		}
	})
	t.Run("value receiver", func(t *testing.T) {
		src := `package main

import (
	"fmt"
	"reflect"
)

type M int

func (m M) Get() int { return int(m) }

func main() {
	local := M(7)
	f := reflect.ValueOf(local).MethodByName("Get").Interface().(func() int)
	local = 9
	fmt.Println(f(), local)
}
`
		stdout, stderr, steps, err := runS248GoSource(t, src)
		if err != nil || stdout != "7 9\n" {
			t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
		if steps["local:ValueOf"] != 0 || steps["reflect.ValueOf"] != 1 {
			t.Errorf("value receiver was not left to the helper: %v", steps)
		}
	})
	t.Run("retained pointer-receiver func refused", func(t *testing.T) {
		src := `package main

import (
	"reflect"
	"time"
)

type M struct{ n int }

func (m *M) Tick() { m.n++ }

func main() {
	local := &M{}
	f := reflect.ValueOf(local).MethodByName("Tick").Interface().(func())
	time.AfterFunc(time.Second, f)
	println("after")
}
`
		stdout, stderr, _, err := runS248GoSource(t, src)
		if err == nil || !strings.Contains(err.Error()+stderr, "dependency mutation of interpreter-owned references is unsupported for time.AfterFunc") || strings.Contains(stdout+stderr, "after") {
			t.Fatalf("want the retained-callback refusal; stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
	})
}

// TestS248ReflectedMethodValueUnmaterializedRefused: the session itself fails
// closed on a lazy descriptor that bypassed materialisation.
func TestS248ReflectedMethodValueUnmaterializedRefused(t *testing.T) {
	var r *Runner
	var got error
	probed := false
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !strings.Contains(string(p), "probe") {
			return len(p), nil
		}
		probed = true
		req, err := r.bashPPEvalRequest()
		if err != nil {
			got = err
			return len(p), nil
		}
		for _, lazy := range []bashPPBridgeValue{
			{Kind: "handle", Session: req.Bridge.id, Handle: goSourceLocalReflectHandle(), localReflect: &goSourceLocalReflect{}},
			{Kind: "string", Text: "x", localCell: &bashPPCell{}},
		} {
			_, err = req.Bridge.request(context.Background(), req, bashPPBridgeRequest{Op: "call", Selector: "fmt.Sprint", Args: []bashPPBridgeValue{lazy}})
			if err == nil || !strings.Contains(err.Error(), "interpreter-owned reflected value reached the dependency unmaterialized") {
				got = err
				if got == nil {
					got = errNoRefusal
				}
				return len(p), nil
			}
		}
		return len(p), nil
	})
	var err error
	r, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &bytes.Buffer{}, writer))
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
import "fmt"
func main(){_ = fmt.Sprint(1);println("probe")}`
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), p.File); err != nil {
		t.Fatal(err)
	}
	if !probed || got != nil {
		t.Fatalf("lazy descriptor was not refused (probed=%v): %v", probed, got)
	}
}

var errNoRefusal = errorString("request accepted a lazy descriptor")

type errorString string

func (e errorString) Error() string { return string(e) }
