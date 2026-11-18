//go:build full

package interp_test

import "testing"

func TestGoSourceChannelCaptureThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"send_payload_selector":   `package main;import "fmt";func main(){out:=make(chan int,1);payload:=struct{N int}{7};go func(){out<-payload.N}();fmt.Println(<-out)}`,
		"computed_channel":        `package main;import "fmt";func pick(c chan int)chan int{return c};func main(){out:=make(chan int,1);n:=9;go func(){pick(out)<-n}();fmt.Println(<-out)}`,
		"receive_native_selector": `package main;import("fmt";"time");func main(){timer:=time.NewTimer(0);done:=make(chan bool);go func(){<-timer.C;done<-true}();<-done;fmt.Println("fired")}`,
		"receive_shadow":          `package main;import "fmt";func main(){out:=make(chan int,1);out<-7;done:=make(chan bool);go func(){out:=make(chan int,1);out<-9;<-out;done<-true}();<-done;fmt.Println(<-out)}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
