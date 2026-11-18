//go:build full

package interp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceReaderCallbackThreeModes(t *testing.T) {
	original, err := os.ReadFile("testdata/gosource-reader-callbacks/rot13.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(original)) != "a46d83576a2ef31663d22225a32740c3ab70d3dc11066318ec471c57cd2a854f" {
		t.Fatal("original rot13 bytes changed")
	}
	for name, source := range map[string]string{
		"unchanged_rot13": string(original),
		"local_buffer_capacity": `package main
import("fmt";"io";"bytes")
type reader struct{}
func(r reader)Read(p []byte)(int,error){for i:=range p{p[i]=255};return len(p),nil}
func main(){b:=bytes.NewBufferString("01234567").Bytes();n,e:=io.ReadFull(reader{},b[2:5:7]);fmt.Printf("%v %d %v\n",b,n,e)}`,
		"shadowed_len_retains_buffer": `package main
import("fmt";"io";"bytes")
var saved []byte
func len(p []byte)int{saved=p;return 3}
type reader struct{}
func(r reader)Read(p []byte)(int,error){for i:=range p{p[i]='A'};return len(p),nil}
func main(){b:=bytes.NewBufferString("012").Bytes();n,e:=io.ReadFull(reader{},b);saved[1]='Z';fmt.Printf("%s %d %v\n",b,n,e)}`,

		"partial_error_state": `package main
import("fmt";"io";"bytes")
type reader struct{calls int}
func(r *reader)Read(p []byte)(int,error){r.calls++;p[0]='A';p[1]='B';return 2,io.ErrUnexpectedEOF}
func main(){r:=reader{};out:=bytes.NewBufferString("");n,e:=io.Copy(out,&r);fmt.Println(n,e,out.String(),r.calls)}`,
		"retained_overlapping_buffer": `package main
import("fmt";"io")
var retained []byte
type reader struct{calls int}
func(r *reader)Read(p []byte)(int,error){r.calls++;if r.calls==1{retained=p[:2:3];alias:=p[1:3];alias[0]='X';p[0]='A';return 2,nil};retained[0]='B';p[0]='C';return 1,io.EOF}
func main(){r:=reader{};b,e:=io.ReadAll(&r);fmt.Printf("%s %v %d %d %d %d\n",b,e,retained[0],len(retained),cap(retained),r.calls)}`,
		"panic_writes_visible": `package main
import("fmt";"io")
var retained []byte
type reader struct{}
func(r reader)Read(p []byte)(int,error){retained=p;p[0]='X';panic("reader panic")}
func main(){defer func(){fmt.Println(recover(),retained[0])}();io.ReadAll(reader{})}`,
		"byte_rune_aliases": `package main
import "fmt"
func main(){var a byte=3;var b uint8=4;var c rune=5;var d int32=6;fmt.Println(a+b,c+d);}`,
	} {
		t.Run(name, func(t *testing.T) { callbackTourThreeModes(t, source) })
	}
}

// The complete original validator consumes 1 MiB. Keep it as a separate named
// acceptance test because per-byte native buffer operations are deliberately
// not replaced with native execution of the original loop.
func TestGoSourceOriginalReaderValidationThreeModes(t *testing.T) {
	source, err := os.ReadFile("testdata/gosource-reader-callbacks/readers.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(source)) != "6086e0029480b202ff1d5bc81ac9d6343103f25afd40ac56635d0cf65e41038a" {
		t.Fatal("original reader bytes changed")
	}
	callbackTourThreeModes(t, string(source))
}

func TestGoSourceReaderCallbackCancelReset(t *testing.T) {
	dir := callbackTourModule(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output := &callbackCancelWriter{cancel: cancel}
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, output, output))
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
import("io";"time")
type reader struct{}
func(r reader)Read(p []byte)(int,error){p[0]='X';println("callback-ready");time.Sleep(time.Minute);return 1,io.EOF}
func main(){io.ReadAll(reader{});println("after")}`
	if err = r.Run(ctx, callbackTourLoad(t, dir, source).File); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v %q", err, output.String())
	}
	if strings.Contains(output.String(), "after") {
		t.Fatal("continued after canceled Read")
	}
	r.Reset()
	output.Reset()
	source = `package main
import("fmt";"io")
type reader struct{}
func(r reader)Read(p []byte)(int,error){p[0]='Y';return 1,io.EOF}
func main(){b,e:=io.ReadAll(reader{});fmt.Printf("fresh %s %v\n",b,e)}`
	if err = r.Run(context.Background(), callbackTourLoad(t, dir, source).File); err != nil {
		t.Fatal(err)
	}
	if output.String() != "fresh Y <nil>\n" {
		t.Fatalf("Reset state: %q", output.String())
	}
}

func TestGoSourceReaderRetainedCallbackBoundary(t *testing.T) {
	dir := callbackTourModule(t)
	if err := os.Mkdir(filepath.Join(dir, "dep"), 0700); err != nil {
		t.Fatal(err)
	}
	dependency := `package dep
import("fmt";"io")
var saved io.Reader
func Retain(r io.Reader){saved=r;fmt.Println("dependency-retained")}`
	if err := os.WriteFile(filepath.Join(dir, "dep", "retain.go"), []byte(dependency), 0600); err != nil {
		t.Fatal(err)
	}
	source := `package main
import("io";"example.com/callback-test/dep")
type reader struct{}
func(r reader)Read(p []byte)(int,error){println("original-Read");return 0,io.EOF}
func main(){dep.Retain(reader{});println("after")}`
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "oracle")
	cmd := exec.Command("go", "build", "-mod=readonly", "-p", "2", "-o", binary, path)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	cmd = exec.Command(binary)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "dependency-retained\nafter\n" {
		t.Fatalf("native: %v %q", err, output)
	}
	p := callbackTourLoad(t, dir, source)
	var out bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), p.File)
	if err == nil || !strings.Contains(err.Error()+out.String(), "unsupported") {
		t.Fatalf("retained callback accepted: %v %q", err, out.String())
	}
	for _, marker := range []string{"dependency-retained", "original-Read", "after"} {
		if strings.Contains(out.String(), marker) {
			t.Fatalf("retained callback path executed: %q", out.String())
		}
	}
}
