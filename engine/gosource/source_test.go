//go:build full

package gosource_test

import (
	"bytes"
	"context"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUnchangedGo(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"native_method_shortdecl", "package main\nimport \"fmt\"\nimport \"time\"\nfunc main(){value:=time.Date(2020,time.January,2,0,0,0,0,time.UTC);year:=value.Year();fmt.Println(year)}", "2020\n"},
		{"constant_defaults", "package main\nimport \"fmt\"\nconst(i=1;f=1.0;r='a';h=0x1p0)\nfunc main(){fmt.Printf(\"%T %T %T %T %T\\n\",i,f,r,h,i+f)}", "int float64 int32 float64 float64\n"},
		{"constant_huge", "package main\nimport \"fmt\"\nconst huge=1e1000\nfunc main(){fmt.Printf(\"%T %v\\n\",huge-huge,huge-huge)}", "float64 0\n"},
		{"constant_fraction", "package main\nimport \"fmt\"\nconst third=1.0/3.0\nconst n=9007199254740993.0\nfunc main(){fmt.Println(third*3==1,n-9007199254740992.0)}", "true 1\n"},
		{"hello", "// A Go program.\npackage main\nimport \"fmt\"\nfunc main(){ fmt.Println(\"Hello, 世界\") }", "Hello, 世界\n"},
		{"literals", "package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"$HOME // | ` x\", `raw $HOME // |`, '世', 6 & 3, 1 << 3)}", "$HOME // | ` x raw $HOME // | 19990 2 8\n"},
		{"constants", "package main\nimport \"fmt\"\nconst Pi = 3.14\nfunc main(){const World=\"世界\";fmt.Println(\"Hello\",World,Pi);fmt.Printf(\"%T\\n\",'世')}", "Hello 世界 3.14\nint32\n"},
		{"tuple_strings", "package main\nimport \"fmt\"\nfunc swap(a,b string)(string,string){return b,a}\nfunc main(){a,b:=swap(\"hello\",\"world\");fmt.Println(a,b)}", "world hello\n"},
		{"for_clause", "package main\nimport \"fmt\"\nfunc main(){s:=0;for i:=0;i<4;i++{s+=i};fmt.Println(s)}", "6\n"},
		{"shadow_nil", "package main\nimport \"fmt\"\nfunc main(){nil:=7;fmt.Println(nil)}", "7\n"},
		{"function", "package main\nimport \"fmt\"\nfunc add(x, y int)int{return x+y}\nfunc main(){fmt.Println(add(2,3))}", "5\n"},
		{"generic_receiver", "package main\nimport \"fmt\"\ntype Pair[A,B any] struct{first A;second B;next *Pair[A,B]}\nfunc(p Pair[A,B]) First()A{return p.first}\nfunc(p *Pair[A,B]) Seconds()[]B{var out []B;for q:=p.next;q!=nil;q=q.next{out=append(out,q.second)};return out}\nfunc main(){p:=Pair[int,string]{first:7,next:&Pair[int,string]{second:\"bound\"}};fmt.Printf(\"%T %v %T %v\\n\",p.First(),p.First(),p.Seconds()[0],p.Seconds()[0])}", "int 7 string bound\n"},
		{"zero", "package main\nimport \"fmt\"\nvar a int\nvar b bool\nvar c string\nfunc main(){fmt.Printf(\"%d %t %q\\n\",a,b,c)}", "0 false \"\"\n"},
		{"hygiene", "package main\nimport \"fmt\"\nvar __gosource_import_0_0=7\nfunc __gosource_init_0(){}\nfunc init(){}\nfunc main(){fmt.Println(__gosource_import_0_0)}", "7\n"},
		{"init", "package main\nimport \"fmt\"\nvar a = f()\nvar b = 3\nfunc f() int {return b+1}\nfunc init(){fmt.Println(a,b)}\nfunc init(){fmt.Println(\"init2\")}\nfunc main(){fmt.Println(\"main\")}", "4 3\ninit2\nmain\n"},
		{"examples_constants", "package main\nimport(\"fmt\"\n\"math\")\nconst s string=\"constant\"\nfunc main(){\nfmt.Println(s)\nconst n=500000000\nconst d=3e20/n\nfmt.Println(d)\nfmt.Println(int64(d))\nfmt.Println(math.Sin(n))\n}", "constant\n6e+11\n600000000000\n-0.28470407323754404\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(tc.src), tc.name+".go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			err = r.Run(context.Background(), p.File)
			if err != nil || out.String() != tc.want || stderr.Len() > 0 {
				t.Fatalf("run err=%v out=%q stderr=%q want=%q", err, out.String(), stderr.String(), tc.want)
			}
			result, err := lower.Compile(p.File, lower.Options{Origin: tc.name + ".go"})
			if err != nil {
				t.Fatalf("lower: %v", err)
			}
			dir := t.TempDir()
			source, artifact := filepath.Join(dir, "generated.go"), filepath.Join(dir, "program")
			if err := os.WriteFile(source, result.Source, 0600); err != nil {
				t.Fatal(err)
			}
			build := exec.Command("go", "build", "-p", "2", "-o", artifact, source)
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s\n%s", err, output, result.Source)
			}
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			run := exec.Command(artifact)
			run.Env = []string{"PATH="}
			output, err := run.CombinedOutput()
			if err != nil || string(output) != tc.want {
				t.Fatalf("artifact: %v out=%q want=%q", err, output, tc.want)
			}
		})
	}
}
func TestGoDiagnostics(t *testing.T) {
	for _, src := range []string{"package main\nfunc main(){ echo hi }", "package main\nfunc main(){ println(null) }", "package main\nfunc main(){ var x int = nil; println(x) }"} {
		_, err := gosource.Parse(strings.NewReader(src), "bad.go", gosource.Options{RunMain: true})
		if err == nil || !strings.Contains(err.Error(), "bad.go:2:") {
			t.Fatalf("missing original diagnostic: %v", err)
		}
	}
}

func TestMultipleFilesAndCheckOnly(t *testing.T) {
	sources := []gosource.Source{
		{Name: "b.go", Data: []byte("package main\nimport f \"fmt\"\nvar b = 4\nfunc main(){f.Println(a,b)}")},
		{Name: "a.go", Data: []byte("package main\nimport f \"strings\"\nvar a = f.ToUpper(\"go\")\nfunc helper() int{return b}")},
	}
	// Type checking must preserve file-local import bindings; neither alias may
	// overwrite the other's imported package identity.
	p, err := gosource.Load(sources, gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Package != "main" || p.Main != "main" || len(p.Sources) != 2 {
		t.Fatalf("metadata: %+v", p)
	}
	for _, stmt := range p.File.Stmts {
		if _, ok := stmt.Cmd.(*syntax.BashPPCall); ok {
			t.Fatal("check-only inserted execution call")
		}
	}
	for _, si := range p.Sources {
		name, offset, ok := p.SourceAt(syntax.NewPos(si.Base+10, 2, 1))
		if !ok || name != si.Name || offset != 10 {
			t.Fatalf("source position: %q %d %v", name, offset, ok)
		}
	}
}

func TestModuleImporter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/source\n\ngo 1.26.5\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "helper"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helper", "helper.go"), []byte("package helper\nfunc Value() int{return 42}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	src := "package main\nimport h \"example.com/source/helper\"\nfunc main(){println(h.Value())}\n"
	if _, err := gosource.Parse(strings.NewReader(src), filepath.Join(dir, "main.go"), gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)}); err != nil {
		t.Fatal(err)
	}
}

func TestGoSourceComplexRuntime(t *testing.T) {
	src := "package main\nvar z complex128 = 1i\nfunc main(){println(\"complex ready\")}"
	p, err := gosource.Parse(strings.NewReader(src), "unsupported.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(context.Background(), p.File); err != nil {
		t.Fatalf("complex runtime: %v %s", err, stderr.String())
	}
	if out.Len() != 0 || stderr.String() != "complex ready\n" {
		t.Fatalf("complex print output: %q %q", out.String(), stderr.String())
	}
}

func TestPrintAndReset(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"1", "2"} {
		p, err := gosource.Parse(strings.NewReader("package main\nvar x="+n+"\nfunc main(){print(\"a\",\"b\");println(x)}"), "main.go", gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Run(context.Background(), p.File); err != nil {
			t.Fatal(err)
		}
		if stdout.Len() != 0 || stderr.String() != "ab"+n+"\n" {
			t.Fatalf("streams stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		stdout.Reset()
		stderr.Reset()
		r.Reset()
	}
}

func TestMultipleFileRuntimeAndLowerPositions(t *testing.T) {
	p, err := gosource.Load([]gosource.Source{{Name: "a.go", Data: []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(f())}")}, {Name: "b.go", Data: []byte("package main\nvar zero int\nfunc f()int{return 1/zero}")}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	// A run-time fault is Go's panic report since Sprint 162: the goroutine
	// trace names the interpreted frame's file:line (`b.go:3`), as gc does,
	// rather than a positioned file:line:col diagnostic.
	if err = r.Run(context.Background(), p.File); err == nil || !strings.Contains(err.Error()+stderr.String(), "b.go:3") {
		t.Fatalf("runtime position: %v stderr=%q", err, stderr.String())
	}
	result, err := lower.Compile(p.File, lower.Options{Origin: "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("lost sources: %+v", result.Sources)
	}
	found := false
	for _, m := range result.Mappings {
		if m.Source == "b.go" {
			found = true
			if m.SourceOffset >= uint(len("package main\nvar zero int\nfunc f()int{return 1/zero}")) {
				t.Fatalf("bad source offset %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("lowering lost b.go identity")
	}
}

func TestSourceMetadataAndComputedCallJSON(t *testing.T) {
	p, err := gosource.Parse(strings.NewReader("package main\nimport \"time\"\nfunc main(){println(time.Now().Year())}"), "computed.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := typedjson.Encode(&buf, p.File); err != nil {
		t.Fatal(err)
	}
	node, err := typedjson.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	file := node.(*syntax.File)
	if !file.GoSource || len(file.Sources) != 1 || !reflect.DeepEqual(file.Sources[0], p.Sources[0]) {
		t.Fatalf("source metadata lost: %+v", file)
	}
	found := false
	syntax.Walk(file, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.BashPPCall); ok && call.CalleeExpr != nil {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("computed callee lost")
	}
	if _, err := lower.Compile(file, lower.Options{Origin: "computed.go"}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeGoConcurrency(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"range", `package main
import "fmt"
func main(){xs:=[]int{2,3,5};ch:=make(chan int);go func(){s:=0;for _,v:=range xs{s+=v};ch<-s;close(ch)}();for v:=range ch{fmt.Println(v)}}`, "10\n"},
		{"select", `package main
import "fmt"
func main(){ch:=make(chan int,1);ch<-4;select{case n:=<-ch:fmt.Println(n);default:fmt.Println("wrong")}}`, "4\n"},
		{"defer", `package main
import "fmt"
func value()(v int){defer func(){v++}();return 4}
func main(){fmt.Println(value())}`, "5\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(tc.src), tc.name+".go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := lower.Compile(p.File, lower.Options{Origin: tc.name + ".go"})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(result.Source), "lower/shellrt") {
				t.Fatal("ordinary Go acquired shell runtime")
			}
			dir := t.TempDir()
			src, bin := filepath.Join(dir, "out.go"), filepath.Join(dir, "program")
			if err := os.WriteFile(src, result.Source, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "build", "-p", "2", "-o", bin, src)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build %v: %s\n%s", err, out, result.Source)
			}
			if err := os.Remove(src); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd = exec.CommandContext(ctx, bin)
			cmd.Env = []string{"PATH="}
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != tc.want {
				t.Fatalf("native err=%v output=%q want=%q", err, out, tc.want)
			}
		})
	}
}

func TestGoPackageFunctionLiteral(t *testing.T) {
	src := `package main
var f=func()int{return x}
var x=7
func main(){y:=f();println(y)}`
	p, err := gosource.Parse(strings.NewReader(src), "closure.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(context.Background(), p.File); err != nil || out.Len() != 0 || stderr.String() != "7\n" {
		t.Fatalf("closure: err=%v out=%q stderr=%q", err, out.String(), stderr.String())
	}
	if _, err := lower.Compile(p.File, lower.Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestGoBufferedChannel(t *testing.T) {
	p, err := gosource.Parse(strings.NewReader("package main\nfunc main(){c:=make(chan int,1);c<-4;x:=<-c;println(x)}"), "channel.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = r.Run(ctx, p.File); err != nil || out.Len() != 0 || stderr.String() != "4\n" {
		t.Fatalf("channel: err=%v out=%q stderr=%q", err, out.String(), stderr.String())
	}
}

func TestAllGoDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		needles      []string
	}{
		{"types", "package main\nfunc main(){\n println(missingOne)\n println(missingTwo)\n}\n", []string{"types.go:3:", "undefined: missingOne", "types.go:4:", "undefined: missingTwo"}},
		// A syntax error is reported with gc's parser wording (the syntax
		// verdict); gc recovers past the second bad signature without a
		// second diagnostic, so only line 2 is reported.
		{"syntax", "package main\nfunc first( {\n}\nfunc second( {\n}\n", []string{"syntax.go:2:13: syntax error: unexpected {, expected )"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gosource.Parse(strings.NewReader(tc.source), tc.name+".go", gosource.Options{})
			if err == nil {
				t.Fatal("missing diagnostics")
			}
			for _, needle := range tc.needles {
				if !strings.Contains(err.Error(), needle) {
					t.Fatalf("missing %q in %s", needle, err)
				}
			}
			if strings.Contains(err.Error(), "more errors") {
				t.Fatalf("collapsed diagnostics: %v", err)
			}
		})
	}
}

func TestMultipleFileParseDiagnostics(t *testing.T) {
	_, err := gosource.Load([]gosource.Source{
		{Name: "a.go", Data: []byte("package main\nfunc bad( {\n}")},
		{Name: "b.go", Data: []byte("package main\nfunc good(){}")},
		{Name: "c.go", Data: []byte("package main\nfunc badAgain( {\n}")},
	}, gosource.Options{})
	if err == nil || !strings.Contains(err.Error(), "a.go:2:") || !strings.Contains(err.Error(), "c.go:2:") {
		t.Fatalf("lost parse errors after a valid sibling: %v", err)
	}
}

func TestGoPrintCallArguments(t *testing.T) {
	source := `package main
import "strings"
var calls int
func local()string{calls++;return "local"}
func main(){println(strings.ToUpper("go"),local(),calls);print(strings.ToLower("END"))}
`
	p, err := gosource.Parse(strings.NewReader(source), "print.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), p.File); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || stderr.String() != "GO local 1\nend" {
		t.Fatalf("stdout=%q stderr=%q", out.String(), stderr.String())
	}
}

// Slice and generic conversion execution remains a separate interpreter task.
// This test explicitly verifies ingestion, typed serialization, and native lowering.
func TestGoConversionLowering(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"slice", "package main\nimport \"fmt\"\nfunc bytes(s string)[]byte{return []byte(s)}\nfunc main(){b:=[]byte(\"abc\");b=[]byte(\"def\");fmt.Println(string(b),string(bytes(\"xyz\")))}", "def xyz\n"},
		{"pointer", "package main\nimport \"fmt\"\nfunc main(){p:=(*int)(nil);fmt.Println(p==nil)}", "true\n"},
		{"generic", "package main\nimport \"fmt\"\ntype Slice[T any][]T\nfunc main(){fmt.Println(Slice[int]([]int{1,2}))}", "[1 2]\n"},
		{"computed_call", "package main\nimport \"fmt\"\nfunc factory()func(int)int{return func(n int)int{return n+1}}\nfunc main(){fmt.Println(factory()(2))}", "3\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := gosource.Parse(strings.NewReader(tc.src), tc.name+".go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var encoded bytes.Buffer
			if err := typedjson.Encode(&encoded, p.File); err != nil {
				t.Fatal(err)
			}
			decoded, err := typedjson.Decode(&encoded)
			if err != nil {
				t.Fatal(err)
			}
			result, err := lower.Compile(decoded.(*syntax.File), lower.Options{Origin: tc.name + ".go"})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			source := filepath.Join(dir, "generated.go")
			artifact := filepath.Join(dir, "program")
			if err := os.WriteFile(source, result.Source, 0600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command("go", "build", "-p", "2", "-o", artifact, source).CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s\n%s", err, output, result.Source)
			}
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			run := exec.Command(artifact)
			run.Env = []string{"PATH="}
			if output, err := run.CombinedOutput(); err != nil || string(output) != tc.want {
				t.Fatalf("artifact: %v output=%q want=%q", err, output, tc.want)
			}
		})
	}
}
