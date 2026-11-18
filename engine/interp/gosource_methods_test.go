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

func TestGoSourceLocalMethodResultThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"direct_pointer_value_receiver": `package main
import "fmt"
type S struct{N int}
var s=S{1}
func pick()*S{return &s}
func arg()int{s.N=9;return 0}
func(s S)Read(unused int)int{return s.N}
func main(){fmt.Println(pick().Read(arg()),s.N)}`,
		"deferred_pointer_value_receiver": `package main
import "fmt"
type S struct{N int}
var s=S{1}
func pick()*S{return &s}
func arg()int{s.N=9;return 0}
func(s S)Print(unused int){fmt.Println(s.N)}
func main(){defer pick().Print(arg());s.N=42}`,
		"chain_original": `package main
import "fmt"
type Uint64 struct { hi,lo uint32 }
type Int64 struct { hi int32;lo uint32 }
func (a Int64) Uint64() (c Uint64) {c.hi=uint32(a.hi);c.lo=a.lo;return}
func (a Uint64) Int64() (c Int64) {c.hi=int32(a.hi);c.lo=a.lo;return}
func (a Uint64) Com() (c Uint64) {c.hi=^a.hi;c.lo=^a.lo;return}
func (a Int64) Com() (c Int64) {return a.Uint64().Com().Int64()}
func main(){a:=Int64{};c:=a.Com();fmt.Println(c.hi,c.lo)}
`,
		"pointer_chain": `package main
import "fmt"
type S struct{N int}
var calls int
func(p *S)Add(n int)*S{p.N+=n;return p}
func pick(p *S)*S{calls++;return p}
func main(){p:=&S{1};q:=pick(p).Add(2).Add(3);fmt.Println(p.N,q.N,p==q,calls)}`,
		"method_snapshot": `package main
import "fmt"
type S struct{N int}
func(s S)Read()int{return s.N}
func(s *S)Add(){s.N++}
func main(){s:=S{1};f:=s.Read;g:=s.Add;s.N=4;g();fmt.Println(f(),s.N);p:=&s;h:=p.Add;p=&S{10};h();fmt.Println(s.N,p.N)}`,
		"indexed_method_value": `package main
import "fmt"
type S struct{N int}
func(s *S)Add(){s.N++}
var calls int
func index()int{calls++;return 0}
func main(){xs:=[]S{{1}};f:=xs[index()].Add;f();f();fmt.Println(xs[0].N,calls)}`,
		"receiver_argument_order": `package main
import "fmt"
type S struct{N int}
var order string
func makeS()S{order+="r";return S{3}}
func arg()int{order+="a";return 4}
func(s S)Add(n int)S{order+="m";s.N+=n;return s}
func main(){v:=makeS().Add(arg());fmt.Println(v.N,order)}`,
		"interface_method_value": `package main
import "fmt"
type I interface{Read()int}
type S struct{N int}
func(s S)Read()int{return s.N}
func main(){var i I=S{2};f:=i.Read;i=S{8};fmt.Println(f(),i.Read())}`,
		"native_struct_argument": `package main
import "fmt"
type S struct{N int}
func(s S)Copy()S{return s}
func main(){s:=S{2};fmt.Println("value",s.Copy().Copy())}`,
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
