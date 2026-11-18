//go:build full

package interp_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceNativeMembers(t *testing.T) {
	cases := map[string]string{
		"listener_deadline": `package main
import("fmt";"net";"time")
func main(){addr,e:=net.ResolveTCPAddr("tcp","127.0.0.1:0");if e!=nil{panic(e)};listener,e:=net.ListenTCP("tcp",addr);if e!=nil{panic(e)};defer listener.Close();listener.SetDeadline(time.Now().Add(20*time.Millisecond));_,acceptErr:=listener.Accept();fmt.Println(acceptErr!=nil)}`,

		"typed_nil_method": `package main
import("fmt";"os")
func main(){var f *os.File;close:=f.Close;fmt.Println(close())}`,

		"file_methods_and_deferred_receiver": `package main
import("fmt";"os")
func main(){f,e:=os.CreateTemp(".","member-");if e!=nil{panic(e)};defer os.Remove(f.Name());defer f.Close();n,e:=f.Write([]byte{'o','k'});fmt.Println(n,e,f==f);fmt.Println(f.Name()!="")}`,
		"bound_method": `package main
import("fmt";"os")
func main(){f,e:=os.CreateTemp(".","member-");if e!=nil{panic(e)};defer os.Remove(f.Name());defer f.Close();write:=f.Write;n,e:=write([]byte{'o','k'});fmt.Println(n,e)}`,
		"listener_interface": `package main
import("fmt";"net")
func main(){listener,e:=net.Listen("tcp","127.0.0.1:0");if e!=nil{panic(e)};defer listener.Close();addr:=listener.Addr();fmt.Println(addr.Network(),listener.Addr().Network());fmt.Println(listener==listener)}`,
		"native_nested_field": `package main
import("fmt";"net/url")
func main(){u,e:=url.Parse("https://ada:secret@example.com/path");if e!=nil{panic(e)};fmt.Println(u.Host,u.User.Username(),u.User.String());user:=u.User;fmt.Println(user.Username(),user==u.User)}`,
		"defer_values_captured": `package main
import("fmt";"bytes")
func main(){b:=bytes.NewBufferString("");method:=b.WriteString;defer fmt.Println(b.String());defer method("second");defer b.WriteString("first");fmt.Println(b.Len())}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
	t.Run("http_response_body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("hello\n"))
		}))
		defer server.Close()
		source := `package main
import("fmt";"net/http";"os";"io")
func main(){resp,e:=http.Get(os.Args[1]);if e!=nil{panic(e)};defer resp.Body.Close();fmt.Println(resp.StatusCode,resp.Status,resp.ContentLength);body:=resp.Body;fmt.Printf("%T %t\n",body,body==resp.Body);io.Copy(os.Stdout,body);fmt.Println(resp.Request.URL.Host!="",resp.Request.URL.Scheme)}`
		differGoSource(t, source, []string{server.URL}, "")
	})
}

func TestGoSourceNativeMemberCancellation(t *testing.T) {
	source := `package main
import("fmt";"net";"os")
func main(){listener,e:=net.Listen("tcp","127.0.0.1:0");if e!=nil{panic(e)};fmt.Println(os.Getpid(),"ready");listener.Accept()}`
	dir := t.TempDir()
	program, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ready := &nativeMemberReadyWriter{cancel: cancel}
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, ready, nil))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = runner.Run(ctx, program.File)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation of blocking Accept, got %v", err)
	}
	if time.Since(started) > 15*time.Second {
		t.Fatal("native method cancellation exceeded bound")
	}
	pid := strings.Fields(ready.output.String())[0]
	for attempt := 0; attempt < 30; attempt++ {
		if exec.Command("/bin/kill", "-0", pid).Run() != nil {
			break
		}
		if attempt == 29 {
			t.Fatal("native listener process survived cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".bashpp*"))
	if len(leftovers) != 0 {
		t.Fatalf("helper artifacts survived cancellation: %v", leftovers)
	}
}

type nativeMemberReadyWriter struct {
	cancel context.CancelFunc
	output bytes.Buffer
}

func (w *nativeMemberReadyWriter) Write(p []byte) (int, error) {
	n, err := w.output.Write(p)
	if bytes.Contains(p, []byte("ready")) {
		time.AfterFunc(200*time.Millisecond, w.cancel)
	}
	return n, err
}

func TestGoSourceNativeMemberFieldStorage(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "dep"), 0700); err != nil {
		t.Fatal(err)
	}
	source := `package main
import("fmt";"example.com/member/dep")
func main(){holder:=dep.New();fmt.Println(holder.Counter.Add(2));counter:=holder.Counter;fmt.Println(counter.Add(3),holder.Counter.Read());read:=holder.Counter.Read;holder.Counter.Add(4);fmt.Println(read(),holder.Counter.Read());add:=holder.Counter.Add;fmt.Println(add(1),holder.Counter.Read());fmt.Println(holder.Counter.Before(holder.Counter.Add(1)),holder.Counter.Read())}`
	dependency := `package dep
type Counter struct {n int}
func(c *Counter) Add(n int)int{c.n+=n;return c.n}
func(c Counter) Read()int{return c.n}
func(c Counter) Before(_ int)int{return c.n}
type Holder struct {Counter Counter}
func New()*Holder{return &Holder{}}`
	files := map[string]string{"go.mod": "module example.com/member\n\ngo 1.26\n", "dep/dep.go": dependency, "main.go": source}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "main.go")
	binary := filepath.Join(dir, "oracle")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-o", binary, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("oracle build: %v %s", err, out)
	}
	native := exec.CommandContext(ctx, binary)
	native.Dir = dir
	want, err := native.CombinedOutput()
	if err != nil {
		t.Fatalf("oracle: %v %s", err, want)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatal(err)
	}
	var output, errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &output, &errs))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v %s", err, errs.String())
	}
	if output.String() != string(want) || errs.Len() != 0 {
		t.Fatalf("Runner %q/%q; native %q", output.String(), errs.String(), want)
	}
	for name, body := range files {
		after, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(after) != body {
			t.Fatalf("original input changed: %s", name)
		}
	}
}
