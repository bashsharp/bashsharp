//go:build full

package interp_test

// Sprint: #376; Story: #1549; Story-ID: 91561dbe8537
//
// Narrow tests for the general resident-RWMutex certificate model: the same
// package-wide escape rules as resident Mutex/WaitGroup, extended to the
// RWMutex read/write paths. No per-root code; no fixture edits.

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS376ResidentRWMutexDifferential(t *testing.T) {
	for name, source := range map[string]string{
		"readers":   `package main;import("sync";"fmt");func main(){var m sync.RWMutex;var w sync.WaitGroup;n:=0;for i:=0;i<4;i++{w.Add(1);go func(){m.RLock();_ = n;m.RUnlock();w.Done()}()};w.Wait();m.Lock();n=7;m.Unlock();m.RLock();fmt.Println(n);m.RUnlock()}`,
		"contended": `package main;import("sync";"fmt");func main(){var m sync.RWMutex;var w sync.WaitGroup;m.Lock();w.Add(1);go func(){m.RLock();m.RUnlock();w.Done()}();m.Unlock();w.Wait();fmt.Println("done")}`,
		"writer":    `package main;import("sync";"fmt");func main(){var m sync.RWMutex;var w sync.WaitGroup;m.RLock();w.Add(1);go func(){m.Lock();m.Unlock();w.Done()}();m.RUnlock();w.Wait();fmt.Println("done")}`,
		"embedded":  `package main;import("sync";"fmt");type Box struct{sync.RWMutex;n int};func main(){b:=new(Box);var w sync.WaitGroup;w.Add(2);for i:=0;i<2;i++{go func(){b.RLock();b.n++;b.RUnlock();w.Done()}()};w.Wait();b.Lock();fmt.Println(b.n);b.Unlock()}`,
		"pointers":  `package main;import("sync";"fmt");func read(m *sync.RWMutex){m.RLock();defer m.RUnlock()};func main(){m:=new(sync.RWMutex);p:=m;read(p);fmt.Println(m.TryRLock());m.RUnlock();fmt.Println(m.TryLock());m.Unlock()}`,
		"literal":   `package main;import("sync";"fmt");func main(){m:=&sync.RWMutex{};m.RLock();m.RUnlock();m.Lock();m.Unlock();fmt.Println("done")}`,
		"escape":    `package main;import("sync";"reflect";"fmt");func main(){var m sync.RWMutex;reflect.ValueOf(&m).MethodByName("RLock").Call(nil);m.RUnlock();fmt.Println(m.TryRLock());m.RUnlock()}`,
		"mixed":     `package main;import("sync";"fmt");func main(){var mu sync.Mutex;var rw sync.RWMutex;var w sync.WaitGroup;w.Add(1);go func(){mu.Lock();rw.RLock();rw.RUnlock();mu.Unlock();w.Done()}();w.Wait();fmt.Println("done")}`,
	} {
		t.Run(name, func(t *testing.T) { s374SyncDifferential(t, source) })
	}
}

func TestS376ResidentRWMutexMisuse(t *testing.T) {
	for name, source := range map[string]string{
		"runlock": `package main;import "sync";func main(){var m sync.RWMutex;m.RUnlock()}`,
		"wunlock": `package main;import "sync";func main(){var m sync.RWMutex;m.Unlock()}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(source), "misuse.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err = r.Run(ctx, p.File); err == nil {
				t.Fatalf("expected misuse error, got output %q", out.String())
			}
		})
	}
}

func TestS376ResidentRWMutexCertificates(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         bool
	}{
		{"local", `package main;import "sync";func main(){var m sync.RWMutex;m.RLock();m.RUnlock()}`, true},
		{"new", `package main;import s "sync";func main(){m:=new(s.RWMutex);m.Lock();m.Unlock()}`, true},
		{"embedded", `package main;import "sync";type B struct{sync.RWMutex};func main(){b:=new(B);b.RLock();b.RUnlock()}`, true},
		{"literal", `package main;import "sync";func main(){m:=&sync.RWMutex{};m.Lock();m.Unlock()}`, true},
		{"try", `package main;import "sync";func main(){var m sync.RWMutex;_=m.TryLock();_=m.TryRLock()}`, true},
		{"native", `package main;import("sync";"reflect");func main(){var m sync.RWMutex;_=reflect.ValueOf(&m)}`, false},
		{"opaque", `package main;import "sync";func main(){var m sync.RWMutex;var a any=&m;_=a}`, false},
		{"callback", `package main;import("sync";"time");var m sync.RWMutex;func main(){time.AfterFunc(0,func(){m.RLock();m.RUnlock()})}`, false},
		{"method-value", `package main;import "sync";func main(){var m sync.RWMutex;f:=m.RLock;f();m.RUnlock()}`, false},
		{"method-expression", `package main;import "sync";func main(){var m sync.RWMutex;(*sync.RWMutex).RLock(&m);m.RUnlock()}`, false},
		{"copy", `package main;import "sync";func main(){var m sync.RWMutex;n:=m;_=n}`, false},
		{"aggregate-copy", `package main;import "sync";type B struct{sync.RWMutex};func main(){var b B;c:=b;_=c}`, false},
		{"rlocker", `package main;import "sync";func main(){var m sync.RWMutex;_ = m.RLocker()}`, false},
		{"comparison", `package main;import "sync";func main(){a:=new(sync.RWMutex);b:=a;_=a==b}`, false},
		{"value-param", `package main;import "sync";func use(m sync.RWMutex){};func main(){use(sync.RWMutex{})}`, false},
		{"unsafe", `package main;import("sync";"unsafe");func main(){var m sync.RWMutex;_=unsafe.Pointer(&m)}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(tc.source), "proof.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			certified := false
			syntax.Walk(p.File, func(n syntax.Node) bool {
				if x, ok := n.(*syntax.BashPPNamedType); ok && x.LocalSync == "sync.RWMutex" {
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

func BenchmarkS376ResidentRWMutex(b *testing.B) {
	for _, domain := range []string{"native", "resident"} {
		b.Run(domain, func(b *testing.B) {
			source := fmt.Sprintf(`package main;import("sync";"time";"fmt");func main(){var m sync.RWMutex;m.RLock();m.RUnlock();start:=time.Now();for i:=0;i<%d;i++{m.RLock();m.RUnlock()};fmt.Println(time.Since(start).Nanoseconds())}`, b.N)
			p, err := gosource.Parse(strings.NewReader(source), "rwbench.go", gosource.Options{RunMain: true})
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
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
