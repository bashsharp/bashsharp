//go:build full

package interp_test

import (
	"crypto/sha256"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoSourceTupleVarThreeModes(t *testing.T) {
	cases := map[string]string{
		"map_once_and_absent": `package main
var calls int
func table() map[string]int { calls++; return map[string]int{"x":7} }
func main(){ var a,ok=table()["x"]; println(a,ok,calls);var b,missing=table()["y"];println(b,missing,calls) }`,
		"assert_once_and_absent": `package main
var calls int
func value() any {calls++;return 7}
func main(){var a,ok=value().(int);println(a,ok,calls);var b,missing=value().(string);println(b,missing,calls)}`,
		"multi_return_once": `package main
var calls int
func pair()(int,string){calls++;return calls,"value"}
func main(){var a,b=pair();println(a,b,calls);var _,_=pair();println(calls)}`,
		"lexical_shadow": `package main
func pair(x int)(int,bool){return x+1,true}
func main(){x:=7;{var x,ok=pair(x);println(x,ok)};println(x)}`,
		"explicit_named_bool": `package main
import "fmt"
type Flag bool
func main(){m:=map[int]Flag{0:true};var x,ok Flag=m[0];fmt.Printf("%T %T %v %v\n",x,ok,x,ok);var y,missing Flag=m[1];fmt.Printf("%T %T %v %v\n",y,missing,y,missing)}`,
		"package_initialization_order": `package main
var calls int
var a,b=pair()
var c=a+b
func pair()(int,int){calls++;return 3,4}
func main(){println(a,b,c,calls)}`,
		"pointer_tuple_identity": `package main
type S struct{N int}
func pair()(*S,*S){p:=&S{7};return p,p}
func main(){var a,b=pair();a.N=8;println(a==b,b.N)}`,
	}
	cases["receive_once_and_closed"] = `package main
var calls int
func channel(c chan int) chan int {calls++;return c}
func main(){c:=make(chan int,2);c<-7;c<-8;close(c);var a,ok=<-channel(c);println(a,ok,calls);var b,more=<-channel(c);println(b,more,calls);var z,closed=<-channel(c);println(z,closed,calls)}`
	cases["parentheses_and_prefix_collision"] = `package main
var __gosource_tuple_1_0=11
func main(){m:=map[int]int{1:7};var a,ok=(m[1]);println(a,ok,__gosource_tuple_1_0);var _,_=(m[2])}`
	cases["user_any_type_is_not_predeclared_alias"] = `package main
type any int
func pair()(any,bool){return 7,true}
func main(){var a,ok=pair();var b=a;println(b,ok)}`

	cases["parenthesized_assertion_operand"] = `package main
func main(){var i any=7;var a,ok=((i).(int));println(a,ok)}`

	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestGoSourceOriginalTupleVarThreeModes(t *testing.T) {
	for name, want := range map[string]string{
		"bug227.go.txt":     "d0a620a22c475792a74c417abf02bdcf73bd476abd91d13ffffd553a2691cfe3",
		"bug244.go.txt":     "9f83485037c7c1fc99568395c30f79eaeab8f5bec9a01f57ffffa887e66fedd9",
		"bug264.go.txt":     "641a973a4c3af674c04190445b89a65a90209bad1d8cc9ce250f7f9369a25d81",
		"bug291.go.txt":     "fe67bdaf345626d8b1fc0fddef63e006a956e7f19242560b292a8cdf4bd9044a",
		"bug436.go.txt":     "652659fd8e4199febb93d6a2fb09e5ddeb76394af3342e2584a8096c1a86ff10",
		"issue53619.go.txt": "b79e5298b944387200410a205eb9b45644942c6cc291db0916d056f2c2de24e3",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata/gosource-tuple-var", name))
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(data)) != want {
				t.Fatal("original fixture bytes changed")
			}
			typedSendThreeModes(t, string(data))
		})
	}
}

func TestGoSourceOriginalTupleVarGenericCheck(t *testing.T) {
	data, err := os.ReadFile("testdata/gosource-tuple-var/issue66878.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "b6e68af10d251540930ac6b2e9ac6f91a71cf82f73eda2ed535715e389c4d225" {
		t.Fatal("original bytes changed")
	}
	p, err := gosource.Parse(strings.NewReader(string(data)), "issue66878.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lower.Compile(p.File, lower.Options{Origin: "issue66878.go"}); err != nil {
		t.Fatal(err)
	}
}
