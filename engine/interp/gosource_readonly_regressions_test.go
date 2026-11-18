//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceNativeReadOnlyRegressionsThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"sha256_write": `package main
import("fmt";"crypto/sha256")
func main(){h:=sha256.New();b:=[]byte("hello");n,e:=h.Write(b);b[0]='H';fmt.Printf("%d %v %s %x\n",n,e,b,h.Sum(nil))}`,
		"template_primitive_slice": `package main
import("os";"text/template")
func main(){t:=template.Must(template.New("x").Parse("Range: {{range .}}{{.}} {{end}}\n"));t.Execute(os.Stdout,[]string{"Go","Rust"})}`,
	} {
		t.Run(name, func(t *testing.T) { callbackTourThreeModes(t, source) })
	}
}
func TestGoSourceNativeTemplateFunctionBoundary(t *testing.T) {
	// Template invocations are refused even when this particular nested tree
	// is harmless; an uninspected associated template must not evade the guard.
	source := `package main
import("fmt";"os";"text/template")
func main(){t:=template.Must(template.New("x").Parse("{{define \"nested\"}}{{range .}}{{.}}{{end}}{{end}}{{template \"nested\" .}}\n"));t.Execute(os.Stdout,[]string{"a","b"});fmt.Println("after")}`
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	oracle := runNativeOracle(t, dir, path, nil, "")
	if oracle.status != 0 || oracle.stdout != "ab\nafter\n" {
		t.Fatalf("oracle: %+v", oracle)
	}
	p, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), p.File)
	if err == nil || !strings.Contains(err.Error()+out.String(), "template with function or template callbacks") {
		t.Fatalf("missing template boundary: %v %q", err, out.String())
	}
	if strings.Contains(out.String(), "ab\n") || strings.Contains(out.String(), "after") {
		t.Fatalf("unsafe template ran: %q", out.String())
	}
}

func TestGoSourceNativeTemplateMutatorBoundary(t *testing.T) {
	dir := callbackTourModule(t)
	if err := os.Mkdir(filepath.Join(dir, "mutator"), 0700); err != nil {
		t.Fatal(err)
	}
	dependency := `package mutator
import("fmt";"text/template")
func mutate(data []string)string{data[0]="changed";fmt.Println("dependency-mutated");return data[0]}
func New()*template.Template{return template.Must(template.New("x").Funcs(template.FuncMap{"mut":mutate}).Parse("{{mut .}}\n"))}`
	if err := os.WriteFile(filepath.Join(dir, "mutator", "mutate.go"), []byte(dependency), 0600); err != nil {
		t.Fatal(err)
	}
	source := `package main
import("fmt";"os";"example.com/callback-test/mutator")
func main(){t:=mutator.New();data:=[]string{"original"};t.Execute(os.Stdout,data);fmt.Println(data,"after")}`
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "oracle")
	cmd := exec.Command("go", "build", "-mod=readonly", "-p", "2", "-o", binary, path)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native build: %v %s", err, output)
	}
	cmd = exec.Command(binary)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "dependency-mutated\nchanged\n[changed] after\n" {
		t.Fatalf("native effect: %v %q", err, output)
	}
	p := callbackTourLoad(t, dir, source)
	var out bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), p.File)
	if err == nil || !strings.Contains(err.Error()+out.String(), "template with function or template callbacks") {
		t.Fatalf("missing mutator boundary: %v %q", err, out.String())
	}
	if strings.Contains(out.String(), "dependency-mutated") || strings.Contains(out.String(), "after") {
		t.Fatalf("mutator executed: %q", out.String())
	}
}
