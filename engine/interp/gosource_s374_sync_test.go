//go:build full

package interp_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestS374ResidentSyncDifferential(t *testing.T) {
	for name, source := range map[string]string{
		"contended":    `package main;import("sync";"fmt");func main(){var m sync.Mutex;var w sync.WaitGroup;m.Lock();w.Add(1);go func(){m.Lock();m.Unlock();w.Done()}();m.Unlock();w.Wait();fmt.Println("done")}`,
		"wait-in-task": `package main;import("sync";"fmt");func main(){var w sync.WaitGroup;w.Add(1);done:=make(chan bool);go func(){w.Wait();done<-true}();w.Done();fmt.Println(<-done)}`,
		"embedded":     `package main;import("sync";"fmt");type Box struct{sync.Mutex;wg sync.WaitGroup;n int};func main(){b:=new(Box);b.wg.Add(2);for i:=0;i<2;i++ {go func(){b.Lock();b.n++;b.Unlock();b.wg.Done()}()};b.wg.Wait();fmt.Println(b.n)}`,
		"pointers":     `package main;import("sync";"fmt");func lock(m *sync.Mutex){m.Lock();defer m.Unlock()};func main(){m:=new(sync.Mutex);p:=m;lock(p);fmt.Println(m.TryLock(),p.TryLock());p.Unlock();var wg sync.WaitGroup;wg.Add(1);go func(){wg.Done()}();wg.Wait();wg.Add(1);go func(){wg.Done()}();wg.Wait();fmt.Println("done")}`,
		"literal":      `package main;import("sync";"fmt");func main(){m:=&sync.Mutex{};m.Lock();m.Unlock();w:=&sync.WaitGroup{};w.Add(1);go func(){w.Done()}();w.Wait();fmt.Println("done")}`,
		"escape":       `package main;import("sync";"reflect";"fmt");func main(){var m sync.Mutex;reflect.ValueOf(&m).MethodByName("Lock").Call(nil);m.Unlock();fmt.Println(m.TryLock());m.Unlock()}`,
		"negative-add": `package main;import("sync";"fmt");func main(){defer func(){fmt.Println(recover())}();var w sync.WaitGroup;w.Add(-1)}`,
	} {
		t.Run(name, func(t *testing.T) { s374SyncDifferential(t, source) })
	}
}

func BenchmarkS374ResidentSync(b *testing.B) {
	for _, domain := range []string{"native", "resident"} {
		b.Run(domain, func(b *testing.B) {
			source := fmt.Sprintf(`package main;import("sync";"time";"fmt");func main(){var m sync.Mutex;m.Lock();m.Unlock();start:=time.Now();for i:=0;i<%d;i++{m.Lock();m.Unlock()};fmt.Println(time.Since(start).Nanoseconds())}`, b.N)
			p, err := gosource.Parse(strings.NewReader(source), "syncbench.go", gosource.Options{RunMain: true})
			if err != nil {
				b.Fatal(err)
			}
			seen := 0
			syntax.Walk(p.File, func(n syntax.Node) bool {
				if t, ok := n.(*syntax.BashPPNamedType); ok && t.LocalSync != "" {
					seen++
					if domain == "native" {
						t.LocalSync = ""
					}
				}
				return true
			})
			if seen == 0 {
				b.Fatal("missing sync certificate")
			}
			var out, stderr bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(b.TempDir()), interp.StdIO(nil, &out, &stderr))
			if err != nil {
				b.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err = r.Run(ctx, p.File); err != nil {
				b.Fatalf("%v: %s", err, stderr.String())
			}
			elapsed, err := strconv.ParseInt(strings.TrimSpace(out.String()), 10, 64)
			if err != nil || elapsed <= 0 {
				b.Fatalf("duration %q: %v", out.String(), err)
			}
			b.ReportMetric(float64(elapsed)/float64(b.N), "ns/op")
			b.ReportMetric(float64(b.N)*1e9/float64(elapsed), "pairs/s")
		})
	}
}

// Use the requested go run oracle and compare both output streams exactly.
func s374SyncDifferential(t *testing.T, source string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var want, wantErr bytes.Buffer
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "run", path)
	cmd.Stdout = &want
	cmd.Stderr = &wantErr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go run: %v %s", err, wantErr.String())
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
	if err = r.Run(ctx, p.File); err != nil {
		t.Fatalf("Runner: %v stdout=%q stderr=%q", err, out.String(), stderr.String())
	}
	if out.String() != want.String() || stderr.String() != wantErr.String() {
		t.Fatalf("Runner %q/%q; go run %q/%q", out.String(), stderr.String(), want.String(), wantErr.String())
	}
}

func TestS374ResidentSyncCertificates(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         bool
	}{
		{"local", `package main;import "sync";func main(){var m sync.Mutex;m.Lock();m.Unlock()}`, true},
		{"new", `package main;import s "sync";func main(){m:=new(s.Mutex);m.Lock();m.Unlock()}`, true},
		{"embedded", `package main;import "sync";type B struct{sync.Mutex};func main(){b:=new(B);b.Lock();b.Unlock()}`, true},
		{"native", `package main;import("sync";"reflect");func main(){var m sync.Mutex;_=reflect.ValueOf(&m)}`, false},
		{"opaque", `package main;import "sync";func main(){var m sync.Mutex;var a any=&m;_=a}`, false},
		{"callback", `package main;import("sync";"time");var m sync.Mutex;func main(){time.AfterFunc(0,func(){m.Lock();m.Unlock()})}`, false},
		{"indirect-field", `package main;import "sync";var m sync.Mutex;func main(){b:=struct{f func()}{func(){m.Lock();m.Unlock()}};b.f()}`, false},
		{"method-value", `package main;import "sync";func main(){var m sync.Mutex;f:=m.Lock;f();m.Unlock()}`, false},
		{"method-expression", `package main;import "sync";func main(){var m sync.Mutex;(*sync.Mutex).Lock(&m);m.Unlock()}`, false},
		{"copy", `package main;import "sync";func main(){var m sync.Mutex;n:=m;_=n}`, false},
		{"aggregate-copy", `package main;import "sync";type B struct{sync.Mutex};func main(){var b B;c:=b;_=c}`, false},
		{"unsupported", `package main;import "sync";func main(){var w sync.WaitGroup;g:=w.Go;g(func(){});w.Wait()}`, false},
		{"comparison", `package main;import "sync";func main(){a:=new(sync.Mutex);b:=a;_=a==b}`, false},
		{"value-param", `package main;import "sync";func use(m sync.Mutex){};func main(){use(sync.Mutex{})}`, false},
		{"unsafe", `package main;import("sync";"unsafe");func main(){var m sync.Mutex;_=unsafe.Pointer(&m)}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(tc.source), "proof.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			certified := false
			syntax.Walk(p.File, func(n syntax.Node) bool {
				if x, ok := n.(*syntax.BashPPNamedType); ok && x.LocalSync != "" {
					certified = true
				}
				return true
			})
			if certified != tc.want {
				t.Fatalf("certificate=%v, want %v", certified, tc.want)
			}
		})
	}
}
