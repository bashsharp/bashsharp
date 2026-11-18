//go:build full

package interp_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

func TestGoSourceResultBindingThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"named_native_scalar": `package main
import "fmt"
import "time"
func main(){var d time.Duration;var err error;d,err=time.ParseDuration("1s");fmt.Println(d==time.Second,err==nil);fmt.Printf("%T\n",d)}`,
		"assignment_order": `package main
import "fmt"
import "strings"
var count int
func input()string{count++;return "a:b"}
func main(){s:="old";ok:=false;s,s,ok=strings.Cut(input(),":");fmt.Println(s,ok,count)}`,
		"native_assignment": `package main
import "bufio"
import "fmt"
import "os"
func main(){var out *bufio.Writer;out=bufio.NewWriter(os.Stdout);var n int;var err error;n,err=out.WriteString("hello\n");out.Flush();fmt.Println(n,err==nil)}`,
		"native_tuple": `package main
import "fmt"
import "strconv"
func main(){var n int;var err error;n,err=strconv.Atoi("12");fmt.Println(n,err==nil);n,err=strconv.Atoi("bad");fmt.Println(n,err!=nil,err.Error());_,err=strconv.Atoi("7");fmt.Println(err==nil)}`,
		"native_named_result": `package main
import "fmt"
import "time"
func zero()(t time.Time){return}
func main(){t:=zero();fmt.Println(t.IsZero())}`,
		"original_carry_result": `package main
import "fmt"
type Uint64 struct{hi,lo uint32}
func(a Uint64)Plus(b Uint64)(c Uint64){var carry uint32;if c.lo=a.lo+b.lo;c.lo<a.lo{carry=1};c.hi=a.hi+b.hi+carry;return}
func main(){a:=Uint64{1,4294967295};b:=Uint64{2,1};c:=a.Plus(b);fmt.Println(c.hi,c.lo)}`,
		"deferred_named_result": `package main
import "fmt"
type S struct{N int;A [2]int}
func value()(s S){defer func(){s.N++;s.A[1]=9}();return S{N:4}}
func main(){a:=value();b:=a;b.A[1]=3;fmt.Println(a.N,a.A,b.A)}`,
		"named_zero_results": `package main
import "fmt"
type S struct{N int}
func zero()(s S,a [2]int,p *S,m map[string]int,x []int,err error,n int,b bool,f float64){return}
func main(){s,a,p,m,x,err,n,b,f:=zero();fmt.Println(s.N,a,p==nil,m==nil,x==nil,err==nil,n,b,f)}`,
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
