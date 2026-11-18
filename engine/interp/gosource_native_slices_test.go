//go:build full

package interp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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

func TestGoSourceNativeReadSlicesThreeModes(t *testing.T) {
	original, err := os.ReadFile("testdata/gosource-native-slices/reader.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(original)) != "efe45163df17ea9c24da5ad0ba634265f680165183bb0bc98ed33b171c5164e0" {
		t.Fatal("unchanged Tour source digest mismatch")
	}
	for name, source := range map[string]string{
		"unchanged_tour_reader": string(original),
		"aliased_subslice_capacity": `package main
import "fmt"
import "strings"
func main(){b:=[]byte{9,9,9,9,9,9};a:=b[1:5:5];alias:=b[2:6];r:=strings.NewReader("ABC");n,err:=r.Read(a[:3]);fmt.Println(n,err,b,alias,len(a),cap(a))}`,
		"partial_read_error": `package main
import "fmt"
import "strings"
import "io"
func main(){b:=[]byte{9,9,9,9};r:=strings.NewReader("xy");n,err:=io.ReadFull(r,b);fmt.Println(n,err,b);n,err=io.ReadFull(r,b);fmt.Println(n,err,b)}`,
		"read_at_and_method_value": `package main
import "fmt"
import "strings"
func main(){b:=[]byte{9,9,9};r:=strings.NewReader("abc");n,err:=r.ReadAt(b,1);fmt.Println(n,err,b);read:=r.Read;n,err=read(b);fmt.Println(n,err,b)}`,
		"buffer_expression_once": `package main
import "fmt"
import "strings"
var calls int
var data=[]byte{9,9,9,9}
func buffer()[]byte{calls++;return data[1:3]}
func main(){r:=strings.NewReader("hi");n,err:=r.Read(buffer());fmt.Println(n,err,data,calls)}`,
		"deferred_slice_header": `package main
import "fmt"
import "strings"
func work(b []byte){r:=strings.NewReader("X");defer r.Read(b);b[1]=7;b=[]byte{4,5,6}}
func main(){b:=[]byte{1,2,3};work(b);fmt.Println(b)}`,
		"readonly_argument_alias": `package main
import "fmt"
import "bytes"
var calls int
func change(b []byte)[]byte{calls++;b[0]=7;return b}
func main(){b:=[]byte{1,2};same:=bytes.Equal(b,change(b));fmt.Println(same,b,calls)}`,
		"native_reader_pointer_state": `package main
import "fmt"
import "bytes"
func main(){r:=bytes.NewBufferString("abcd");alias:=r;b:=make([]byte,2);r.Read(b);fmt.Println(b,alias.String());alias.Read(b);fmt.Println(b,r.Len())}`,
		"capacity_zero_storage": `package main
import "fmt"
import "strings"
func main(){b:=make([]byte,2,4);alias:=b[:4];r:=strings.NewReader("AB");n,err:=r.Read(b);fmt.Println(n,err,b,alias,len(b),cap(b))}`,
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

func TestGoSourceNativeReadSliceBoundaries(t *testing.T) {
	for name, source := range map[string]string{
		"callback_alias_observation": `package main
import "fmt"
var b=[]byte{1}
type Tag int
func(t Tag)String()string{println("original-read");b[0]=7;return "tag"}
func main(){fmt.Println(Tag(1),b);println("after")}`,
		"retaining_constructor": `package main
import "bytes"
func main(){b:=[]byte{1,2};r:=bytes.NewReader(b);_ = r;println("after")}`,
		"original_reader": `package main
import "io"
type Reader struct{}
func(r Reader)Read(b []byte)(int,error){println("original-read");b[0]=7;return 1,nil}
func main(){b:=make([]byte,1);io.ReadFull(Reader{},b);println("after")}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			want := runNativeOracle(t, dir, path, nil, "")
			if want.status != 0 {
				t.Fatalf("oracle: %+v", want)
			}
			if name == "callback_alias_observation" {
				if got := runGoSourceRunner(t, dir, path, source, nil, ""); got != want {
					t.Fatalf("callback alias: interpreter=%+v native=%+v", got, want)
				}
				return
			}
			p, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			err = r.Run(context.Background(), p.File)
			if err == nil || !strings.Contains(err.Error(), "unsupported") || strings.Contains(out.String(), "after") || strings.Contains(out.String(), "original-read") {
				t.Fatalf("boundary: %v %q", err, out.String())
			}
		})
	}
}
func TestGoSourceNativeReadSliceCancelReset(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out := &callbackCancelWriter{cancel: cancel}
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, out, out))
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
import "os"
func main(){r,w,_:=os.Pipe();defer r.Close();defer w.Close();b:=make([]byte,2);println("callback-ready");r.Read(b);println("after")}`
	if err := r.Run(ctx, load(source).File); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v %q", err, out.String())
	}
	if strings.Contains(out.String(), "after") {
		t.Fatal("continued after canceled read")
	}
	r.Reset()
	out.Reset()
	source = `package main
import "strings"
import "fmt"
func main(){r:=strings.NewReader("hi");b:=make([]byte,2);r.Read(b);fmt.Println(b)}`
	if err := r.Run(context.Background(), load(source).File); err != nil {
		t.Fatal(err)
	}
	if out.String() != "[104 105]\n" {
		t.Fatalf("reset: %q", out.String())
	}
}
