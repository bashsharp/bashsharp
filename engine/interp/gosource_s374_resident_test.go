//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS374ResidentCertificate(t *testing.T) {
	for name, source := range map[string]string{
		"direct": `package main
func relay(c chan int) { c <- 7 }
func main() { c:=make(chan int,1); go relay(c); println(<-c); close(c) }`,
		"inferred-field":     `package main;type Box struct{ C chan int };func main(){b:=new(Box);b.C=make(chan int,1);b.C<-7;select{case <-b.C:}}`,
		"inferred-aggregate": `package main;func main(){a:=[]chan int{make(chan int,1)};b:=a;b[0]<-1;println(<-a[0])}`,
		"directional":        `package main;type C chan int;func main(){c:=make(C,1);var s chan<- int=c;s<-1;var r <-chan int=c;println(<-r)}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(source), "resident.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			syntax.Walk(p.File, func(n syntax.Node) bool {
				if ch, ok := n.(*syntax.BashPPChanType); ok {
					seen++
					if !ch.LocalDomain {
						t.Error("non-escaping channel lacks resident certificate")
					}
				}
				return true
			})
			if seen == 0 {
				t.Fatal("no channel types")
			}
		})
	}
}

func TestS374KenCertificate(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "ken", "chan.go"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := gosource.Parse(strings.NewReader(string(source)), "ken.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	local, native := 0, 0
	syntax.Walk(p.File, func(n syntax.Node) bool {
		if ch, ok := n.(*syntax.BashPPChanType); ok {
			if ch.LocalDomain {
				local++
			} else {
				native++
			}
		}
		return true
	})
	t.Logf("channel certificates: resident=%d native=%d", local, native)
	if local == 0 || native != 0 {
		t.Fatal("inferred channel types lost allocation certificate")
	}
}

func TestS374ResidentDifferential(t *testing.T) {
	for name, source := range map[string]string{
		"goroutines": `package main; import "fmt"
func relay(c chan int, done chan bool) { for i:=0;i<8;i++ { c<-i }; close(c); done<-true }
func main(){ c:=make(chan int);done:=make(chan bool);go relay(c,done);sum:=0;for n:=range c {sum+=n};<-done;fmt.Println(sum,len(c),cap(c)) }`,
		"field-select": `package main; import "fmt"
type Box struct { c chan int }; func main(){ b:=new(Box);b.c=make(chan int,2);b.c<-7; var nilc chan int;select {case v:=<-b.c:fmt.Println(v);case <-nilc:panic("nil")};b.c=nil;select{case <-b.c:panic("nil");default:fmt.Println("default")} }`,
		"nil-named": `package main;import "fmt";type C chan int;func main(){var c C;fmt.Println(len(c),cap(c));c=make(C,2);fmt.Println(len(c),cap(c));c=nil;fmt.Println(len(c),cap(c))}`,
		"directions": `package main;import "fmt";type C chan int
func send(c chan<- int){c<-9;close(c)};func recv(c <-chan int){v,ok:=<-c;fmt.Println(v,ok);v,ok=<-c;fmt.Println(v,ok)}
func main(){c:=make(C,2);send(c);recv(c);fmt.Println(len(c),cap(c))}`,
		"copy-and-alias":  `package main;import "fmt";type V struct{ n int };func main(){c:=make(chan V,1);v:=V{3};c<-v;v.n=8;fmt.Println((<-c).n);s:=[]int{1};d:=make(chan []int,1);d<-s;s[0]=4;fmt.Println((<-d)[0])}`,
		"close-panics":    `package main;import "fmt";func attempt(f func()){defer func(){fmt.Println(recover()!=nil)}();f()};func main(){c:=make(chan int,1);c<-2;close(c);fmt.Println(<-c);v,ok:=<-c;fmt.Println(v,ok);attempt(func(){close(c)});attempt(func(){c<-3});var n chan int;attempt(func(){close(n)})}`,
		"aliased-fields":  `package main;import "fmt";type Pipe struct{send,recv chan int};func main(){p:=new(Pipe);p.send=make(chan int);p.recv=p.send;go func(){for i:=0;i<8;i++{p.send<-i};close(p.send)}();sum:=0;for n:=range p.recv{sum+=n};fmt.Println(sum)}`,
		"aggregate-range": `package main;import "fmt";func main(){c:=make(chan int,2);c<-3;c<-4;close(c);b:=struct{c chan int}{c};fmt.Println(len(b.c),cap(b.c));sum:=0;for n:=range b.c{sum+=n};a:=[]chan int{c};calls:=0;pick:=func()int{calls++;return 0};for range a[pick()]{};fmt.Println(sum,calls,len(a[0]),cap(a[0]))}`,
		"select-send":     `package main;import "fmt";func main(){a:=make(chan int,1);b:=make(chan int);select{case a<-5:case b<-9:panic("unready")};fmt.Println(<-a);close(a);select{case n,ok:=<-a:fmt.Println(n,ok);default:panic("closed")}}`,
		"native-select":   `package main;import("fmt";"time");func main(){c:=make(chan int,1);c<-4;select{case v:=<-c:fmt.Println(v);case <-time.After(time.Hour):panic("timer")}}`,
		"native-escape":   `package main;import("fmt";"reflect");func main(){c:=make(chan int,1);reflect.ValueOf(c).Send(reflect.ValueOf(6));fmt.Println(<-c)}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestS374ResidentEscapeCertificates(t *testing.T) {
	for name, source := range map[string]string{
		"direct":               `package main;import "reflect";func main(){c:=make(chan int,1);_=reflect.ValueOf(c)}`,
		"opaque-return":        `package main;import "fmt";func box(c chan int) any{return c};func main(){c:=make(chan int);_=fmt.Sprint(box(c))}`,
		"aggregate-box":        `package main;import "fmt";func main(){c:=make(chan int);_=fmt.Sprint(struct{X any}{c})}`,
		"callback-capture":     `package main;import "sort";var c chan int;func main(){c=make(chan int);sort.Search(1,func(int)bool{return len(c)==0})}`,
		"indirect-native":      `package main;import "reflect";func main(){f:=reflect.ValueOf;c:=make(chan int);_=f(c)}`,
		"native-storage":       `package main;import "reflect";func main(){c:=make(chan int);dst:=reflect.New(reflect.TypeOf(c));dst.Elem().Set(reflect.ValueOf(c))}`,
		"native-origin-select": `package main;import "time";func main(){c:=make(chan int);select{case <-c:case <-time.After(1):}}`,
		"unsafe":               `package main;import "unsafe";func main(){c:=make(chan int);_=unsafe.Pointer(&c)}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(source), "escape.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			syntax.Walk(p.File, func(n syntax.Node) bool {
				if ch, ok := n.(*syntax.BashPPChanType); ok {
					seen++
					if ch.LocalDomain {
						t.Error("escaping channel certified resident")
					}
				}
				return true
			})
			if seen == 0 {
				t.Fatal("no channel types")
			}
		})
	}
}
