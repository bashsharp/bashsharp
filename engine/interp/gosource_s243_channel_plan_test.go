//go:build full

package interp_test

// Sprint: #243; Story: #674; Story-ID: 63073886bfce
import (
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestS243ChannelDomainPlanningThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"connected-local-types": `package main
import "fmt"
type payload struct{ n int }
func main(){ requests:=make(chan int,1); data:=make(chan payload,1); requests<-1; data<-payload{7}; select { case <-requests: fmt.Println((<-data).n); case data<-payload{9}: panic("losing send") }; close(requests); close(data) }`,
		"native-origin-keeps-domain-native": `package main
import("fmt";"time")
func main(){ c:=make(chan int,1); c<-7; select { case v:=<-c: fmt.Println(v); case <-time.After(time.Hour): panic("timer") } }`,
		"close-nil-cancel-and-nonselected-sender": `package main
import "fmt"
func main(){ c:=make(chan int,1); var stop chan struct{}; c<-4; select { case v:=<-c: fmt.Println(v); case c<-9: panic("losing sender"); case <-stop: panic("nil cancellation arm") }; close(c); _,ok:=<-c; fmt.Println(ok) }`,
		"assigned-nil-keeps-local-domain": `package main
import "fmt"
func main(){ a:=make(chan int,1); b:=make(chan int,1); b<-7; a=nil; select { case <-a: panic("nil ready"); case v:=<-b: fmt.Println(v) } }`,
		"ranged-channel-keeps-capability": `package main
import "fmt"
func main(){ channels:=[]chan bool{make(chan bool,1)}; for _,c:=range channels { c<-true }; fmt.Println(<-channels[0]) }`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestS243ChannelDomainNativeProvenanceThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"direct-native-in-select-body": `package main
import("fmt";"time")
func main(){ c:=make(chan time.Time,1); c<-time.Time{}; select { case <-c: _=fmt.Sprint(c); fmt.Println("local"); default: panic("default") } }`,
		"aliased-time-after": `package main
import("fmt";"time")
func main(){ after:=time.After; local:=make(chan time.Time,1); local<-time.Time{}; select { case <-local: fmt.Println("local"); case <-after(time.Hour): panic("timer") } }`,
		"timer-field-and-local": `package main
import("fmt";"time")
func main(){ timer:=time.NewTimer(time.Hour); defer timer.Stop(); local:=make(chan time.Time,1); local<-time.Time{}; select { case <-local: fmt.Println("local"); case <-timer.C: panic("timer") } }`,
		"directional-native-boundary": `package main
import("fmt";"reflect")
func main(){ c:=make(chan int,1); var send chan<- int=c; _=reflect.ValueOf(send); c<-7; select { case v:=<-c: fmt.Println(v); default: panic("default") } }`,
		"interface-and-slice-native-escape": `package main
import "fmt"
func main(){ c:=make(chan int,1); var opaque any=c; _=fmt.Sprint(opaque); _=fmt.Sprint([]chan int{c}); c<-9; select { case v:=<-c: fmt.Println(v); default: panic("default") } }`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestS243OriginalPowserChannelDomains(t *testing.T) {
	for _, name := range []string{"powser1.go", "powser2.go"} {
		source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "chan", name))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, string(source)) })
	}
}

func TestS243OriginalChannelDomainRegressions(t *testing.T) {
	for _, name := range []string{"chan/select.go", "chanlinear.go"} {
		source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", name))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) {
			typedSendThreeModes(t, string(source))
		})
	}
}

func TestS243ChannelDomainIndirectNativeBoundary(t *testing.T) {
	for name, source := range map[string]string{
		"function-parameter": `package main
import ("fmt";"time")
func choose(after func(time.Duration)<-chan time.Time) {
 c:=make(chan int,1); c<-7
 select {case n:=<-c:fmt.Println(n);case <-after(time.Hour):panic("timer")}
}
func main(){choose(time.After)}`,
		"function-field": `package main
import ("fmt";"time")
type Factory struct{ f func(time.Duration)<-chan time.Time }
func main(){factory:=Factory{time.After}; c:=make(chan int,1);c<-7
 select {case n:=<-c:fmt.Println(n);case <-factory.f(time.Hour):panic("timer")}}
`,
	} {
		t.Run(name, func(t *testing.T) {
			if name != "function-field" {
				typedSendThreeModes(t, source)
				return
			}
			// Direct calls through a function-valued field have a separate
			// existing dispatch limitation. Check the allocation certificate
			// here without claiming that unrelated execution path repaired.
			program, err := gosource.Parse(strings.NewReader(source), "field.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			syntax.Walk(program.File, func(node syntax.Node) bool {
				if typ, ok := node.(*syntax.BashPPChanType); ok {
					seen++
					if typ.LocalDomain {
						t.Error("native function field received local channel certificate")
					}
				}
				return true
			})
			if seen == 0 {
				t.Fatal("no channel types inspected")
			}
		})
	}
}
