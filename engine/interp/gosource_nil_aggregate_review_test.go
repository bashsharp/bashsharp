//go:build full

package interp_test

import "testing"

func TestGoSourceNilAggregateReviewThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"func_channel_fields": `package main
import "fmt"
type Box struct{F func();C chan int}
func main(){b:=Box{nil,nil};fmt.Println(b.F==nil,b.C==nil);f,c:=b.F,b.C;fmt.Println(f==nil,c==nil)}`,
		"func_channel_fields_aliases": `package main
import "fmt"
type F = func()
type C = chan int
type Box struct { F F; C C }
func main(){b:=Box{nil,nil};fmt.Println(b.F==nil,b.C==nil);f,c:=b.F,b.C;fmt.Println(f==nil,c==nil);rows:=[]Box{{nil,nil}};alias:=rows[:];fmt.Println(alias[0].F==nil,alias[0].C==nil);fs:=[]F{nil,F(nil)};cs:=[]C{nil,C(nil)};for i,f:=range fs{fmt.Println(f==nil,cs[i]==nil)}}`,
		"nil_function_signature": `package main
import "fmt"
type F func(int,...string)(bool,error)
func main(){fs:=[]F{nil,F(nil)};fmt.Println(fs[0]==nil,fs[1]==nil)}`,
		"comparison_evaluates_index_once": `package main
import "fmt"
var calls int
func idx()int{calls++;return 0}
func main(){fs:=[]func(){nil};cs:=[]chan int{nil};fmt.Println(fs[idx()]==nil,cs[idx()]==nil,calls);alias:=cs[:];alias[0]=nil;fmt.Println(cs[0]==alias[0])}`,
		"interface_typed_nil_function_channel": `package main
import "fmt"
func main(){values:=[]any{(func())(nil),(chan int)(nil),nil};for _,v:=range values{fmt.Println(v==nil)};fmt.Println(values[0]==nil,values[1]==nil,values[2]==nil)}`,
		"nonempty_interface_typed_nil": `package main
import "fmt"
type E struct{}
func(*E)Error()string{return "E"}
type Box struct{ Err error }
func main(){rows:=[]Box{{nil},{(*E)(nil)}};a:=rows[:];fmt.Println(a[0].Err==nil,a[1].Err==nil);var e error=(*E)(nil);fmt.Println(a[1].Err==e);a[0].Err=e;a[1].Err=nil;fmt.Println(rows[0].Err==nil,rows[1].Err==nil)}`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}
