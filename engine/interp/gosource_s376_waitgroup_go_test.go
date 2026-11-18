//go:build full

package interp_test

// Sprint: #376; Story: #1549; Story-ID: 91561dbe8537
//
// Narrow tests for resident-certified sync.WaitGroup.Go: the converter lowers
// the statement to Add + go + Done so the production mutexes.bsh shape no
// longer declines the package-wide resident certificate.

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS376ResidentWaitGroupGoDifferential(t *testing.T) {
	for name, source := range map[string]string{
		"mutexes":                   `package main;import("fmt";"sync");type C struct{mu sync.Mutex;n map[string]int};func (c *C) inc(k string){c.mu.Lock();defer c.mu.Unlock();c.n[k]++};func main(){c:=C{n:map[string]int{"a":0,"b":0}};var wg sync.WaitGroup;do:=func(k string,n int){for range n{c.inc(k)}};wg.Go(func(){do("a",300)});wg.Go(func(){do("a",300)});wg.Go(func(){do("b",300)});wg.Wait();fmt.Println(c.n)}`,
		"pointer":                   `package main;import("fmt";"sync");func run(wg *sync.WaitGroup,m *sync.Mutex,n *int){wg.Go(func(){m.Lock();*n++;m.Unlock()})};func main(){wg:=new(sync.WaitGroup);m:=new(sync.Mutex);n:=0;for i:=0;i<20;i++{run(wg,m,&n)};wg.Wait();fmt.Println(n)}`,
		"field":                     `package main;import("fmt";"sync");type P struct{wg sync.WaitGroup;mu sync.Mutex;n int};func main(){p:=&P{};for i:=0;i<10;i++{p.wg.Go(func(){p.mu.Lock();p.n+=i;p.mu.Unlock()})};p.wg.Wait();fmt.Println(p.n)}`,
		"embedded":                  `package main;import("fmt";"sync");type P struct{sync.WaitGroup;n int32;mu sync.Mutex};func main(){p:=&P{};for i:=0;i<5;i++{p.Go(func(){p.mu.Lock();p.n++;p.mu.Unlock()})};p.Wait();fmt.Println(p.n)}`,
		"operand-once":              `package main;import("fmt";"sync");var calls int;var m sync.Mutex;func mk()func(){calls++;return func(){m.Lock();calls+=10;m.Unlock()}};func main(){var wg sync.WaitGroup;wg.Go(mk());wg.Wait();fmt.Println(calls)}`,
		"pointer-param":             `package main;import("fmt";"sync");func run(wg *sync.WaitGroup,m *sync.Mutex,n *int){for i:=0;i<6;i++{wg.Go(func(){m.Lock();*n+=i;m.Unlock()})}};func main(){wg:=new(sync.WaitGroup);m:=new(sync.Mutex);n:=0;run(wg,m,&n);wg.Wait();fmt.Println(n)}`,
		"receiver-reassigned":       `package main;import("fmt";"sync");func main(){a:=new(sync.WaitGroup);b:=new(sync.WaitGroup);p:=a;var m sync.Mutex;n:=0;p.Go(func(){m.Lock();n++;m.Unlock()});p=b;a.Wait();b.Wait();fmt.Println(n)}`,
		"receiver-field-reassigned": `package main;import("fmt";"sync");type H struct{wg *sync.WaitGroup};func main(){a:=new(sync.WaitGroup);h:=&H{a};n:=0;var m sync.Mutex;h.wg.Go(func(){m.Lock();n++;m.Unlock()});h.wg=new(sync.WaitGroup);a.Wait();fmt.Println(n)}`,
		"receiver-arg-reassignment": `package main;import("fmt";"sync");func main(){a:=new(sync.WaitGroup);b:=new(sync.WaitGroup);p:=a;var m sync.Mutex;n:=0;p.Go(func()func(){p=b;return func(){m.Lock();n++;m.Unlock()}}());a.Wait();b.Wait();fmt.Println(n)}`,
		"arg-order":                 `package main;import("fmt";"sync");var log []string;func note(s string)int{log=append(log,s);return 0};func mk()func(){note("arg");return func(){}};func main(){wg:=new(sync.WaitGroup);wg.Go(mk());wg.Wait();fmt.Println(log)}`,
		"arg-panic":                 `package main;import("fmt";"sync");func mk()func(){panic("boom")};func main(){var wg sync.WaitGroup;func(){defer func(){fmt.Println("recovered",recover())}();wg.Go(mk())}();wg.Wait();fmt.Println("no leaked Add")}`,
		"named-func-value":          `package main;import("fmt";"sync");func main(){var wg sync.WaitGroup;var m sync.Mutex;n:=0;f:=func(){m.Lock();n++;m.Unlock()};for range 4{wg.Go(f)};wg.Wait();fmt.Println(n)}`,
	} {
		t.Run(name, func(t *testing.T) { s374SyncDifferential(t, source) })
	}
}

func TestS376ResidentWaitGroupGoCertificates(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         bool
	}{
		{"statement", `package main;import "sync";func main(){var wg sync.WaitGroup;wg.Go(func(){});wg.Wait()}`, true},
		{"field-chain", `package main;import "sync";type P struct{wg sync.WaitGroup};func main(){p:=&P{};p.wg.Go(func(){});p.wg.Wait()}`, true},
		{"production-shape", `package main;import "sync";type C struct{mu sync.Mutex;n map[string]int};func (c *C) inc(k string){c.mu.Lock();defer c.mu.Unlock();c.n[k]++};func main(){c:=C{n:map[string]int{}};var wg sync.WaitGroup;wg.Go(func(){for range 3{c.inc("a")}});wg.Wait()}`, true},
		{"production-mutexes-exact", mutexesBsh, true},
		{"local-closure-callee", `package main;import "sync";type C struct{mu sync.Mutex;n map[string]int};func (c *C) inc(k string){c.mu.Lock();defer c.mu.Unlock();c.n[k]++};func main(){c:=C{n:map[string]int{}};var wg sync.WaitGroup;do:=func(k string,n int){for range n{c.inc(k)}};wg.Go(func(){do("a",3)});wg.Wait()}`, true},
		{"local-closure-escapes", `package main;import "sync";func main(){var wg sync.WaitGroup;do:=func(){};g:=do;g();wg.Go(func(){do()});wg.Wait()}`, false},
		{"local-closure-reassigned", `package main;import "sync";func main(){var wg sync.WaitGroup;do:=func(){};do=nil;wg.Go(func(){do()});wg.Wait()}`, false},
		{"go-statement", `package main;import "sync";func main(){var wg sync.WaitGroup;go wg.Go(func(){});wg.Wait()}`, false},
		{"defer-statement", `package main;import "sync";func main(){var wg sync.WaitGroup;defer wg.Go(func(){});wg.Wait()}`, false},
		{"method-value", `package main;import "sync";func main(){var wg sync.WaitGroup;g:=wg.Go;g(func(){});wg.Wait()}`, false},
		{"computed-receiver", `package main;import "sync";func get(p *sync.WaitGroup)*sync.WaitGroup{return p};func main(){wg:=new(sync.WaitGroup);get(wg).Go(func(){});wg.Wait()}`, false},
		{"indexed-receiver", `package main;import "sync";func main(){wgs:=[]*sync.WaitGroup{new(sync.WaitGroup)};wgs[0].Go(func(){});wgs[0].Wait()}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(tc.source), "proof.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			certified := false
			syntax.Walk(p.File, func(n syntax.Node) bool {
				if x, ok := n.(*syntax.BashPPNamedType); ok && x.LocalSync == "sync.WaitGroup" {
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

const mutexesBsh = `// In the previous example we saw how to manage simple
// counter state using [atomic operations](atomic-counters).
// For more complex state we can use a [_mutex_](https://en.wikipedia.org/wiki/Mutual_exclusion)
// to safely access data across multiple goroutines.

package main

import (
	"fmt"
	"sync"
)

// Container holds a map of counters; since we want to
// update it concurrently from multiple goroutines, we
// add a ` + "`" + `Mutex` + "`" + ` to synchronize access.
// Note that mutexes must not be copied, so if this
// ` + "`" + `struct` + "`" + ` is passed around, it should be done by
// pointer.
type Container struct {
	mu       sync.Mutex
	counters map[string]int
}

func (c *Container) inc(name string) {
	// Lock the mutex before accessing ` + "`" + `counters` + "`" + `; unlock
	// it at the end of the function using a [defer](defer)
	// statement.
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counters[name]++
}

func main() {
	c := Container{
		// Note that the zero value of a mutex is usable as-is, so no
		// initialization is required here.
		counters: map[string]int{"a": 0, "b": 0},
	}

	var wg sync.WaitGroup

	// This function increments a named counter
	// in a loop.
	doIncrement := func(name string, n int) {
		for range n {
			c.inc(name)
		}
	}

	// Run several goroutines concurrently; note
	// that they all access the same ` + "`" + `Container` + "`" + `,
	// and two of them access the same counter.
	wg.Go(func() {
		doIncrement("a", 10000)
	})

	wg.Go(func() {
		doIncrement("a", 10000)
	})

	wg.Go(func() {
		doIncrement("b", 10000)
	})

	// Wait for the goroutines to finish
	wg.Wait()
	fmt.Println(c.counters)
}
`
