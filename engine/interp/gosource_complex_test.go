//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceComplexThreeModes(t *testing.T) {
	original, err := os.ReadFile("testdata/gosource-complex/basic-types.go")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"tour_basic_types": string(original),
		"assignment_and_local_call": `package main
import "fmt"
func twice(z complex64) complex64{return z*2}
func main(){var z complex64=16777216+2i;z+=1+1i;z=twice(z);fmt.Printf("%T %v\n",z,z);a:=2+3i;fmt.Printf("%T %v\n",a,a)}
`,
		"arithmetic": `package main
import "fmt"
func main(){var z complex128 = 2+3i; var w complex128 = 1-2i; fmt.Printf("%T %v %v %v %v %v %v %v\n",z,z+w,z-w,z*w,z/w,-z,real(z),imag(z));fmt.Println(z==z,z!=w)}
`,
		"width_and_builtins": `package main
import "fmt"
import "math/cmplx"
func main(){var z complex64 = 16777217+16777219i; x:=complex(float32(1.25),float32(2.5));y:=complex(1.25,2.5);var zero complex128;fmt.Printf("%T %v %T %v %T %v %T %v %T %v %v\n",z,z,x,x,y,y,real(x),real(x),imag(x),imag(x),zero);fmt.Printf("%T %v\n",cmplx.Sqrt(-5+12i),cmplx.Sqrt(-5+12i));fmt.Println(cmplx.Sqrt(4),cmplx.Sqrt(4.0))}
`,
		"exact_constants": `package main
import "fmt"
const z = (1+2i)/3
const tiny = (1i/3)*3
func main(){fmt.Printf("%T %v %v\n",z,z,tiny)}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			sdk := filepath.Join(runtime.GOROOT(), "bin", "go")
			build := func(input, output string) {
				t.Helper()
				cmd := exec.Command(sdk, "build", "-p", "2", "-o", output, input)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("build: %v %s", err, out)
				}
			}
			run := func(binary string) (string, string) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary)
				cmd.Dir = dir
				var out, stderr bytes.Buffer
				cmd.Stdout = &out
				cmd.Stderr = &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("run: %v %s", err, stderr.String())
				}
				return out.String(), stderr.String()
			}
			oracle := filepath.Join(dir, "oracle")
			build(path, oracle)
			wantOut, wantErr := run(oracle)
			program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := r.Run(ctx, program.File); err != nil {
				t.Fatalf("Runner: %v stdout=%q stderr=%q", err, out.String(), stderr.String())
			}
			if out.String() != wantOut || stderr.String() != wantErr {
				t.Fatalf("Runner %q %q; oracle %q %q", out.String(), stderr.String(), wantOut, wantErr)
			}
			lowered, err := lower.Compile(program.File, lower.Options{Origin: path, Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			generated := filepath.Join(dir, "generated.go")
			if err := os.WriteFile(generated, lowered.Source, 0600); err != nil {
				t.Fatal(err)
			}
			artifact := filepath.Join(dir, "compiled")
			build(generated, artifact)
			after, err := os.ReadFile(path)
			if err != nil || string(after) != source {
				t.Fatal("original changed")
			}
			os.Remove(path)
			os.Remove(generated)
			gotOut, gotErr := run(artifact)
			if gotOut != wantOut || gotErr != wantErr {
				t.Fatalf("compiled %q %q; oracle %q %q", gotOut, gotErr, wantOut, wantErr)
			}
		})
	}
}

// Go's complex family is predeclared rather than an ordinary function.  In
// particular, a single multi-result call supplies both complex operands; the
// scalar spelling must not collapse that call to one argument.
func TestGoSourceComplexBuiltinValuePath(t *testing.T) {
	for name, tc := range map[string]struct{ source, want string }{
		"spread": {`package main
import "fmt"
func parts() (float64, float64) { return 5, 7 }
func main() { z := complex(parts()); fmt.Printf("%T %v %v %v\n", z, z, real(z), imag(z)) }
`, "complex128 (5+7i) 5 7\n"},
		"complex_collection": {`package main
import "fmt"
func main() { z := complex(5.0, 7.0); a := append([]complex128{}, z); fmt.Printf("%T %v %v\n", a[0], a[0], real(a[0])) }
		`, "complex128 (5+7i) 5\n"},
	} {
		t.Run(name, func(t *testing.T) {
			out, stderr, err := runGoSource(t, name, tc.source)
			if err != nil {
				t.Fatalf("Runner: %v stdout=%q stderr=%q", err, out, stderr)
			}
			if out != tc.want || stderr != "" {
				t.Fatalf("stdout=%q stderr=%q; want %q", out, stderr, tc.want)
			}
		})
	}
}

func TestGoSourceComplexBuiltinRejectsInvalidOperands(t *testing.T) {
	for name, tc := range map[string]struct{ source, want string }{
		"complex_arity": {`package main; func main() { _ = complex(1) }`, "not enough arguments for complex"},
		"real_string":   {`package main; func main() { _ = real("x") }`, "expected complex type"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gosource.Parse(strings.NewReader(tc.source), name+".go", gosource.Options{RunMain: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v; want %q", err, tc.want)
			}
		})
	}
}
