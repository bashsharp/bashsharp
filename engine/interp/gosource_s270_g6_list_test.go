//go:build full

package interp_test

// Sprint: #270; Story: #761; Story-ID: c8365c7b8c50

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestSprint270GenericListSentinelPointerIdentity(t *testing.T) {
	const src = `package main

type Element[T any] struct {
	next, prev *Element[T]
	list *List[T]
	Value T
}

func (e *Element[T]) Prev() *Element[T] {
	if p := e.prev; e.list != nil && p != &e.list.root {
		return p
	}
	return nil
}

type List[T any] struct {
	root Element[T]
	len int
}

func (l *List[T]) Init() *List[T] {
	l.root.next = &l.root
	l.root.prev = &l.root
	return l
}

func New[T any]() *List[T] { return new(List[T]).Init() }

func (l *List[T]) PushFront(v T) *Element[T] {
	e := &Element[T]{Value: v}
	e.prev = &l.root
	e.next = l.root.next
	e.prev.next = e
	e.next.prev = e
	e.list = l
	l.len++
	return e
}

func main() {
	l := New[string]()
	e := l.PushFront("a")
	if e.Prev() != nil {
		panic("front Prev returned sentinel")
	}
}
`
	differGoSource(t, src, nil, "")
}

func TestSprint270Go127TypeparamList2Root(t *testing.T) {
	source := readGo127Typeparam(t, "list2.go")
	differGoSource(t, source, nil, "")
}

func TestSprint270Go127TypeparamListimp2Root(t *testing.T) {
	dir := filepath.Join(runtime.GOROOT(), "test", "typeparam", "listimp2.dir")
	mainSource := gosource.Source{Name: "main.go", Data: []byte(readFile(t, filepath.Join(dir, "main.go")))}
	depSource := gosource.Source{Name: "a.go", Data: []byte(readFile(t, filepath.Join(dir, "a.go")))}
	program, err := gosource.Load([]gosource.Source{mainSource}, gosource.Options{
		RunMain:    true,
		ImportBase: "test",
		ImportPath: "test/main",
		Packages: []gosource.PackageSpec{{
			Path:    "test/a",
			Sources: []gosource.Source{depSource},
		}},
	})
	if err != nil {
		t.Fatalf("gosource.Load: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	var status interp.ExitStatus
	if errors.As(err, &status) {
		t.Fatalf("Runner exit %d; stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	if err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func readGo127Typeparam(t *testing.T, name string) string {
	t.Helper()
	return readFile(t, filepath.Join(runtime.GOROOT(), "test", "typeparam", name))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
