//go:build full

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoSourceLocalPrivateFieldsThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"private_value_and_format": `package main
import "fmt"
type Pair struct{ lo uint32; Hi uint32 }
func(p Pair)String()string{return fmt.Sprintf("%d/%d",p.lo,p.Hi)}
func main(){p:=Pair{7,9};fmt.Printf("%s %T\n",p,p);fmt.Println(p.lo,p.Hi)}`,
		"private_pointer_writes": `package main
import "fmt"
type Counter struct{ n int; Label string }
func(c *Counter)String()string{c.n++;return fmt.Sprintf("%s:%d",c.Label,c.n)}
func main(){c:=&Counter{2,"count"};alias:=c;fmt.Printf("%s %s\n",c,alias);fmt.Println(c.n,alias.n)}`,
		"private_nested_and_tags": `package main
import "fmt"
type Inner struct{ n int }
type Outer struct{ inner Inner; Data int ` + "`json:\"data\"`" + ` }
func(o Outer)String()string{return fmt.Sprintf("%d/%d",o.inner.n,o.Data)}
func main(){o:=Outer{Inner{4},6};fmt.Printf("%s %T\n",o,o);fmt.Printf("%+v\n",Inner{8})}`,
		"empty_and_defined_struct": `package main
import "fmt"
type Empty struct{}
type Original struct{ n int }
type Copy Original
func main(){fmt.Printf("%+v %+v\n",Empty{},Copy{9})}`,
		"anonymous_nested_private": `package main
import "fmt"
type Outer struct{ inner struct{ n int }; label string }
func(o Outer)String()string{return fmt.Sprintf("%d/%s",o.inner.n,o.label)}
func main(){o:=Outer{struct{ n int }{3},"nested"};fmt.Printf("%s %T\n",o,o)}`,
		"native_object_interface_field": `package main
import "fmt"
import "time"
type Event struct{ when interface{}; label string }
func(e Event)String()string{return fmt.Sprintf("%s=%v",e.label,e.when)}
func main(){when:=time.Date(2020,1,2,3,4,5,0,time.UTC);e:=Event{when,"event"};fmt.Println(e)}`,
		"native_pointer_interface_field": `package main
import "fmt"
import "math/big"
type Event struct{ payload interface{}; label string }
func(e Event)String()string{return fmt.Sprintf("%s=%v",e.label,e.payload)}
func main(){payload:=big.NewInt(17);e:=Event{payload,"native"};fmt.Println(e);payload.SetInt64(19);fmt.Println(e)}`,
		"anonymous_tag_identity": `package main
import "fmt"
type Outer struct{ inner struct{ n int ` + "`json:\"n x\"`" + ` } }
func main(){o:=Outer{struct{ n int ` + "`json:\"n x\"`" + ` }{3}};fmt.Printf("%#v %T\n",o,o.inner)}`,
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

func TestGoSourceLocalCodecCancelReset(t *testing.T) {
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
type Tag struct{ n int }
func(t *Tag)String()string{t.n++;println("callback-ready");time.Sleep(time.Minute);return "late"}
func main(){fmt.Fprintf(os.Stdout,"%s\n",&Tag{1});println("after")}`
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
type Tag struct{ n int }
func(t *Tag)String()string{t.n++;return fmt.Sprint(t.n)}
func main(){fmt.Fprintln(os.Stdout,&Tag{6})}`
	if err := r.Run(context.Background(), load(source).File); err != nil {
		t.Fatal(err)
	}
	if output.String() != "7\n" {
		t.Fatalf("reset: %q", output.String())
	}
}
