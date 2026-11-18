//go:build full

package interp_test

import (
	"bytes"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGoSourceInvocationResultsThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"computed_struct_arguments": `package main
import "fmt"
type U struct{ n uint32 }
type I struct{ n int32 }
func(a I)U()(b U){b.n=uint32(a.n);return}
func(a U)I()(b I){b.n=int32(a.n);return}
func(a U)Plus(b U)(c U){c.n=a.n+b.n;return}
func(a I)Plus(b I)(c I){return a.U().Plus(b.U()).I()}
func main(){a:=I{3};b:=I{4};c:=a.Plus(b);fmt.Println(c.n)}`,
		"argument_order_and_copies": `package main
import "fmt"
type S struct{ n int }
var order int
func next()S{order++;return S{order}}
func combine(a,b S)S{a.n=a.n*10+b.n;return a}
func main(){s:=combine(next(),next());fmt.Println(s.n,order);a:=S{4};b:=combine(a,next());fmt.Println(a.n,b.n,order)}`,
		"builtin_returns": `package main
import "fmt"
func count(bs []byte)int{return len(bs)}
func capacity(bs []byte)int{return cap(bs)}
func grow(xs []int)[]int{return append(xs,3)}
func main(){bs:=[]byte{1,2};fmt.Println(count(bs),capacity(bs));xs:=[]int{1,2};ys:=grow(xs);fmt.Println(xs,ys)}`,
		"shadowed_builtin_returns": `package main
import "fmt"
func len(n int)int{return n+7}
func compute()int{return len(3)}
func main(){fmt.Println(compute())}`,
		"unnamed_parameters": `package main
import "fmt"
type S struct{ n int }
func(s S)Read(int)int{return s.n}
func ignore(int,string)int{return 9}
func main(){fmt.Println(S{4}.Read(5),ignore(6,"x"))}`,
		"struct_pointer_and_interface_results": `package main
import "fmt"
type S struct{ n int }
func makeS()*S{return &S{3}}
func bump(s *S)*S{s.n++;return s}
func wrap(s *S)interface{}{return s}
func main(){p:=bump(makeS());fmt.Println(p.n);v:=wrap(p);fmt.Printf("%T\n",v)}`,
		"unsigned_complement_width": `package main
import "fmt"
type Word uint16
func main(){fmt.Println(^uint8(0),^uint16(0),^uint32(0),^uint64(0),^Word(0),^int8(0),^0);n:=uint32(1);fmt.Println(^n)}`,
		"launched_argument_once": `package main
import "fmt"
var n int
func next()int{n++;return n}
func send(c chan int,v int){c<-v}
func main(){c:=make(chan int);go send(c,next());v:=<-c;fmt.Println(v,n)}`,
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
