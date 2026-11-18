//go:build full

package interp_test

import "testing"

func TestGoSourceCapturedValueReassignmentThreeModes(t *testing.T) {
	for n, s := range map[string]string{
		"initially_nil_native_pointer":      `package main;import("fmt";"math/big");func main(){var v *big.Int;ready:=make(chan struct{});done:=make(chan struct{});go func(){<-ready;fmt.Println(v.Int64());close(done)}();v=big.NewInt(9);close(ready);<-done}`,
		"native_pointer_nil_control":        `package main;import("fmt";"math/big");func main(){v:=big.NewInt(9);v=nil;fmt.Println(v==nil)}`,
		"native_pointer_set_nil":            `package main;import("fmt";"math/big");func main(){v:=big.NewInt(9);ready:=make(chan struct{});done:=make(chan struct{});go func(){<-ready;fmt.Println(v==nil);close(done)}();v=nil;close(ready);<-done}`,
		"initially_nil_channel":             `package main;import "fmt";func main(){var c chan int;ready:=make(chan struct{});done:=make(chan struct{});go func(){<-ready;c<-7;close(done)}();c=make(chan int,1);close(ready);<-done;fmt.Println(len(c))}`,
		"local_reference_channel":           `package main;import "fmt";type Box struct{n int};func main(){c:=make(chan *Box,1);d:=make(chan *Box,1);ready:=make(chan struct{});done:=make(chan struct{});go func(){<-ready;c<-&Box{7};close(done)}();c=d;close(ready);<-done;fmt.Println(len(d))}`,
		"captured_close":                    `package main;import "fmt";func main(){c:=make(chan int);d:=make(chan int);ready:=make(chan struct{});done:=make(chan struct{});go func(){<-ready;close(c);close(done)}();c=d;close(ready);<-done;_,ok:=<-d;fmt.Println(ok)}`,
		"native_alias_is_separate_variable": `package main;import("fmt";"math/big");func main(){v:=big.NewInt(1);old:=v;w:=big.NewInt(2);ready:=make(chan struct{});done:=make(chan struct{});go func(){<-ready;fmt.Println(v.Int64(),old.Int64());close(done)}();v=w;close(ready);<-done}`,
		"channel": `package main;import "fmt";func main(){c:=make(chan int,1);d:=make(chan int,1);ready:=make(chan struct{});done:=make(chan struct{});go func(){<-ready;c<-7;close(done)}();c=d;close(ready);<-done;fmt.Println(len(d))}
`,
		"native_pointer": `package main;import("fmt";"math/big");func main(){v:=big.NewInt(1);w:=big.NewInt(2);ready:=make(chan struct{});done:=make(chan struct{});go func(){<-ready;fmt.Println(v.Int64());close(done)}();v=w;close(ready);<-done}
`,
	} {
		t.Run(n, func(t *testing.T) { typedSendThreeModes(t, s) })
	}
}
