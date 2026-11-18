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
	"testing"
	"time"
)

const nativeChannelDependency = `package dep
func Buffer() chan int { return make(chan int,2) }
func Empty() chan int { return make(chan int) }
func Nil() chan int { return nil }
func Closed() chan int { c:=make(chan int);close(c);return c }
func Len(c chan int) int { return len(c) }
func Send(c chan int,n int) { c<-n }
func Close(c chan int) { close(c) }
func Bytes() chan []byte { return make(chan []byte,1) }
func Bools() chan bool { return make(chan bool,1) }
func Strings() chan string { return make(chan string,1) }
func Anys() chan any { return make(chan any,1) }
func Uints() chan uint64 { return make(chan uint64,1) }
`

func nativeChannelModule(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "dep"), 0700); err != nil {
		t.Fatal(err)
	}
	_, testFile, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(testFile)))
	module := fmt.Sprintf("module example.test/nativechannels\n\ngo 1.26.5\n\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => %s\n", filepath.ToSlash(root))
	for path, body := range map[string]string{"go.mod": module, "dep/dep.go": nativeChannelDependency, "original.go": source} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func nativeChannelRun(t *testing.T, dir, source string, ctx context.Context, out, errs *bytes.Buffer) error {
	t.Helper()
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatal(err)
	}
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, out, errs))
	if err != nil {
		t.Fatal(err)
	}
	return r.Run(ctx, p.File)
}
func nativeChannelThreeModes(t *testing.T, source string) {
	t.Helper()
	dir := nativeChannelModule(t, source)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	build := func(path, input string) {
		cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-mod=mod", "-p", "2", "-o", path, input)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
	}
	run := func(path string) (string, string) {
		var out, errs bytes.Buffer
		cmd := exec.CommandContext(ctx, path)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = &out, &errs
		if err := cmd.Run(); err != nil {
			t.Fatalf("execute: %v %q/%q", err, out.String(), errs.String())
		}
		return out.String(), errs.String()
	}
	oracle := filepath.Join(dir, "oracle")
	build(oracle, "original.go")
	want, wantErr := run(oracle)
	var out, errs bytes.Buffer
	if err := nativeChannelRun(t, dir, source, ctx, &out, &errs); err != nil {
		t.Fatalf("Runner: %v %q/%q", err, out.String(), errs.String())
	}
	if out.String() != want || errs.String() != wantErr {
		t.Fatalf("Runner %q/%q; oracle %q/%q", out.String(), errs.String(), want, wantErr)
	}
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatal(err)
	}
	generated, err := lower.Compile(p.File, lower.Options{Origin: filepath.Join(dir, "original.go"), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated.go"), generated.Source, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "artifact")
	build(artifact, "generated.go")
	for path, body := range map[string]string{"original.go": source, "dep/dep.go": nativeChannelDependency} {
		b, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil || string(b) != body {
			t.Fatalf("source changed: %s", path)
		}
	}
	for _, path := range []string{"original.go", "generated.go", "dep"} {
		if err := os.RemoveAll(filepath.Join(dir, path)); err != nil {
			t.Fatal(err)
		}
	}
	got, gotErr := run(artifact)
	if got != want || gotErr != wantErr {
		t.Fatalf("artifact %q/%q; oracle %q/%q", got, gotErr, want, wantErr)
	}
}
func TestGoSourceNativeChannelsThreeModes(t *testing.T) {
	cases := map[string]string{
		"local_struct_value_copy":          `type Item struct{N int};func main(){c:=dep.Anys();v:=Item{7};c<-v;v.N=9;got:=<-c;fmt.Printf("%T %v %v\n",got,got,v)}`,
		"unsigned_width":                   `func main(){c:=dep.Uints();c<-18446744073709551615;v:=<-c;fmt.Printf("%T %v\n",v,v)}`,
		"later_rhs_panic_no_communication": `func broken()int{panic("rhs")};func main(){a:=dep.Buffer();b:=dep.Buffer();defer func(){fmt.Println(recover(),dep.Len(a),dep.Len(b))}();select{case a<-7:case b<-broken():}}`,

		"send_receive":             `func main(){c:=dep.Buffer();c<-7;v,ok:=<-c;fmt.Printf("%T %v %t\n",v,v,ok)}`,
		"closed_nil_default":       `func main(){c:=dep.Closed();v,ok:=<-c;fmt.Println(v,ok);n:=dep.Nil();select{case <-n:panic("nil ready");default:fmt.Println("default")}}`,
		"no_self_match":            `func main(){c:=dep.Empty();select{case c<-7:panic("self-send");case <-c:panic("self-receive");default:fmt.Println("default",dep.Len(c))}}`,
		"losing_receive_untouched": `func main(){a:=dep.Buffer();b:=dep.Buffer();a<-7;b<-7;select{case <-a:case <-b:};fmt.Println(dep.Len(a)+dep.Len(b))}`,
		"losing_send_untouched":    `func main(){a:=dep.Buffer();b:=dep.Buffer();select{case a<-7:case b<-7:};fmt.Println(dep.Len(a)+dep.Len(b))}`,
		"duplicate_receive":        `func main(){a:=dep.Buffer();a<-7;a<-7;select{case <-a:case <-a:};fmt.Println(dep.Len(a),<-a)}`,
		"duplicate_send":           `func main(){a:=dep.Buffer();select{case a<-7:case a<-7:};fmt.Println(dep.Len(a),<-a)}`,
		"operand_rhs_once":         `func pick(c chan int,s string)chan int{fmt.Println(s);return c};func value(s string)int{fmt.Println(s);return 7};func main(){a:=dep.Buffer();b:=dep.Nil();select{case pick(a,"a")<-value("av"):case pick(b,"b")<-value("bv"):};fmt.Println(<-a)}`,
		"concurrent_rpc":           `func receive(c chan int,done chan int){done<- <-c};func main(){c:=dep.Empty();done:=make(chan int);go receive(c,done);dep.Send(c,9);fmt.Println(<-done)}`,
		"closed_send_recover":      `func main(){c:=dep.Closed();defer func(){fmt.Println(recover())}();c<-7}`,
		"scalar_identity":          `func main(){b:=dep.Bools();s:=dep.Strings();b<-true;s<-"a\nb";fmt.Printf("%T %t %q\n",<-b,true,<-s)}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			nativeChannelThreeModes(t, `package main;import("fmt";"example.test/nativechannels/dep");`+body)
		})
	}
}

// A select mixing dependency channels with interpreter-owned ones commits
// exactly one arm (Sprint 275, gosource_mixed_select.go). Both-ready shapes
// print only what holds whichever arm wins, so the three modes must agree.
func TestGoSourceNativeChannelsMixedSelect(t *testing.T) {
	for name, body := range map[string]string{
		"mixed_receive":             `func main(){a:=dep.Buffer();a<-7;b:=make(chan []int,1);b<-[]int{8};n:=0;select{case v:=<-a:n=v+100*len(b);case v:=<-b:n=v[0]+100*len(a)};fmt.Println(n==107||n==108,len(a)+len(b))}`,
		"mixed_send":                `func main(){a:=dep.Buffer();b:=make(chan []int,1);select{case a<-7:fmt.Println(len(b)==0&&<-a==7);case b<-[]int{8}:fmt.Println(dep.Len(a)==0&&(<-b)[0]==8);default:fmt.Println("default")}}`,
		"mixed_send_default":        `func main(){a:=dep.Empty();b:=make(chan int);select{case a<-7:fmt.Println("a");case b<-8:fmt.Println("b");default:fmt.Println("default")}}`,
		"mixed_send_blocking_wakes": `func main(){a:=dep.Empty();b:=make(chan int);done:=make(chan int);go func(){done<- <-a}();select{case a<-5:fmt.Println("sent");case v:=<-b:fmt.Println("b",v)};fmt.Println(<-done)}`,
		"mixed_receive_closed":      `func main(){a:=dep.Closed();b:=make(chan int);select{case v,ok:=<-a:fmt.Println("a",v,ok);case v:=<-b:fmt.Println("b",v)}}`,
	} {
		t.Run(name, func(t *testing.T) {
			nativeChannelThreeModes(t, `package main;import("fmt";"example.test/nativechannels/dep");`+body)
		})
	}
}
func TestGoSourceNativeChannelsRejectedReferences(t *testing.T) {
	for name, test := range map[string]struct{ body, want string }{
		"retained_method": {`type Item struct{N int};func(i Item)String()string{return "item"};func main(){c:=dep.Anys();c<-Item{1};fmt.Println("UNREACHABLE")}`, "cannot retain original callback identity"},
		"reference":       {`func main(){c:=dep.Bytes();c<-[]byte{1};fmt.Println("UNREACHABLE")}`, "cannot retain interpreter-owned reference values"},
	} {
		t.Run(name, func(t *testing.T) {
			source := `package main;import("fmt";"example.test/nativechannels/dep");` + test.body
			dir := nativeChannelModule(t, source)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var out, errs bytes.Buffer
			err := nativeChannelRun(t, dir, source, ctx, &out, &errs)
			if err == nil || !strings.Contains(err.Error()+errs.String(), test.want) || strings.Contains(out.String(), "UNREACHABLE") {
				t.Fatalf("expected %q: %v %q/%q", test.want, err, out.String(), errs.String())
			}
		})
	}
}
func TestGoSourceOriginalNativeTimerThreeModes(t *testing.T) {
	b, err := os.ReadFile("testdata/gosource-native-channels/timers.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	const want = "9a4f53271ce75bafb04decf8494af57b15923c17e28182beba4a7b23740a2989"
	if fmt.Sprintf("%x", sha256.Sum256(b)) != want {
		t.Fatal("original bytes changed")
	}
	typedSendThreeModes(t, string(b))
}

func TestGoSourceNativeChannelCancellationReset(t *testing.T) {
	for _, body := range []string{`c:=dep.Nil();fmt.Println("ready");<-c`, `c:=dep.Nil();fmt.Println("ready");select{case <-c:}`, `c:=dep.Nil();fmt.Println("ready");c<-7`} {
		source := `package main;import("fmt";"example.test/nativechannels/dep");func main(){` + body + `;fmt.Println("UNREACHABLE")}`
		dir := nativeChannelModule(t, source)
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
		if err != nil {
			t.Fatal(err)
		}
		output := &sendReadyWriter{marker: "ready\n", ready: make(chan struct{})}
		var errs bytes.Buffer
		r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, output, &errs))
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
			t.Fatalf("ended before blocking: %v %q", err, errs.String())
		case <-ctx.Done():
			cancel()
			t.Fatal("readiness timeout")
		}
		select {
		case err := <-done:
			cancel()
			t.Fatalf("nil channel did not block: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled receive succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled native operation survived")
		}
		if output.String() != "ready\n" {
			t.Fatalf("post-block execution: %q", output.String())
		}
		r.Reset()
		fresh := `package main;import("fmt";"example.test/nativechannels/dep");func main(){c:=dep.Buffer();c<-9;fmt.Println(<-c)}`
		p, err = gosource.Parse(strings.NewReader(fresh), filepath.Join(dir, "fresh.go"), gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		err = r.Run(ctx, p.File)
		cancel()
		if err != nil || output.String() != "ready\n9\n" {
			t.Fatalf("reset failed: %v %q/%q", err, output.String(), errs.String())
		}
	}
}

func TestGoSourceNativeChannelStandardLibraryThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"after_typed_value":   `package main;import("fmt";"time");func main(){t:=<-time.After(time.Millisecond);fmt.Printf("%T %t\n",t,t.IsZero())}`,
		"context_done_closed": `package main;import("fmt";"context";"time");func main(){ctx,_:=context.WithTimeout(context.Background(),time.Millisecond);_,ok:=<-ctx.Done();fmt.Println(ok)}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
