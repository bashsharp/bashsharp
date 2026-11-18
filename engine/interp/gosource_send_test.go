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
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGoSourceTypedSendThreeModes(t *testing.T) {
	cases := map[string]string{
		"local_shadows_function": `package main
import "fmt"
func sum(c chan int){sum:=3;sum+=4;c<-sum}
func main(){c:=make(chan int,1);sum(c);fmt.Println(<-c)}`,
		"operand_then_rhs_once": `package main
import "fmt"
func pick(c chan int)chan int{fmt.Println("channel");return c}
func value()int{fmt.Println("value");return 20}
func main(){c:=make(chan int,1);pick(c)<-value()+1;fmt.Println(<-c)}`,
		"select_operands_before_default": `package main
import "fmt"
func pick(c chan int,s string)chan int{fmt.Println("channel",s);return c}
func value(s string)int{fmt.Println("value",s);return 1}
func main(){a:=make(chan int,1);a<-0;var b chan int;select{case pick(a,"a")<-value("a"):fmt.Println("bad a");case pick(b,"b")<-value("b"):fmt.Println("bad b");default:fmt.Println("default")}}`,
		"before_block": `package main
import "fmt"
func value()int{fmt.Println("rhs");return 7}
func worker(c chan int,done chan int){x:=<-c;done<-x}
func main(){c:=make(chan int);done:=make(chan int);go worker(c,done);c<-value();fmt.Println(<-done)}`,
		"scalar_identity": `package main
import "fmt"
type Count int
func main(){s:=make(chan string,1);b:=make(chan bool,1);n:=make(chan Count,1);u:=make(chan uint64,1);f:=make(chan float32,1);z:=make(chan complex128,1);s<-"a\nb";b<-true;n<-Count(9);u<-18446744073709551615;f<-1.25;z<-2+3i;fmt.Printf("%q %t %T %v %T %v\n",<-s,<-b,<-n,<-u,<-f,<-z)}`,
		"value_copy": `package main
import "fmt"
type Item struct {N int;Text string}
func main(){c:=make(chan Item,1);a:=make(chan [2]int,1);v:=Item{7,"hello"};arr:=[2]int{1,2};c<-v;a<-arr;v.N=99;arr[0]=99;x:=<-c;y:=<-a;fmt.Println(x,y,v,arr)}`,
		"slice_reference": `package main
import "fmt"
func main(){c:=make(chan []int,1);v:=[]int{1,2};c<-v;v[0]=7;x:=<-c;fmt.Println(x);close(c);zero,ok:=<-c;fmt.Println(zero==nil,ok)}`,
		"interface": `package main
import "fmt"
func main(){c:=make(chan any,2);c<-7;c<-nil;x:=<-c;y:=<-c;fmt.Printf("%T %v %v\n",x,x,y==nil)}`,
		"operand_panic_skips_rhs": `package main
import "fmt"
func pick()chan int{fmt.Println("channel");panic("broken channel")}
func value()int{fmt.Println("UNREACHABLE rhs");return 1}
func main(){defer func(){v:=recover();fmt.Println(v)}();pick()<-value();fmt.Println("UNREACHABLE")}`,
		"nil_select_rhs_panic": `package main
import "fmt"
func value()int{fmt.Println("rhs");panic("broken rhs")}
func main(){var c chan int;defer func(){v:=recover();fmt.Println(v)}();select{case c<-value():default:fmt.Println("UNREACHABLE default")}}`,
		"map_pointer_reference": `package main
import "fmt"
type Item struct{N int}
func main(){c:=make(chan map[string]int,1);p:=make(chan *Item,1);m:=map[string]int{"n":1};v:=&Item{2};c<-m;p<-v;m["n"]=7;v.N=8;x:=<-c;y:=<-p;fmt.Println(x["n"],y.N)}`,
		"rhs_panic": `package main
import "fmt"
func value()int{fmt.Println("rhs");panic("broken")}
func main(){c:=make(chan int,1);defer func(){v:=recover();fmt.Println(v)}();c<-value();fmt.Println("UNREACHABLE")}`,
		"later_select_rhs_closes_earlier": `package main
import "fmt"
func value()int{fmt.Println("first");return 1}
func shut(c chan int)int{fmt.Println("later");close(c);return 2}
func main(){c:=make(chan int,1);var n chan int;defer func(){v:=recover();fmt.Println(v)}();select{case c<-value():case n<-shut(c):};fmt.Println("UNREACHABLE")}`,
		"closed_select_evaluates_all": `package main
import "fmt"
func value(s string)int{fmt.Println(s);return 1}
func main(){c:=make(chan int);close(c);var n chan int;defer func(){v:=recover();fmt.Println(v)}();select{case c<-value("closed"):case n<-value("nil"):};fmt.Println("UNREACHABLE")}`,
		"closed_zero": `package main
import "fmt"
func main(){c:=make(chan complex128);a:=make(chan any);close(c);close(a);z:=<-c;x:=<-a;fmt.Printf("%T %v %v\n",z,z,x==nil)}`,
		"channel_reference": `package main
import "fmt"
func main(){outer:=make(chan chan int,1);inner:=make(chan int,1);outer<-inner;got:=<-outer;got<-9;fmt.Println(<-inner)}`,
		"select_struct": `package main
import "fmt"
type Item struct {N int}
func main(){c:=make(chan Item,1);select{case c<-Item{7}:default:panic("not ready")};select{case value:=<-c:fmt.Println(value.N);default:panic("not ready")}}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
func typedSendThreeModes(t *testing.T, source string) {
	t.Helper()
	typedSendThreeModesNormalized(t, source, nil)
}

// typedSendThreeModesNormalized compares the three modes after passing stdout
// through normalize; stderr is always exact. It exists for originals whose output embeds a value
// no two runs can agree on — a wall clock reading — so that everything else in
// the output is still compared byte for byte. normalize must fail the test
// itself if the elided value is not well formed, or the comparison would be
// weakened rather than narrowed.
func typedSendThreeModesNormalized(t *testing.T, source string, normalize func(string) string) {
	t.Helper()
	if normalize == nil {
		normalize = func(s string) string { return s }
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sdk := filepath.Join(runtime.GOROOT(), "bin", "go")
	run := func(binary string) (string, string) {
		var out, errout bytes.Buffer
		cmd := exec.CommandContext(ctx, binary)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = &out, &errout
		if err := cmd.Run(); err != nil {
			t.Fatalf("native: %v %q %q", err, out.String(), errout.String())
		}
		return out.String(), errout.String()
	}
	build := func(output, input string) {
		cmd := exec.CommandContext(ctx, sdk, "build", "-p", "2", "-o", output, input)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
	}
	oracle := filepath.Join(dir, "oracle")
	build(oracle, path)
	wantOut, wantErr := run(oracle)
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v out=%q err=%q", err, out.String(), errout.String())
	}
	if normalize(out.String()) != normalize(wantOut) || errout.String() != wantErr {
		t.Fatalf("Runner %q/%q; oracle %q/%q", out.String(), errout.String(), wantOut, wantErr)
	}
	lowered, err := lower.Compile(program.File, lower.Options{Origin: path, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(dir, "generated.go")
	if err := os.WriteFile(generated, lowered.Source, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "compiled")
	build(artifact, generated)
	after, err := os.ReadFile(path)
	if err != nil || string(after) != source {
		t.Fatal("original bytes changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(generated); err != nil {
		t.Fatal(err)
	}
	gotOut, gotErr := run(artifact)
	if normalize(gotOut) != normalize(wantOut) || gotErr != wantErr {
		t.Fatalf("artifact %q/%q; oracle %q/%q", gotOut, gotErr, wantOut, wantErr)
	}
}

func TestGoSourceUpstreamChannelSendThreeModes(t *testing.T) {
	fixtures := map[string]string{
		"range-over-channels.go.txt": "28b0c821c5abab03d502954325bec2ba75af27474973be9dcf5bcb9210c2fd24",
		"channel-buffering.go.txt":   "6f79aec915ed516c6b44809d22c1a93259c4c29f6520522cf95f32c354cd9fa2",
		"channel-directions.go.txt":  "57f24a91827b584354f9cb0547f0d0847246ae821f7ab70c420c0829fd63c92f",
	}
	for name, want := range fixtures {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata/gosource-channel-send", name))
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(source)) != want {
				t.Fatal("upstream bytes drifted")
			}
			typedSendThreeModes(t, string(source))
		})
	}
}

// A readiness signal observes the RHS's actual output. The test then proves
// the send stays blocked before cancelling the Runner and joining it.
type sendReadyWriter struct {
	marker string
	mu     sync.Mutex
	data   bytes.Buffer
	ready  chan struct{}
	once   sync.Once
}

func (w *sendReadyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.data.Write(p)
	marker := w.marker
	if marker == "" {
		marker = "rhs\n"
	}
	if strings.Contains(w.data.String(), marker) {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}
func (w *sendReadyWriter) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.data.String() }
func TestGoSourceTypedSendCancellation(t *testing.T) {
	for name, body := range map[string]string{
		"nil":        `var c chan int;c<-value()`,
		"full":       `c:=make(chan int,1);c<-0;c<-value()`,
		"select_nil": `var c chan int;select{case c<-value():}`,
	} {
		t.Run(name, func(t *testing.T) {
			// A sleeping goroutine keeps the program alive: with the main
			// goroutine alone blocked, Go reports a deadlock instead.
			source := `package main
import "fmt"
import "time"
func value()int{fmt.Println("rhs");return 7}
func main(){go func(){for{time.Sleep(time.Hour)}}();` + body + `;fmt.Println("UNREACHABLE")}`
			program, err := gosource.Parse(strings.NewReader(source), "cancel.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			out := &sendReadyWriter{ready: make(chan struct{})}
			var errout bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, out, &errout))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- runner.Run(ctx, program.File) }()
			select {
			case <-out.ready:
			case err := <-done:
				t.Fatalf("send ended before RHS: %v %q", err, errout.String())
			case <-ctx.Done():
				t.Fatal("RHS did not execute")
			}
			select {
			case err := <-done:
				t.Fatalf("send did not block: %v %q", err, errout.String())
			case <-time.After(50 * time.Millisecond):
			}
			cancel()
			select {
			case err := <-done:
				if status, ok := interp.IsExitStatus(err); !ok || status != 1 || ctx.Err() != context.Canceled {
					t.Fatalf("cancel returned %v; stderr=%q", err, errout.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled send did not join")
			}
			if out.String() != "rhs\n" {
				t.Fatalf("RHS count or post-send execution: %q", out.String())
			}
		})
	}
}
