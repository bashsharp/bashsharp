//go:build full

package interp_test

// Sprint: #118; Story: #51; Story-ID: 825f8083451e
import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoSourceUnifiedChannelsThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"capacity_once":             `package main;import "fmt";func capacity()int{fmt.Println("capacity");return 2};func main(){c:=make(chan int,capacity()+1);c<-7;fmt.Println(len(c),cap(c),<-c)}`,
		"negative_capacity_panic":   `package main;import "fmt";func main(){n:=-1;defer func(){fmt.Println(recover())}();_ = make(chan int,n)}`,
		"beyond_legacy_cap":         `package main;import "fmt";func main(){c:=make(chan int,1000001);fmt.Println(cap(c),len(c))}`,
		"constructor_result":        `package main;import "fmt";func newChannel(n int)chan int{return make(chan int,n)};func main(){c:=newChannel(1);c<-9;fmt.Println(<-c)}`,
		"close_operand_once":        `package main;import "fmt";func pick(c chan int)chan int{fmt.Println("pick");return c};func main(){c:=make(chan int,1);c<-8;close(pick(c));v,ok:=<-c;fmt.Println(v,ok);v,ok=<-c;fmt.Println(v,ok)}`,
		"close_nil":                 `package main;import "fmt";func main(){var c chan int;fmt.Println(len(c),cap(c));defer func(){fmt.Println(recover())}();close(c)}`,
		"close_twice":               `package main;import "fmt";func main(){c:=make(chan int);close(c);defer func(){fmt.Println(recover())}();close(c)}`,
		"native_and_created_atomic": `package main;import("fmt";"time");func main(){c:=make(chan int,1);c<-7;var n chan int;select{case v:=<-c:fmt.Println(v);case <-time.After(time.Hour):panic("timer");case <-n:panic("nil")}}`,
		"range_and_direction":       `package main;import "fmt";func fill(c chan<- int){c<-4;c<-5;close(c)};func sum(c <-chan int)int{total:=0;for v:=range c{total+=v};return total};func main(){c:=make(chan int,2);fill(c);fmt.Println(sum(c))}`,
		"worker_pool_result":        `package main;import "fmt";func worker(jobs <-chan int,out chan<- int){for j:=range jobs{out<-j*2}};func main(){jobs:=make(chan int,5);out:=make(chan int,5);for i:=0;i<3;i++{go worker(jobs,out)};for j:=1;j<=5;j++{jobs<-j};close(jobs);sum:=0;for i:=0;i<5;i++{sum+=<-out};fmt.Println(sum)}`,
		"native_owned_type":         `package main;import("fmt";"time");func main(){c:=make(chan time.Time,1);t:=time.Date(2020,1,2,3,4,5,0,time.UTC);c<-t;got:=<-c;fmt.Println(got.Equal(t),got.Year())}`,
		"empty_struct_select":       `package main;import("fmt";"time");func main(){done:=make(chan struct{});close(done);select{case <-done:fmt.Println("done");case <-time.After(time.Hour):panic("timer")}}`,
		"no_imports":                `package main;func main(){c:=make(chan int,1);c<-7;println(<-c);close(c)}`,
		"nil_select_default":        `package main;import("fmt";"time");func main(){var c chan int;select{case <-c:panic("nil");case <-time.After(time.Hour):panic("timer");default:fmt.Println("default")}}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
func TestGoSourceOriginalUnifiedTimeoutThreeModes(t *testing.T) {
	for file, expected := range map[string]string{"timeouts.go.txt": "3670d030f98ca1046237c7c7f8b35fca82a0e4278ead38fe35c279eb1191c3ff"} {
		b, err := os.ReadFile(filepath.Join("testdata/gosource-native-channels", file))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != expected {
			t.Fatal("original bytes changed")
		}
		typedSendThreeModes(t, string(b))
	}
}

func TestGoSourceChannelCapacityTypedJSON(t *testing.T) {
	const source = `package main;func capacity()int{println("capacity");return 2};func main(){c:=make(chan int,capacity()+1);println(cap(c))}`
	p, err := gosource.Parse(strings.NewReader(source), "capacity.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := (typedjson.EncodeOptions{}).Encode(&encoded, p.File); err != nil {
		t.Fatal(err)
	}
	node, err := typedjson.Decode(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	syntax.Walk(node, func(n syntax.Node) bool {
		if mk, ok := n.(*syntax.BashPPMakeChan); ok {
			count++
			if _, ok := mk.CapacityExpr.(*syntax.BashPPBinaryExpr); !ok {
				t.Fatalf("lost typed capacity: %T", mk.CapacityExpr)
			}
		}
		return true
	})
	if count != 1 {
		t.Fatalf("constructors: %d", count)
	}
	var output bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, nil, &output))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.Run(ctx, node); err != nil {
		t.Fatal(err)
	}
	if output.String() != "capacity\n3\n" {
		t.Fatalf("capacity evaluation: %q", output.String())
	}
}

func TestGoSourceUnifiedChannelCancellationReset(t *testing.T) {
	for _, operation := range []string{"<-c", "c<-7", "select{case <-c:}"} {
		// A sleeping goroutine keeps the program alive: with the main
		// goroutine alone blocked, Go reports a deadlock instead.
		source := `package main;import "fmt";import "time";func main(){go func(){for{time.Sleep(time.Hour)}}();c:=make(chan int);fmt.Println("ready");` + operation + `;fmt.Println("UNREACHABLE")}`
		p, err := gosource.Parse(strings.NewReader(source), "blocked.go", gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		output := &sendReadyWriter{marker: "ready\n", ready: make(chan struct{})}
		var errs bytes.Buffer
		r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, output, &errs))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		done := make(chan error, 1)
		go func() { done <- r.Run(ctx, p.File) }()
		select {
		case <-output.ready:
		case err := <-done:
			cancel()
			t.Fatalf("ended before block: %v %q", err, errs.String())
		case <-ctx.Done():
			cancel()
			t.Fatal("ready timeout")
		}
		select {
		case err := <-done:
			cancel()
			t.Fatalf("did not block: %v %q", err, errs.String())
		case <-time.After(50 * time.Millisecond):
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancel succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("blocked native operation survived cancellation")
		}
		if output.String() != "ready\n" {
			t.Fatalf("post-block output: %q", output.String())
		}
		r.Reset()
		p, err = gosource.Parse(strings.NewReader(`package main;import "fmt";func main(){c:=make(chan int,1);c<-9;fmt.Println(<-c);close(c)}`), "fresh.go", gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		err = r.Run(ctx, p.File)
		cancel()
		if err != nil || output.String() != "ready\n9\n" {
			t.Fatalf("Reset: %v %q/%q", err, output.String(), errs.String())
		}
	}
}

func TestGoSourceUnifiedChannelReferenceDomainThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"local_callback":   `package main;import "fmt";type Token struct{};func(Token)String()string{return "token"};func main(){c:=make(chan Token,1);c<-Token{};fmt.Println(<-c);close(c)}`,
		"computed_len_cap": `package main;import "fmt";func pick(c chan int)chan int{fmt.Println("pick");return c};func main(){c:=make(chan int,2);c<-7;fmt.Println(len(pick(c)),cap(pick(c)))}`,
		"directional_make": `package main;import "fmt";func main(){c:=make(chan<- int,3);fmt.Println(len(c),cap(c));close(c)}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
