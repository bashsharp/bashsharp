//go:build full

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceNativeWriterFormattingThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"buffered_string_effects": `package main
import "bufio"
import "fmt"
import "os"
type Counter struct{N int}
func(c *Counter)String()string{c.N++;return fmt.Sprintf("count=%d",c.N)}
func main(){w:=bufio.NewWriter(os.Stdout);c:=&Counter{};fmt.Fprint(w,c);fmt.Fprintln(w,c);fmt.Fprintf(w,"%s\n",c);w.Flush();fmt.Println(c.N)}`,
		"error_method": `package main
import "fmt"
import "os"
type E int
var calls int
func(e E)Error()string{calls++;return fmt.Sprintf("error=%d",int(e))}
func main(){fmt.Fprintf(os.Stdout,"%v/%s\n",E(3),E(4));fmt.Println(calls)}`,
		"nested_reentry": `package main
import "fmt"
import "os"
type Inner int
type Outer int
var count int
func(i Inner)String()string{count++;return fmt.Sprint(int(i))}
func(o Outer)String()string{return fmt.Sprintf("outer(%s)",Inner(o))}
func main(){fmt.Fprintf(os.Stdout,"%s\n",Outer(7));fmt.Println(count)}`,
		"format_recovers_callback_panic": `package main
import "fmt"
import "os"
type Tag int
func(_ Tag)String()string{panic("boom")}
func main(){fmt.Fprintf(os.Stdout,"%s\n",Tag(1));fmt.Println("after")}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			want := runNativeOracle(t, dir, path, nil, "")
			if got := runGoSourceRunner(t, dir, path, source, nil, ""); got != want {
				t.Fatalf("interpreter=%+v native=%+v", got, want)
			}
			p, err := gosource.Parse(bytes.NewBufferString(source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := lower.Compile(p.File, lower.Options{Origin: path})
			if err != nil {
				t.Fatal(err)
			}
			generated, binary := filepath.Join(dir, "generated.go"), filepath.Join(dir, "compiled")
			if err := os.WriteFile(generated, result.Source, 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("go", "build", "-p", "2", "-o", binary, generated).CombinedOutput(); err != nil {
				t.Fatalf("build: %v %s", err, out)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != source {
				t.Fatal("original source changed")
			}
			for _, file := range []string{path, generated} {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			var out, stderr bytes.Buffer
			cmd := exec.Command(binary)
			cmd.Dir = dir
			cmd.Env = []string{"PATH="}
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("artifact: %v %s", err, stderr.String())
			}
			if want.status != 0 || out.String() != want.stdout || stderr.String() != want.stderr {
				t.Fatalf("artifact=%q/%q oracle=%+v", out.String(), stderr.String(), want)
			}
		})
	}
}

func TestGoSourceNativeWriterCancelReset(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output := &callbackCancelWriter{cancel: cancel}
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, output, output))
	if err != nil {
		t.Fatal(err)
	}
	load := func(source string) *gosource.Program {
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	source := `package main
import "fmt"
import "os"
import "time"
type Tag int
func(_ Tag)String()string{println("callback-ready");time.Sleep(time.Minute);return "late"}
func main(){fmt.Fprintf(os.Stdout,"%s\n",Tag(1));println("after")}`
	if err := r.Run(ctx, load(source).File); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v %q", err, output.String())
	}
	if strings.Contains(output.String(), "after") || strings.Contains(output.String(), "late") {
		t.Fatal("continued after cancellation")
	}
	r.Reset()
	output.Reset()
	source = `package main
import "fmt"
import "os"
type Tag int
func(t Tag)String()string{return fmt.Sprint(int(t))}
func main(){fmt.Fprintln(os.Stdout,Tag(7))}`
	if err := r.Run(context.Background(), load(source).File); err != nil {
		t.Fatal(err)
	}
	if output.String() != "7\n" {
		t.Fatalf("reset: %q", output.String())
	}
}
func TestGoSourceOriginalWriterCallback(t *testing.T) {
	source := `package main
import "fmt"
type Writer struct{}
func(w Writer)Write(p []byte)(int,error){println("write-ran");return len(p),nil}
type Tag int
func(t Tag)String()string{println("string-ran");return "tag"}
func main(){fmt.Fprintf(Writer{},"%s",Tag(1));println("after")}`
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	want := runNativeOracle(t, dir, path, nil, "")
	if want.status != 0 || !strings.Contains(want.stderr, "write-ran") || !strings.Contains(want.stderr, "string-ran") {
		t.Fatalf("native: %+v", want)
	}
	p, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), p.File)
	if err != nil || output.String() != want.stderr {
		t.Fatalf("callback: err=%v output=%q want=%q", err, output.String(), want.stderr)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != source {
		t.Fatal("original source changed")
	}
}
