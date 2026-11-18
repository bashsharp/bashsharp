//go:build full

package lower

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sources are unchanged public profile cases. Optional external inventory
// verification catches drift without requiring that repository for unit tests.
func TestCheckedValueArtifactParity(t *testing.T) {
	cases := []struct{ name, path, source, header, body string }{
		{"nil_deref", "pointers/nil-deref-neg.bpp", checkedNilSource, "", "var p *int; _ = %s"},
		{"assert_fail", "assertions/assert-fail-neg.bpp", checkedAssertFailSource, "type T int; func(T) M(string) {}; type U int; func(U) M(string) {}; type I interface{M(string)}", "var i I = T(1); _ = %s"},
		{"assert_impossible", "assertions/assert-impossible-neg.bpp", checkedAssertImpossibleSource, "type T int; func(T) M(string) {}; type U int; type I interface{M(string)}", "var i I = T(1); _, _ = %s"},
		{"assert_identity", "interfaces/value-copy-assertion-zero.bpp", checkedAssertIdentitySource, "type Box struct{N int}; func(Box) Show(string) {}; type Shower interface{Show(string)}; type Other int; func(Other) Show(string) {}", ""},
	}
	dir := t.TempDir()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	module := "module checkedartifact\n\ngo 1.25\n\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + filepath.ToSlash(root) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		if profile := os.Getenv("BASHPP_PROFILE_DIR"); profile != "" {
			actual, err := os.ReadFile(filepath.Join(profile, tc.path))
			if err != nil || string(actual) != tc.source {
				t.Fatalf("public source %s differs: %v", tc.path, err)
			}
		}
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.source), tc.path)
		if err != nil {
			t.Fatal(err)
		}
		e := &emitter{prefix: "cv_", options: Options{Origin: tc.path}}
		var assertions []*syntax.BashPPTypeAssertExpr
		var deref *syntax.BashPPDerefExpr
		syntax.Walk(file, func(node syntax.Node) bool {
			switch node := node.(type) {
			case *syntax.BashPPTypeAssertExpr:
				assertions = append(assertions, node)
			case *syntax.BashPPDerefExpr:
				deref = node
			}
			return true
		})
		body := tc.body
		if deref != nil {
			body = fmt.Sprintf(body, e.checkedDeref(deref, "p", "p"))
		} else {
			var emitted []string
			for i, node := range assertions {
				operand, sourceType, commaOK := "i", "I", tc.name == "assert_impossible"
				if tc.name == "assert_identity" {
					sourceType = "Shower"
					commaOK = i == 1
					if i == 2 {
						operand = "pi"
					}
				}
				expr, err := e.checkedAssertion(node, operand, sourceType, tc.name == "assert_impossible", commaOK)
				if err != nil {
					t.Fatal(err)
				}
				emitted = append(emitted, expr)
			}
			if tc.name == "assert_identity" {
				body = fmt.Sprintf(`box:=Box{N:1}; var i Shower=box; box.N=9; stored:=%s; fmt.Printf("copy:%%d\n",stored.N); zero,ok:=%s; fmt.Printf("zero:%%d:%%t\n",zero,ok); p:=&box; var pi Shower=p; q:=%s; q.N=12; fmt.Printf("pointer:%%d\n",box.N)`, emitted[0], emitted[1], emitted[2])
			} else {
				body = fmt.Sprintf(body, emitted[0])
			}
		}
		if tc.name != "assert_identity" {
			body += `;fmt.Println("unreachable")`
		}
		source := `package main
import("fmt";"os";cv_rt "github.com/bashsharp/bashsharp/engine/lower/shellrt")
` + tc.header + `
func main(){ defer func(){if r:=recover();r!=nil {if e,ok:=cv_rt.AsValueError(r);ok {fmt.Fprintln(os.Stderr,e);os.Exit(e.ExitStatus())};panic(r)}}();` + body + "}\n"
		packageDir := filepath.Join(dir, tc.name)
		if err := os.Mkdir(packageDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(packageDir, "main.go"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// An extra artifact checks the emitter's side-effect order and lazy RHS.
	e := &emitter{prefix: "cv_"}
	index := e.checkedIndexRead(nil, "sequence()", "index()", "int", "xs")
	deref := e.checkedDeref(nil, "pointer()", "p")
	arrayRead := e.checkedIndexRead(nil, "&array", "mutate()", "int", "array")
	allocation := e.checkedMakeSlice(nil, "int", "size()", "")
	orderSource := `package main
import("fmt";cv_rt "github.com/bashsharp/bashsharp/engine/lower/shellrt")
var log string
var array = [1]int{1}
func mutate() int {array[0]=9;return 0}
func size() int {log+="m";return 2}
func sequence() []int {log+="s";return []int{7}}
func index() int {log+="i";return 0}
func pointer() *int {log+="p"; n:=8;return &n}
func main(){n:=` + index + `; if false && ` + deref + `>0 {panic("not lazy")}; p:=` + deref + `;a:=` + arrayRead + `;made:=` + allocation + `;fmt.Println(n,p,log,a,len(made),cap(made))}
`
	if err := os.Mkdir(filepath.Join(dir, "order"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "order", "main.go"), []byte(orderSource), 0600); err != nil {
		t.Fatal(err)
	}
	binaries := filepath.Join(dir, "bin")
	if err := os.Mkdir(binaries, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-mod=mod", "-o", binaries+string(os.PathSeparator), "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range cases {
		if err := os.RemoveAll(filepath.Join(dir, tc.name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, "order")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.CommandContext(ctx, filepath.Join(binaries, tc.name))
			command.Dir = runDir
			command.Env = []string{"PATH=/no-tools"}
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			artifactStatus := checkedTestStatus(command.Run())
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.source), tc.path)
			if err != nil {
				t.Fatal(err)
			}
			var interpreted, diagnostic bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &interpreted, &diagnostic), interp.Env(expand.ListEnviron("PATH=/no-tools")))
			if err != nil {
				t.Fatal(err)
			}
			interpretedStatus := checkedTestStatus(runner.Run(ctx, file))
			if artifactStatus != interpretedStatus || stdout.String() != interpreted.String() || stderr.String() != diagnostic.String() {
				t.Fatalf("artifact=(%d,%q,%q) interpreted=(%d,%q,%q)", artifactStatus, stdout.String(), stderr.String(), interpretedStatus, interpreted.String(), diagnostic.String())
			}
		})
	}
	order := exec.CommandContext(ctx, filepath.Join(binaries, "order"))
	order.Dir = runDir
	order.Env = []string{"PATH=/no-tools"}
	if out, err := order.CombinedOutput(); err != nil || string(out) != "7 8 sipm 9 2 2\n" {
		t.Fatalf("order/laziness: %v %q", err, out)
	}
}
func checkedTestStatus(err error) int {
	if err == nil {
		return 0
	}
	if status, ok := err.(interface{ ExitStatus() int }); ok {
		return status.ExitStatus()
	}
	if status, ok := err.(*exec.ExitError); ok {
		return status.ExitCode()
	}
	if status, ok := interp.IsExitStatus(err); ok {
		return int(status)
	}
	return -1
}

const checkedNilSource = "func main() {\n var p *int\n x := *p\n}\nmain()\n"

const checkedAssertFailSource = "type T int\nfunc (v T) M(s string) { }\ntype U int\nfunc (v U) M(s string) { }\ntype I interface { M(string) }\nfunc main() { var v T = 1; var i I = v; x := i.(U); echo $x }\nmain()\n"

const checkedAssertImpossibleSource = "type T int\nfunc (v T) M(s string) { }\ntype U int\ntype I interface { M(string) }\nfunc main() { var v T = 1; var i I = v; x, ok := i.(U); echo $x $ok }\nmain()\n"

const checkedAssertIdentitySource = "type Box struct { N int }\nfunc (v Box) Show(prefix string) { echo \"$prefix:${v.N}\"; }\ntype Shower interface { Show(string) }\ntype Other int\nfunc (v Other) Show(prefix string) { echo \"$prefix:$v\"; }\nfunc main() {\n\tvar box Box = Box{N: 1}\n\tvar i Shower = box\n\tbox.N = 9\n\tstored := i.(Box)\n\tprintf 'copy:%s\\n' stored.N\n\tzero, ok := i.(Other)\n\techo \"zero:$zero:$ok\"\n\tp := &box\n\tvar pi Shower = p\n\tq := pi.(*Box)\n\tq.N = 12\n\tprintf 'pointer:%s\\n' box.N\n}\nmain()\n"
