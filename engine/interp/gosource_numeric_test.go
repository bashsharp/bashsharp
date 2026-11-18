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

func TestGoSourceNumericRuntimeThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"large_runtime_shifts": `package main
import "fmt"
func main(){n:=uint64(4294967295);a:=uint64(1);b:=int64(-9);fmt.Println(a<<n,a>>n,b<<n,b>>n);n=64;fmt.Println(1<<n);x:=int64(-9223372036854775808);minus:=int64(-1);fmt.Println(x/minus,x%minus)}`,
		"integer_casts": `package main
import "fmt"
type Tiny uint16
func main(){u:=uint64(4294967295);n:=int64(-1);fmt.Println(uint16(u),int16(u),Tiny(u),uint64(n));x:=int64(-257);fmt.Println(uint8(x),int8(x));b:=uint8(255);fmt.Println(b+1,^b,-b)}`,
		"float_truncation": `package main
import "fmt"
func main(){a:=3.9;b:=-3.9;c:=float32(-257.75);fmt.Println(int(a),int(b),int16(c));u:=uint64(9007199254740993);fmt.Printf("%.0f\n",float64(u))}`,
		"initializer_scope_order": `package main
import "fmt"
var order string
func next()int{order+="n";return 3}
func mark(){order+="m"}
func main(){x:=8;if x=next();x==3{fmt.Println(x,order)};if mark();false{}else if x++;x==4{fmt.Println(x,order)};if x:=next();false{fmt.Println(x)}else{fmt.Println(x,order)};fmt.Println(x)}`,
		"field_carry": `package main
import "fmt"
type Uint64 struct{hi,lo uint32}
func(a Uint64)Plus(b Uint64)Uint64{c:=Uint64{};var carry uint32;if c.lo=a.lo+b.lo;c.lo<a.lo{carry=1};c.hi=a.hi+b.hi+carry;return c}
func main(){a:=Uint64{1,4294967295};b:=Uint64{2,1};c:=a.Plus(b);fmt.Println(c.hi,c.lo)}`,
		"exact_constants": `package main
import "fmt"
const huge=1e1000
const third=1.0/3.0
const precise=9007199254740993.0
const z=complex(1.0/3.0,2)
func main(){fmt.Printf("%T %v %v %v %v\n",huge-huge,huge-huge,third*3==1,precise-9007199254740992.0,real(z)*3==1)}`,
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
