//go:build full

package interp_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceCollectionRepairThreeModes(t *testing.T) {
	cases := map[string]string{
		"nil_map_comma_ok": `package main
import "fmt"
func main(){var m map[string]int;fmt.Println(m["x"]);v,ok:=m["x"];fmt.Println(v,ok)}`,
		"map_key_once_and_value_copy": `package main
import "fmt"
type Item struct{N int}
func key()string{fmt.Println("key");return "x"}
func main(){m:=map[string]Item{"x":{7}};v,ok:=m[key()];v.N=9;fmt.Println(v,ok,m["x"]);missing,exists:=m["missing"];fmt.Println(missing,exists)}`,
		"nil_slice_nested_builtins": `package main
import "fmt"
func main(){var s []int;fmt.Println(len(s[:0]),cap(s[:0]),s[:0]==nil);fmt.Println(len(append(s[:0],7)));fmt.Println(len(make([]int,2,5)),cap(make([]int,2,5)))}`,
		"copy_slice_operands_once": `package main
import "fmt"
func index()int{fmt.Println("index");return 1}
func main(){a:=[]int{1,2,3};b:=make([]int,2);fmt.Println(copy(b,a[index():]),a,b);b[0]=9;fmt.Println(a,b)}`,
		"indexed_make_and_alias": `package main
import "fmt"
func main(){rows:=make([][]int,2);rows[0]=make([]int,2);rows[1]=make([]int,1);alias:=rows[0];alias[1]=7;fmt.Println(rows,alias);rows[0]=append(rows[0],8);rows[0][0]=9;fmt.Println(rows,alias)}`,
		"indexed_native_arg_once": `package main
import "fmt"
type Count int
func key()string{fmt.Println("key");return "x"}
func index()int{fmt.Println("index");return 0}
func main(){m:=map[string]Count{"x":7};xs:=[]byte{9};fmt.Printf("%T %v %T %v\n",m[key()],m[key()],xs[index()],xs[index()])}`,
		"assignment_key_before_rhs": `package main
import "fmt"
func key()string{fmt.Println("key");return "x"}
func value()[]int{fmt.Println("rhs");return []int{7}}
func main(){m:=map[string][]int{};m[key()]=value();fmt.Println(m["x"])}`,
		"native_value_in_struct": `package main
import "fmt"
import "time"
type Entry struct{When time.Time}
func main(){e:=Entry{time.Date(2001,time.January,2,0,0,0,0,time.UTC)};when:=e.When;fmt.Println(when.Year());fmt.Printf("%T %v\n",e.When,e.When)}`,
		"make_length_once": `package main
import "fmt"
func size()int{fmt.Println("size");return 2}
func main(){fmt.Println(len(make([]int,size())),cap(make([]int,size(),4)))}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
func TestGoSourceCollectionInvalidIndexDoesNotPanicHost(t *testing.T) {
	for name, source := range map[string]string{
		"nil":       `package main;func main(){var s []int;println(s[0])}`,
		"nil_write": `package main;func main(){var s []int;s[0]=1}`,
		"bounds":    `package main;func main(){s:=[]int{1};println(s[2])}`,
	} {
		t.Run(name, func(t *testing.T) {
			program, err := gosource.Parse(strings.NewReader(source), "invalid-index.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out, errout bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &errout))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Any escaping host panic fails the test. This is a negative containment
			// check: since Sprint 162 the interpreter reports an out-of-range index
			// the way Go's runtime does (a recoverable panic with Go's wording and
			// exit status 2); the older static BOUNDS diagnostic is still accepted
			// for the paths that refuse an index before evaluation.
			err = runner.Run(ctx, program.File)
			bounds := func(s string) bool {
				return strings.Contains(s, "BOUNDS") || strings.Contains(s, "runtime error: index out of range")
			}
			if err == nil || (!bounds(err.Error()) && !bounds(errout.String())) {
				t.Fatalf("missing bounds failure: %v %q", err, errout.String())
			}
		})
	}
}
func TestGoSourceOriginalCollectionRepairThreeModes(t *testing.T) {
	fixtures := map[string]string{
		"making-slices.go.txt": "a4dbf0f6f266bb68e7da1cdcbdba7c4bb8711a3065c93ad2dffd7fc4f7c09b1d",
		"nil-slices.go.txt":    "73d805ba5558987b2695d6c72ba640a91bc59eade8741a61505f871e665ed1d9",
		"mutating-maps.go.txt": "a8f33f1af579189d54372c64e2c9648110b968ea5972e58cc261f7ff9f96a15b",
	}
	for name, digest := range fixtures {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata/gosource-collections", name))
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(source)) != digest {
				t.Fatal("original bytes drifted")
			}
			typedSendThreeModes(t, string(source))
		})
	}
}
