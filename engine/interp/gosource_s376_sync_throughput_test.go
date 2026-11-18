//go:build full

package interp_test

// Sprint: #376; Story: #1549; Story-ID: 91561dbe8537
//
// Narrow differential tests for the two general repairs behind the
// interpreted synchronization roots: nil comparison of an interpreter-owned
// channel that lives in an aggregate, and contended resident Mutex/RWMutex
// use by many goroutines. Shapes keep the constructs and constant spelling of
// test/ken/chan.go and test/fixedbugs/issue79186.go at reduced counts.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS376ChannelNilComparisonDifferential(t *testing.T) {
	for name, source := range map[string]string{
		"field":    `package main;type Chan struct{sc,rc chan int;sv,rv int};func main(){ch:=new(Chan);println(ch.sc!=nil,ch.sc==nil);ch.sc=make(chan int,0);ch.rc=ch.sc;println(ch.sc!=nil,ch.rc!=nil,ch.sc==nil,nil!=ch.rc);ch.rc=nil;println(ch.sc!=nil,ch.rc!=nil,ch.rc==nil)}`,
		"value":    `package main;type One struct{c chan int};func main(){var v One;println(v.c==nil);v.c=make(chan int,3);println(v.c!=nil,v.c==nil)}`,
		"literal":  `package main;type Chan struct{sc,rc chan int};func main(){lit:=&Chan{sc:make(chan int)};println(lit.sc!=nil,lit.rc!=nil)}`,
		"copy":     `package main;type Chan struct{sc chan int};func main(){ch:=new(Chan);ch.sc=make(chan int);x:=ch.sc;println(x!=nil,x==nil);x=nil;println(x!=nil,ch.sc!=nil);ch.sc=nil;println(ch.sc!=nil)}`,
		"nil-copy": `package main;type Chan struct{sc chan int};func main(){ch:=new(Chan);x:=ch.sc;println(x!=nil,x==nil);ch.sc=make(chan int,1);x=ch.sc;println(x!=nil,x==nil);x<-7;println(<-ch.sc);x=nil;println(x!=nil,x==nil,ch.sc!=nil)}`,
		"element":  `package main;func main(){m:=map[string]chan int{"a":make(chan int)};s:=[]chan int{make(chan int),nil};a:=[2]chan int{nil,make(chan int,1)};println(m["a"]!=nil,m["b"]!=nil,s[0]!=nil,s[1]!=nil,a[0]==nil,a[1]==nil)}`,
		"param":    `package main;type Chan struct{sc,rc chan int};var nc *Chan;func init(){nc=new(Chan)};func count(r0,s0 *Chan)int{a:=0;if r0.rc!=nil{a++};if s0.sc!=nil{a++};return a};func main(){ch:=new(Chan);ch.sc=make(chan int,0);ch.rc=ch.sc;ca:=make([]*Chan,1);ca[0]=ch;done:=make(chan int);go func(){done<-count(ca[0],nc)}();println(count(ch,nc),count(nc,ch),count(ch,ch),<-done)}`,
		// The select stage of test/ken/chan.go: a finished channel is set to
		// nil in its struct, and the loop runs until none is left.
		"select": `package main
import "runtime"
import "sync"
type Chan struct {
	sc, rc chan int
	sv, rv int
}
var (
	nproc     int
	nprocLock sync.Mutex
	cval      int
	end       int = 10000
	totr      int
	totLock   sync.Mutex
	nc        *Chan
)
func init() { nc = new(Chan) }
func changeNproc(adjust int) int {
	nprocLock.Lock()
	nproc += adjust
	ret := nproc
	nprocLock.Unlock()
	return ret
}
func expect(v, v0 int) (newv int) {
	if v == v0 {
		if v%100 == 5 {
			return end
		}
		return v + 1
	}
	panic("fail")
}
func (c *Chan) send() bool {
	c.sv = expect(c.sv, c.sv)
	if c.sv == end {
		c.sc = nil
		return true
	}
	return false
}
func send(c *Chan) {
	for {
		runtime.Gosched()
		c.sc <- c.sv
		if c.send() {
			break
		}
	}
	changeNproc(-1)
}
func (c *Chan) recv(v int) bool {
	totLock.Lock()
	totr++
	totLock.Unlock()
	c.rv = expect(c.rv, v)
	if c.rv == end {
		c.rc = nil
		return true
	}
	return false
}
func sel(r0, r1, s0 *Chan) {
	var v int
	a := 0
	if r0.rc != nil {
		a++
	}
	if r1.rc != nil {
		a++
	}
	if s0.sc != nil {
		a++
	}
	for {
		select {
		case v = <-r0.rc:
			if r0.recv(v) {
				a--
			}
		case v = <-r1.rc:
			if r1.recv(v) {
				a--
			}
		case s0.sc <- s0.sv:
			if s0.send() {
				a--
			}
		}
		if a == 0 {
			break
		}
	}
	changeNproc(-1)
}
func main() {
	for _, c := range []int{0, 1, 10} {
		ca := make([]*Chan, 2)
		for i := 0; i < 2; i++ {
			cval = cval + 100
			ch := new(Chan)
			ch.sc = make(chan int, c)
			ch.rc = ch.sc
			ch.sv = cval
			ch.rv = cval
			ca[i] = ch
		}
		changeNproc(3)
		go send(ca[0])
		go send(ca[1])
		go sel(ca[0], ca[1], nc)
		for changeNproc(0) != 0 {
			runtime.Gosched()
		}
	}
	println(totr)
}`,
	} {
		t.Run(name, func(t *testing.T) { s374SyncDifferential(t, source) })
	}
}

func TestS376ResidentSyncContendedDifferential(t *testing.T) {
	for name, source := range map[string]string{
		"mutex": `package main
import "sync"
var (
	tots    int
	totLock sync.Mutex
)
func main() {
	var wg sync.WaitGroup
	const goroutines = 64
	const iters = 50
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				totLock.Lock()
				tots++
				totLock.Unlock()
			}
		}()
	}
	wg.Wait()
	println(tots)
}`,
		// test/fixedbugs/issue79186.go at reduced iters.
		"rwmutex": `package main
import (
	"runtime"
	"sync"
)
type M struct {
	mu sync.RWMutex
	m  map[int]int
}
func NewM() *M {
	return &M{m: make(map[int]int)}
}
func (x *M) Get(k int) (int, bool) {
	x.mu.RLock()
	v, ok := x.m[k]
	x.mu.RUnlock()
	return v, ok
}
func (x *M) Set(k, v int) {
	x.mu.Lock()
	x.m[k] = v
	x.mu.Unlock()
}
func main() {
	runtime.GOMAXPROCS(2)
	x := NewM()
	const goroutines = 256
	const iters = 40
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				k := (id + i) & 15
				if _, ok := x.Get(k); !ok {
					x.Set(k, i)
				} else if i&7 == 0 {
					x.Set(k, i)
				}
			}
		}(g)
	}
	wg.Wait()
	println(len(x.m))
}`,
	} {
		t.Run(name, func(t *testing.T) { s374SyncDifferential(t, source) })
	}
}

// A goroutine that panics ends the program with Go's status 2 even when main
// is blocked at the time. The failure cancels the run to wake main; that
// cancellation must not replace the status as the program's result.
func TestS376GoroutinePanicStatusWhileMainBlocked(t *testing.T) {
	for name, source := range map[string]string{
		"sleep":   `package main;import("fmt";"time");func main(){go func(){fmt.Println("before");panic("boom")}();time.Sleep(5*time.Second)}`,
		"mutex":   `package main;import("fmt";"sync");func main(){var m sync.Mutex;m.Lock();go func(){fmt.Println("before");panic("boom")}();m.Lock()}`,
		"receive": `package main;import "fmt";func main(){c:=make(chan int);go func(){fmt.Println("before");panic("boom")}();<-c}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(source), "panic.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err = r.Run(ctx, p.File)
			var status interp.ExitStatus
			if !errors.As(err, &status) || status != 2 {
				t.Fatalf("Run error %v, want exit status 2; stderr=%q", err, stderr.String())
			}
			if out.String() != "before\n" || !strings.Contains(stderr.String(), "panic: boom") {
				t.Fatalf("stdout=%q stderr=%q", out.String(), stderr.String())
			}
		})
	}
}
