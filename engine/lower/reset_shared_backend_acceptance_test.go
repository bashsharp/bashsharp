//go:build full

package lower_test

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// These two programs are unchanged from
// interp/bashpp_scope_test.go:TestBashPPFunctionCaptureSurvivesReset.
// The host retains ONE production shell backend across two compiled entries.
// Each entry owns a lease whose Close releases that lease; only the host owns
// final backend shutdown. Reset is called between completed entries, on the
// very Runner captured through the production backend's public options.
func TestCompiledCaptureAcrossSharedBackendReset(t *testing.T) {
	sources := []string{"var x = 1\nkeep() { echo \"keep=$x\"; }\nkeep\n", "keep\nvar x = 9\nkeep\necho \"now=$x\"\n"}
	const want = "keep=1\nkeep=1\nkeep=1\nnow=9\n"
	var out, diagnostic bytes.Buffer
	oracle, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &diagnostic))
	if err != nil {
		t.Fatal(err)
	}
	for i, source := range sources {
		if i != 0 {
			oracle.Reset()
		}
		if err := oracle.Run(t.Context(), parse(t, source, fmt.Sprintf("part%d.bpp", i))); err != nil {
			t.Fatal(err)
		}
	}
	if out.String() != want || diagnostic.Len() != 0 {
		t.Fatalf("public oracle out=%q stderr=%q", out.String(), diagnostic.String())
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	module := t.TempDir()
	writeAgenticFile(t, filepath.Join(module, "go.mod"), "module resetacceptance\n\ngo "+strings.TrimPrefix(runtime.Version(), "go")+"\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => "+filepath.ToSlash(root)+"\n")
	var paths []string
	for i, source := range sources {
		name := fmt.Sprintf("part%d", i)
		result, err := lower.Compile(parse(t, source, name+".bpp"), lower.Options{Package: name, Entry: "Execute", Origin: name + ".bpp"})
		if err != nil {
			t.Fatal(err)
		}
		// Retain emission in verbose test evidence for native-storage review. The
		// host below independently rejects every typed node sent to its backend.
		goFile, err := goparser.ParseFile(token.NewFileSet(), name+".go", result.Source, 0)
		if err != nil {
			t.Fatal(err)
		}
		nativeCell := false
		ast.Inspect(goFile, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			index, ok := call.Fun.(*ast.IndexExpr)
			if !ok {
				return true
			}
			selector, ok := index.X.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Cell" {
				return true
			}
			typ, ok := index.Index.(*ast.Ident)
			if ok && typ.Name == "int" {
				nativeCell = true
			}
			return true
		})
		if !nativeCell {
			t.Fatal("typed x has no native int storage")
		}
		t.Logf("%s generated native unit:\n%s", name, result.Source)
		path := filepath.Join(module, name, "program.go")
		writeAgenticFile(t, path, string(result.Source))
		paths = append(paths, path)
	}
	host := filepath.Join(module, "host", "reset_test.go")
	writeAgenticFile(t, host, resetSharedBackendHost)
	paths = append(paths, host)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	binary := filepath.Join(module, "reset.test")
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "-mod=mod", "-race", "-c", "-o", binary, "./host")
	build.Dir = module
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build host: %v\n%s", err, output)
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	execDir := t.TempDir()
	assertNoSource(t, execDir)
	run := exec.CommandContext(ctx, binary, "-test.v", "-test.timeout=60s")
	run.Dir = execDir
	run.Env = []string{"PATH=/no-tools", "GORACE=halt_on_error=1"}
	output, err := run.CombinedOutput()
	t.Logf("shared-backend artifact:\n%s", output)
	if err != nil {
		t.Fatalf("shared-backend Reset: %v", err)
	}
}

const resetSharedBackendHost = `package host_test
import("bytes";"context";"fmt";"strings";"testing";"github.com/bashsharp/bashsharp/engine/interp";rt "github.com/bashsharp/bashsharp/engine/lower/shellrt";"github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec";"github.com/bashsharp/bashsharp/engine/syntax";"resetacceptance/part0";"resetacceptance/part1")
type owner struct {backend rt.ShellRunner; runner *interp.Runner; leases, releases, regions int}
type lease struct {owner *owner; closed bool}
func(l *lease) RunShell(ctx context.Context,st *rt.State,io rt.Stdio,src string)error{
 if l.closed{return fmt.Errorf("closed entry lease reused")}
 f,e:=syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src),"region");if e!=nil{return e}
 var typed syntax.Node
 syntax.Walk(f,func(n syntax.Node)bool{if n!=nil&&strings.Contains(fmt.Sprintf("%T",n),"BashPP"){typed=n;return false};return true})
 if typed!=nil{return fmt.Errorf("typed native input reached backend: %T",typed)}
 l.owner.regions++
 return l.owner.backend.RunShell(ctx,st,io,src)
}
func(l *lease) Clone(io rt.Stdio)(rt.ShellRunner,error){return nil,fmt.Errorf("fixture has no subshell or task")}
func(l *lease) Close(context.Context)error{if !l.closed{l.closed=true;l.owner.releases++};return nil}
func TestSharedBackendReset(t *testing.T){
 var out,diagnostic bytes.Buffer;o:=&owner{}
 t.Cleanup(func(){if o.backend!=nil{if err:=o.backend.Close(context.Background());err!=nil{t.Errorf("host close: %v",err)}}})
 factory:=func(st rt.State,io rt.Stdio)(rt.ShellRunner,error){
  if o.backend==nil{var err error;o.backend,err=shellexec.NewRunner(st,io,shellexec.BashPP(),shellexec.RunnerOptions(func(r *interp.Runner)error{o.runner=r;return nil}));if err!=nil{return nil,err}}
  o.leases++;return &lease{owner:o},nil
 }
 options:=[]rt.SessionOption{rt.WithStdio(nil,&out,&diagnostic),rt.WithShellFactory(factory),rt.WithNativeContinuation(rt.NewNativeContinuation())}
 code,err:=part0.Execute(options...);if code!=0||err!=nil{t.Fatalf("part0: code=%d err=%v stderr=%q",code,err,diagnostic.String())}
 if o.runner==nil||o.leases!=1||o.releases!=1{t.Fatalf("first lease lifecycle %+v",o)}
 keep:=o.runner.Funcs["keep"];t.Logf("backend shell keep present=%t (native registry may own it)",keep!=nil)
 original:=o.runner;o.runner.Reset()
 if o.runner!=original||o.runner.Funcs["keep"]!=keep{t.Fatal("Reset replaced backend or lost surviving function")}
 code,err=part1.Execute(options...)
 t.Logf("part1 status=%d error=%v; backend instances=1 leases=%d releases=%d",code,err,o.leases,o.releases)
 if code!=0||err!=nil{t.Errorf("part1: code=%d err=%v",code,err)}
 if o.leases!=2||o.releases!=2{t.Errorf("missing shared lifecycle %+v",o)}
 if got:=out.String();got!="keep=1\nkeep=1\nkeep=1\nnow=9\n"{t.Errorf("capture output=%q",got)}
 if diagnostic.Len()!=0{t.Errorf("stderr=%q",diagnostic.String())}
}
`
