//go:build full

package interp_test

import "testing"

func TestGoSourceTaskArgumentValuesThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"original_pointer_channel_repro": `package main
type S struct{N int}
var c=make(chan *S,1)
func send(p *S){c<-p}
func main(){p:=&S{3};go send(p);q:=<-c;q.N=9;println(p==q,p.N,cap(c))}`,
		"parameter_rebind": `package main
type S struct{N int}
func work(p *S,done chan int){p.N=9;p=&S{20};p.N=21;done<-1}
func main(){p:=&S{3};done:=make(chan int);go work(p,done);<-done;println(p.N)}`,
		"slice_map_backing": `package main
func work(s []int,m map[string]int,done chan int){s[0]=9;m["n"]=8;s=[]int{30};m=map[string]int{"n":40};done<-s[0]+m["n"]}
func main(){s:=[]int{3};m:=map[string]int{"n":4};done:=make(chan int);go work(s,m,done);println(<-done,s[0],m["n"])}`,
		"struct_and_array_values": `package main
type S struct{N int; P *int; Slice []int; Map map[string]int}
func work(v S,a [2]int,done chan int){v.N=90;*v.P=8;v.Slice[0]=7;v.Map["n"]=6;a[0]=99;done<-v.N+a[0]}
func main(){n:=3;s:=[]int{4};m:=map[string]int{"n":5};v:=S{1,&n,s,m};a:=[2]int{2,3};done:=make(chan int);go work(v,a,done);println(<-done,v.N,n,s[0],m["n"],a[0])}`,
		"argument_order_and_values": `package main
var calls int
func first()int{calls++;return calls}
func second()int{calls++;return calls}
func work(a,b int,done chan int){done<-a*10+b}
func main(){done:=make(chan int);go work(first(),second(),done);println(calls,<-done)}`,
		"computed_callee_before_arguments": `package main
var calls int
func factory()func(int,chan int){calls=calls*10+1;return func(n int,done chan int){done<-n}}
func arg()int{calls=calls*10+2;return calls}
func main(){done:=make(chan int);go factory()(arg(),done);println(calls,<-done)}`,
		"pointer_producer_once": `package main
type S struct{N int}
var calls int
func argument(p *S)*S{calls++;return p}
func work(p *S,done chan int){p.N=9;done<-1}
func main(){p:=&S{3};done:=make(chan int);go work(argument(p),done);<-done;println(calls,p.N)}`,
		"channel_parameter_rebind": `package main
func work(c chan int,done chan int){c<-7;c=make(chan int,1);c<-8;done<- <-c}
func main(){c:=make(chan int,1);done:=make(chan int);go work(c,done);println(<-done,<-c,cap(c))}`,

		"argument_binding_frozen": `package main
type S struct{N int}
func work(p *S,ready chan int,done chan int){<-ready;p.N=9;done<-p.N}
func main(){p:=&S{3};old:=p;ready:=make(chan int);done:=make(chan int);go work(p,ready,done);p=&S{4};ready<-1;println(<-done,p.N,old.N)}`,
		"slice_bounds_and_array_references": `package main
func work(s []int,a [1]*int,done chan int){s[0]=7;*a[0]=8;a[0]=nil;done<-len(s)*10+cap(s)}
func main(){base:=[]int{1,2,3,4};n:=3;a:=[1]*int{&n};done:=make(chan int);go work(base[1:3:4],a,done);println(<-done,base[0],base[1],n,a[0]==&n)}`,
		"interface_pointer_argument": `package main
type S struct{N int}
func work(v any,done chan int){p:=v.(*S);p.N=9;v=nil;done<-1}
func main(){p:=&S{3};var v any=p;done:=make(chan int);go work(v,done);<-done;println(p.N,v==nil)}`,
		"panic_argument_starts_no_task": `package main
var ran bool
func work(n int){ran=true}
func argument()int{panic("argument")}
func main(){defer func(){value:=recover();println(value=="argument",ran)}();go work(argument())}`,

		"concurrent_reference_launches": `package main
import "sync"
type State struct{N int}
var mu sync.Mutex
var wg sync.WaitGroup
func work(p *State,s []int,m map[string]int){defer wg.Done();for i:=0;i<50;i++{mu.Lock();p.N++;s[0]++;m["n"]++;mu.Unlock()}}
func main(){p:=&State{};s:=[]int{0};m:=map[string]int{"n":0};for i:=0;i<3;i++{wg.Add(1);go work(p,s,m)};wg.Wait();println(p.N,s[0],m["n"])}`,
		"argument_closure_capture": `package main
func work(f func()int,done chan int){done<-f()}
func main(){n:=3;done:=make(chan int);go work(func()int{return n},done);println(<-done)}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
