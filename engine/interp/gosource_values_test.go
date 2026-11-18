//go:build full

package interp_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestGoSourceCallValuesMatchGo(t *testing.T) {
	pinsData, err := os.ReadFile("testdata/gosource-calls/sha256.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]string
	if err = json.Unmarshal(pinsData, &pins); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"reuse-captured-cell.go": `package main
import "fmt"
func main(){x:=1;f:=func()int{return x};x,y:=2,3;fmt.Println(f(),x,y)}`,

		"tuple-results.go": `package main
import "fmt"
type Box struct{N int}
func values(x int)(int,string,*Box){return x+3,"a"+"b",&Box{N:x*2}}
func main(){a,b,c:=values(4);fmt.Println(a,b,c.N)}`,
		"result-order.go": `package main
import "fmt"
var n int
func next()int{n++;return n}
func pair()(int,int){return next(),next()}
func main(){a,b:=pair();fmt.Println(a,b,n);c,d:=next(),next();fmt.Println(c,d,n)}`,
		"computed-function.go": `package main
import "fmt"
var calls int
func factory()func(int)int{calls++;return func(x int)int{return x+1}}
func main(){fmt.Println(factory()(2));fmt.Println(calls)}`,
		"returned-function.go": `package main
import "fmt"
func identity(f func()int)func()int{return f}
func main(){f:=identity(func()int{return 42});fmt.Println(f())}`,
	}
	for name, digest := range pins {
		data, err := os.ReadFile("testdata/gosource-calls/" + name + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
			t.Fatal("original Tour source changed")
		}
		cases[name] = string(data)
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			want := runNativeOracle(t, dir, path, nil, "")
			got := runGoSourceRunner(t, dir, path, source, nil, "")
			if got != want {
				t.Fatalf("interpreted=%#v native=%#v", got, want)
			}
			p, err := gosource.Parse(bytes.NewBufferString(source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var wire bytes.Buffer
			if err := typedjson.Encode(&wire, p.File); err != nil {
				t.Fatal(err)
			}
			decoded, err := typedjson.Decode(&wire)
			if err != nil {
				t.Fatal(err)
			}
			result, err := lower.Compile(decoded.(*syntax.File), lower.Options{Origin: path})
			if err != nil {
				t.Fatal(err)
			}
			generated := filepath.Join(dir, "generated.go")
			binary := filepath.Join(dir, "compiled")
			if err := os.WriteFile(generated, result.Source, 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("go", "build", "-p", "2", "-o", binary, generated).CombinedOutput(); err != nil {
				t.Fatalf("build: %v %s", err, out)
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != source {
				t.Fatal("original changed")
			}
			for _, sourcePath := range []string{path, generated} {
				if err := os.Remove(sourcePath); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(binary)
			cmd.Dir = dir
			cmd.Env = []string{"PATH="}
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("compiled: %v %s", err, stderr.String())
			}
			if stdout.String() != want.stdout || stderr.String() != want.stderr {
				t.Fatalf("compiled stdout=%q stderr=%q native=%#v", stdout.String(), stderr.String(), want)
			}
		})
	}
}
