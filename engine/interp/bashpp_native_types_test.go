//go:build full

package interp_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
import (
	"bytes"
	"context"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
	"testing"
	"time"
)

func TestGoSourceImportedTypeIdentity(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"private_field_type", `package main
import "time"
import "fmt"
type MyError struct {When time.Time;What string}
func main(){var stamp time.Time;fmt.Println(stamp.IsZero())}`, "true\n"},
		{"path_error_global", `package main
import "io/fs"
import "errors"
import "fmt"
var problem=&fs.PathError{Op:"open",Path:"file",Err:errors.New("broken")}
func main(){fmt.Println(problem.Error())}`, "open file: broken\n"},
		{"zero_mutable", `package main
import "sync/atomic"
import "sync"
import "fmt"
var count atomic.Uint64
var lock sync.Mutex
func main(){lock.Lock();count.Add(3);lock.Unlock();fmt.Println(count.Load())}`, "3\n"},
		{"typed_nil_interface", `package main
import "io/fs"
import "fmt"
func main(){var ptr *fs.PathError;var err error=ptr;fmt.Println(ptr==nil,err==nil)}`, "true false\n"},
		{"error_identity", `package main
import "errors"
import "fmt"
func main(){fmt.Println(errors.New("abc")==errors.New("abc"));err:=errors.New("abc");fmt.Println(err==err,err!=err,err.Error())}`, "false\ntrue false abc\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program, err := gosource.Parse(strings.NewReader(tc.source), "original.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out, errs bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errs))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err = runner.Run(ctx, program.File); err != nil {
				t.Fatalf("run: %v; %s", err, errs.String())
			}
			if out.String() != tc.want || errs.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q want=%q", out.String(), errs.String(), tc.want)
			}
		})
	}
}
