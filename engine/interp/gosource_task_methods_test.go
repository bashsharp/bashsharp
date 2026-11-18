//go:build full

package interp_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

func TestGoSourceOriginalTaskMethodThreeModes(t *testing.T) {
	source, err := os.ReadFile("testdata/gosource-task-methods/mutex-counter.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(source)); digest != "74488ecc347a123c8538069175ffeef5ea38577bb37b2c7a9ceb6bba47ab02b4" {
		t.Fatalf("original digest changed: %s", digest)
	}
	typedSendThreeModes(t, string(source))
}
func TestGoSourceTaskMethodValuesThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"pointer_receiver_rebind": `package main
type S struct{N int}
func(p *S)Work(done chan int){p.N=9;p=&S{20};p.N=21;done<-1}
func main(){p:=&S{3};done:=make(chan int);go p.Work(done);<-done;println(p.N)}`,
		"implicit_address_receiver": `package main
type S struct{N int}
func(p *S)Work(done chan int){p.N=9;p=&S{20};done<-1}
func main(){s:=S{3};done:=make(chan int);go s.Work(done);<-done;println(s.N)}`,
		"value_receiver_copy": `package main
type S struct{N int;P *int}
func(s S)Work(done chan int){s.N=9;*s.P=8;done<-s.N}
func main(){n:=3;s:=S{1,&n};done:=make(chan int);go s.Work(done);println(<-done,s.N,n)}`,
		"receiver_binding_frozen": `package main
type S struct{N int}
func(p *S)Work(start,done chan int){<-start;p.N=9;done<-1}
func main(){p:=&S{3};old:=p;start:=make(chan int);done:=make(chan int);go p.Work(start,done);p=&S{4};start<-1;<-done;println(p.N,old.N)}`,
		"computed_receiver_once": `package main
type S struct{N int}
var calls int
func receiver(p *S)*S{calls=calls*10+1;return p}
func arg()int{calls=calls*10+2;return 9}
func(p *S)Work(n int,done chan int){p.N=n;done<-1}
func main(){p:=&S{3};done:=make(chan int);go receiver(p).Work(arg(),done);<-done;println(calls,p.N)}`,
		"interface_receiver": `package main
type S struct{N int}
type Worker interface{Work(chan int)}
func(p *S)Work(done chan int){p.N=9;done<-1}
func main(){p:=&S{3};var w Worker=p;done:=make(chan int);go w.Work(done);<-done;println(p.N)}`,

		"nil_pointer_receiver": `package main
type S struct{N int}
func(p *S)Work(done chan int){if p==nil{done<-7;return};done<-9}
func main(){var p *S;done:=make(chan int);go p.Work(done);println(<-done)}`,
		"method_value_rebind": `package main
type S struct{N int}
func(p *S)Work(done chan int){p.N++;n:=p.N;p=&S{99};done<-n}
func main(){p:=&S{3};f:=p.Work;done:=make(chan int);go f(done);println(<-done);go f(done);println(<-done,p.N)}`,
		"value_receiver_argument_effect": `package main
type S struct{N int}
func(s S)Work(n int,done chan int){done<-s.N}
func main(){s:=S{1};update:=func()int{s.N=9;return 1};done:=make(chan int);go s.Work(update(),done);println(<-done,s.N)}`,

		"value_receiver_argument_synchronization": `package main
type S struct{N int}
func(s S)Work(n int,done chan int){done<-s.N}
func wait(c chan int)int{<-c;return 1}
func main(){s:=S{1};updated:=make(chan int);done:=make(chan int);go func(){s.N=9;updated<-1}();go s.Work(wait(updated),done);println(<-done,s.N)}`,

		"value_receiver_reference_fields_concurrent": `package main
import "sync"
type S struct{M map[string]int}
var mu sync.Mutex
var wg sync.WaitGroup
func(s S)Work(){defer wg.Done();for j:=0;j<20;j++{mu.Lock();s.M["n"]++;mu.Unlock()}}
func main(){s:=S{map[string]int{"n":0}};for i:=0;i<3;i++{wg.Add(1);go s.Work()};wg.Wait();println(s.M["n"])}`,
		"promoted_receiver": `package main
type S struct{N int}
type Outer struct{*S}
func(p *S)Work(done chan int){p.N=9;done<-1}
func main(){p:=&S{3};s:=Outer{p};done:=make(chan int);go s.Work(done);<-done;println(p.N)}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
