//go:build full

package interp_test

import (
	"github.com/bashsharp/bashsharp/engine/gosource"
	"strings"
	"testing"
)

func TestGoSourcePackageChannelThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"capacity_once_and_dependencies": `package main
var calls int
var capacity=cap(c)
var c=make(chan int,size())
func size()int{calls++;return 2}
func main(){c<-7;c<-8;println(capacity,len(c),calls);var a,ok=<-c;println(a,ok);close(c);println(<-c);var z,more=<-c;println(z,more)}`,
		"directional_aliases": `package main
import "fmt"
type Channel = chan int
var c=make(Channel,1)
var send chan<- int=c
var recv <-chan int=c
func main(){fmt.Printf("%T %T\n",send,recv);send<-7;println(<-recv,cap(c));close(send);var z,ok=<-recv;println(z,ok)}`,
		"defined_channel_identity": `package main
import "fmt"
type Channel chan int
var c=make(Channel,1)
func main(){c<-7;fmt.Printf("%T %d %d\n",c,cap(c),<-c)}`,
		"initializer_function_and_goroutine": `package main
var calls int
var c=start()
func start()chan int{calls++;ch:=make(chan int);go func(){ch<-7;close(ch)}();return ch}
func read()int{return <-c}
func main(){println(read(),calls);var z,ok=<-c;println(z,ok)}`,
		"initializer_function_literal": `package main
var c=func()chan int{ch:=make(chan int,1);ch<-8;return ch}()
func main(){println(<-c,cap(c))}`,
		"interpreter_owned_payload": `package main
type S struct{N int}
var c=make(chan *S,1)
func send(p *S){c<-p}
func main(){p:=&S{3};send(p);q:=<-c;q.N=9;println(p==q,p.N,cap(c))}`,
		"typed_nil_and_local_var": `package main
type Input = <-chan int
var empty Input
func main(){println(empty==nil,cap(empty),len(empty));var c chan int=make(chan int,1);c<-4;println(<-c)}`,
		"nil_initializer": `package main
var c chan int=nil
func main(){println(c==nil);select{case <-c:panic("nil channel ready");default:println("blocked")}}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestGoSourcePackageChannelInvalidDeclarations(t *testing.T) {
	for name, source := range map[string]string{
		"wrong_direction":   `package p;var recv <-chan int;var send chan<- int=recv`,
		"wrong_element":     `package p;var c chan int=make(chan string)`,
		"negative_constant": `package p;var c=make(chan int,-1)`,
		"send_receive_only": `package p;var c <-chan int;func f(){c<-1}`,
		"non_comparable_key": `package p
type S []int
func f(){m:=make(map[S]int);_=m}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(source), "original.go", gosource.Options{})
			if err == nil || p != nil {
				t.Fatal("invalid channel declaration accepted")
			}
		})
	}
}

func TestGoSourceDefinedChannelMapKey(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
type C chan int
func main(){
	c:=make(C,1)
	m:=make(map[C]int)
	m[nil]=1
	m[c]=2
	m[make(C)]=3
	c<-9
	fmt.Println(m[nil],m[c],<-c,len(m))
}`)
}
