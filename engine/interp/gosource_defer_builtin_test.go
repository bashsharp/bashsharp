//go:build full

package interp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceOriginalDeferredCloseThreeModes(t *testing.T) {
	source, err := os.ReadFile("testdata/gosource-defer-builtin/binarytrees_quit.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(source) != 1297 || fmt.Sprintf("%x", sha256.Sum256(source)) != "25157973b97a82b066516ea92df34bbb6ea26294603a471be5523b2ab19a8f03" {
		t.Fatal("original Tour bytes changed")
	}
	callbackTourThreeModes(t, string(source))
}

func TestGoSourceDeferredCloseThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"evaluate_once_before_body": `package main
var calls int
func pick(c chan int)chan int{calls++;println("pick",calls);return c}
func work(c chan int){defer close(pick(c));println("body",calls)}
func main(){c:=make(chan int,1);c<-7;work(c);v,ok:=<-c;println(v,ok);v,ok=<-c;println(v,ok,calls)}`,
		"capture_channel_before_reassignment": `package main
func work(a,b chan int){c:=a;defer close(c);c=b;c<-9}
func main(){a:=make(chan int,1);b:=make(chan int,1);work(a,b);v,ok:=<-a;println(v,ok);println(<-b);b<-8;println(<-b)}`,
		"lifo_during_panic": `package main
import "fmt"
func work(c chan int){defer func(){v,ok:=<-c;fmt.Println("oldest",v,ok,recover())}();defer close(c);defer println("newest");panic("body")}
func main(){work(make(chan int));println("after")}`,
		"nil_panics_at_unwind": `package main
import "fmt"
func work(){var c chan int;defer func(){fmt.Println(recover())}();defer close(c);println("body")}
func main(){work();println("after")}`,
		"closed_panics_at_unwind": `package main
import "fmt"
func work(c chan int){defer func(){fmt.Println(recover())}();defer close(c);close(c);println("body")}
func main(){work(make(chan int));println("after")}`,
		"argument_panic_does_not_register": `package main
import "fmt"
func broken()chan int{println("argument");panic("argument panic")}
func work(){defer func(){fmt.Println(recover())}();defer close(broken());println("unreachable")}
func main(){work();println("after")}`,
		"defined_send_channel": `package main
type Out chan<- int
func work(c Out){defer close(c);c<-7}
func main(){c:=make(chan int,1);work(c);println(<-c);v,ok:=<-c;println(v,ok)}`,
		"interpreter_owned_channel_identity": `package main
type S struct{N int}
func work(c chan *S,p *S){defer close(c);c<-p}
func main(){c:=make(chan *S,1);p:=&S{3};work(c,p);q:=<-c;q.N=9;println(p==q,p.N);q,ok:=<-c;println(q==nil,ok)}`,
		"shadowed_close_function": `package main
func close(n int){println("shadow",n)}
func main(){defer close(7);println("body")}`,
		"shadowed_close_variable": `package main
func main(){close:=func(n int){println("shadow",n)};defer close(7);println("body")}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestGoSourceDeferredCloseClassicGuard(t *testing.T) {
	const source = `close() { echo shell-close:$1; }
func cleanup(v) { close "$v"; }
func work() {
 defer cleanup(kept)
 echo body
}

work()
`
	if got := runBashPPFunc(t, source); got != "body\nshell-close:kept\n" {
		t.Fatalf("classic deferred shell dispatch changed: %q", got)
	}
}

func TestGoSourceDeferredCloseUnrecovered(t *testing.T) {
	for name, setup := range map[string]string{"nil": "var c chan int", "closed": "c:=make(chan int);close(c)"} {
		t.Run(name, func(t *testing.T) {
			source := "package main;func work(){" + setup + `;defer println("oldest");defer close(c);println("body")};func main(){work();println("unreachable")}`
			program, err := gosource.Parse(strings.NewReader(source), "original.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out, errs bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &errs))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			err = r.Run(ctx, program.File)
			if code, ok := interp.IsExitStatus(err); !ok || code != 2 || out.Len() != 0 || !strings.Contains(errs.String(), "body\noldest\n") || !strings.Contains(errs.String(), "close of "+name+" channel") || strings.Contains(errs.String(), "unreachable") {
				t.Fatalf("unrecovered close lost panic or remaining defer: %v, %q/%q", err, out.String(), errs.String())
			}
		})
	}
}
