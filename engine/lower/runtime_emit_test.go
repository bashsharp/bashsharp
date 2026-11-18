//go:build full

package lower

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestChannelEmitterArtifactParity(t *testing.T) {
	fixtures := []string{
		`func main() {
 ch := make(chan string, 1)
 ch <- "ready"
 close(ch)
 first, open := <-ch
 println(first, open)
 select {
 case second, still := <-ch:
  println(second, still)
 }
 empty := make(chan int)
 select {
 case <-empty:
  println("impossible")
 default:
  println("default")
 }
}
main()
`,
		`func worker(ch) {
 ch <- 1
 ch <- 2
 close(ch)
}
func main() {
 ch := make(chan int)
 go worker(ch)
 for v := range ch {
  println(v)
 }
}
main()
`,
	}
	for index, input := range fixtures {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(input), "input.bpp")
			if err != nil {
				t.Fatal(err)
			}
			e := &emitter{prefix: "__test_", scopes: []map[string]bool{{}}, funcs: map[string]bool{"worker": true}}
			contextNames := RuntimeContext{Context: "ctx", Session: "session", Channels: "channels"}
			var emit runtimeBody
			emit = func(stmts []*syntax.Stmt, c RuntimeContext) (string, error) {
				var out strings.Builder
				for _, stmt := range stmts {
					var text string
					var err error
					switch node := stmt.Cmd.(type) {
					case *syntax.BashPPShortDecl:
						if node.MakeChan != nil {
							text, err = e.runtimeMakeChannel(node.MakeChan, c)
						} else if node.Recv != nil {
							text, err = e.runtimeReceive(node.Recv, c, len(node.Lhs) == 2)
						} else {
							return "", fmt.Errorf("unhandled test short form")
						}
						text = strings.Join(names(node.Lhs), ", ") + " := " + text
						for _, name := range node.Lhs {
							e.bind(name.Value)
						}
					case *syntax.BashPPSend:
						text, err = e.runtimeSend(node, c)
					case *syntax.BashPPClose:
						text, err = e.runtimeClose(node, c)
					case *syntax.BashPPSelect:
						text, err = e.runtimeSelect(node, c, emit)
					case *syntax.BashPPRange:
						text, err = e.runtimeChannelRange(node, c, emit)
					case *syntax.BashPPGo:
						text, err = e.runtimeGo(node, c, func(call *syntax.BashPPCall, child RuntimeContext) (string, string, error) {
							arg, err := e.valueWord(call.Args[0])
							return "captured := " + arg, "worker(" + child.Context + ", " + child.Session + ", " + child.Channels + ", captured)", err
						})
					default:
						text, err = e.command(stmt.Cmd)
					}
					if err != nil {
						return "", err
					}
					out.WriteString(text + "\n")
				}
				return out.String(), nil
			}
			var declarations, body string
			for _, stmt := range file.Stmts {
				fn, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
				if !ok {
					continue
				}
				e.push()
				if fn.Name.Value == "worker" {
					e.bind("ch")
				}
				text, err := emit(fn.Body.Stmts, contextNames)
				e.pop()
				if err != nil {
					t.Fatal(err)
				}
				if fn.Name.Value == "worker" {
					declarations = "func worker(ctx __test_rt.TaskContext,session *__test_rt.Session,channels *__test_rt.ChannelScope,ch chan int){\n" + text + "}\n"
				} else {
					body = text
				}
			}
			generated := `package main
import __test_rt "github.com/bashsharp/bashsharp/engine/lower/shellrt"
import __test_fmt "fmt"
` + declarations + `func main(){
session,err:=__test_rt.NewSession();if err!=nil{panic(err)};defer session.Close()
channels:= &__test_rt.ChannelScope{};defer channels.Close()
ctx:=session.Context()
` + body + "}\n"
			dir := t.TempDir()
			source := filepath.Join(dir, "generated.go")
			if err := os.WriteFile(source, []byte(generated), 0600); err != nil {
				t.Fatal(err)
			}
			_, testFile, _, _ := runtime.Caller(0)
			root := filepath.Dir(filepath.Dir(filepath.Dir(testFile)))
			module := "module channelartifact\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + root + "\n"
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			binary := filepath.Join(dir, "program")
			build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "generated.go")
			build.Dir = dir
			build.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build:%v\n%s\n%s", err, out, generated)
			}
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, binary)
			command.Dir = t.TempDir()
			command.Env = []string{"PATH=/no-tools"}
			var out, stderr bytes.Buffer
			command.Stdout = &out
			command.Stderr = &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("artifact:%v\n%s", err, stderr.String())
			}
			var interpreted, diagnostics bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &interpreted, &diagnostics), interp.Dir(command.Dir), interp.Env(expand.ListEnviron("PATH=/no-tools")))
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(ctx, file); err != nil {
				t.Fatalf("interpreter:%v\n%s", err, diagnostics.String())
			}
			if out.String() != interpreted.String() || stderr.String() != diagnostics.String() {
				t.Fatalf("compiled=(%q,%q) interpreted=(%q,%q)", out.String(), stderr.String(), interpreted.String(), diagnostics.String())
			}
		})
	}
}
func TestChannelEmitterRequiresExplicitContext(t *testing.T) {
	e := &emitter{}
	if err := e.runtimeContext(RuntimeContext{}); err == nil {
		t.Fatal("missing context accepted")
	}
}
