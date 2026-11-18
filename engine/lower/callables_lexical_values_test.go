//go:build full

package lower

import (
	"bytes"
	"context"
	"errors"
	"go/parser"
	"go/token"
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

// This applies the proposed postpass to actual Compile output until its owner
// installs the one-line dispatcher hook. It then builds and removes source.
func TestLexicalValuesCompiledArtifact(t *testing.T) {
	cases := map[string]string{
		"compound_equal":   `func main() { var x int = 1; x=010; x += 0; echo "$x" }; main()`,
		"compound_pointer": `func main() { var x int8 = 1; p := &x; x=128; *p /= 2; echo "$x" }; main()`,
		"tuple_equal": `func pair() (int, int) { return 1, 2 }
func main() { var x int = 1; var y int = 2; x=01; y=02; p := &x; x, y = pair(); echo "$x:$y"; value := *p; println(value) }; main()`,
		"tuple_failure_raw": `func pair() (int, bool) { return 7, true }
func main() { var x int = 1; var y int = 2; x=01; y=02; x, y = pair(); echo "$x:$y" }; main()`,
		"readonly_shell_write": `func main() { var x int = 1; readonly x; x=42; echo "$x" }; main()`,
		"exact_float":          `func main() { var x float64 = 1; x=0.1; y := x + 0.2; println(y); echo "$y" }; main()`,
		"lazy_invalid":         `func main() { var x int = 1; x=abc; var enabled bool = false; accepted := enabled && x > 0; println(accepted); echo after }; main()`,
		"named_invalid": `type Amount int
func main() { var x Amount = 1; x=abc; println(x); y := x + 1; println(y); echo after }; main()`,
		"invalid_float":        `func main() { var x float64 = 1; x=abc; println(x); y := x + 0.5; println(y); echo after }; main()`,
		"odd_division":         `func main() { var x int = 1; x=5; y := x / 2; println(y) }; main()`,
		"pointer_alias":        `func main() { var x int8 = 1; p := &x; x=128; y := *p / 2; println(y) }; main()`,
		"pointer_native_write": `func main() { var x int = 1; p := &x; x=01; echo "$x"; *p = 1; echo "$x" }; main()`,
		"canonical":            `func main() { var x int = 1; x=42; println(x); y := x + 1; println(y) }; main()`,
		"octal":                `func main() { var x int = 1; x=010; println(x); y := x + 1; println(y) }; main()`,
		"invalid_continues":    `func main() { var x int = 99; x=abc; println(x); y := x + 1; println(y); echo after }; main()`,
		"wide_division":        `func main() { var x int8 = 1; x=128; println(x); y := x / 2; println(y); println(x > 0) }; main()`,
		"float":                `func main() { var x float64 = 1; x=2.500; println(x); y := x + 0.5; println(y); echo "$y" }; main()`,
		"boolean":              `func main() { var x bool = true; x=arbitrary; println(x); println(!x) }; main()`,
		"equal_write":          `func main() { var x int = 1; x=01; echo "$x"; x = 1; echo "$x" }; main()`,
	}
	for name, source := range cases {
		source = strings.ReplaceAll(source, "; ", "\n")
		source = strings.ReplaceAll(source, "{ var", "{\nvar")
		source = strings.ReplaceAll(source, " };", "\n};")
		source = strings.ReplaceAll(source, "println(x > 0)", "positive := x > 0\nprintln(positive)")
		source = strings.ReplaceAll(source, "println(!x)", "negated := !x\nprintln(negated)")
		t.Run(name, func(t *testing.T) {
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "lexical.bpp")
			if err != nil {
				t.Fatal(err)
			}
			result, err := Compile(file, Options{Entry: "Execute"})
			if err != nil {
				t.Fatal(err)
			}
			fs := token.NewFileSet()
			goFile, err := parser.ParseFile(fs, "generated.go", result.Source, 0)
			if err != nil {
				t.Fatal(err)
			}
			prefix := ""
			for _, spec := range goFile.Imports {
				if spec.Path.Value == `"`+DefaultRuntime+`"` {
					prefix = strings.TrimSuffix(spec.Name.Name, "rt")
				}
			}
			if prefix == "" {
				t.Fatal("fixture did not compile through lexical runtime")
			}
			e := &emitter{prefix: prefix, options: Options{Runtime: DefaultRuntime, Origin: result.Origin}}
			generated, err := e.lexicalValues(result.Source)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			writeEntryModule(t, dir)
			path := filepath.Join(dir, "program.go")
			writeEntryFile(t, path, string(generated))
			binary := filepath.Join(dir, "program")
			runEntryCommand(t, dir, "go", "build", "-mod=mod", "-o", binary, ".")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary)
			command.Dir = t.TempDir()
			command.Env = []string{"PATH=/no-tools"}
			var out, diagnostic bytes.Buffer
			command.Stdout = &out
			command.Stderr = &diagnostic
			status := 0
			if err := command.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				status = exit.ExitCode()
			}
			var oracleOut, oracleErr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &oracleOut, &oracleErr), interp.Env(expand.ListEnviron("PATH=/no-tools")), interp.Dir(command.Dir))
			if err != nil {
				t.Fatal(err)
			}
			oracleStatus := 0
			if err := runner.Run(ctx, file); err != nil {
				var exit interp.ExitStatus
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				oracleStatus = int(exit)
			}
			if status != oracleStatus || out.String() != oracleOut.String() || diagnostic.String() != oracleErr.String() {
				t.Fatalf("artifact status=%d stdout=%q stderr=%q; interpreter status=%d stdout=%q stderr=%q\n%s", status, out.String(), diagnostic.String(), oracleStatus, oracleOut.String(), oracleErr.String(), generated)
			}
		})
	}
}

func TestLexicalValuesConcurrentEntryArtifact(t *testing.T) {
	source := `var count int = 1
func adjust() {
 eval 'count=010'
 println(count)
 count = 1
 echo "$count"
}
adjust()
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "entries.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var oracleOut, oracleErr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &oracleOut, &oracleErr), interp.Env(expand.ListEnviron("PATH=/no-tools")))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil || oracleOut.String() != "8\n1\n" || oracleErr.Len() != 0 {
		t.Fatalf("entry source oracle: %v/%q/%q", err, oracleOut.String(), oracleErr.String())
	}
	result, err := Compile(file, Options{Package: "generated", Entry: "Execute"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), "generated.go", result.Source, 0)
	if err != nil {
		t.Fatal(err)
	}
	prefix := ""
	for _, spec := range parsed.Imports {
		if spec.Path.Value == `"`+DefaultRuntime+`"` {
			prefix = strings.TrimSuffix(spec.Name.Name, "rt")
		}
	}
	e := &emitter{prefix: prefix, options: Options{Runtime: DefaultRuntime, Origin: "entries.bpp"}}
	generated, err := e.lexicalValues(result.Source)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeEntryModule(t, dir)
	unit := filepath.Join(dir, "generated", "program.go")
	host := filepath.Join(dir, "host", "entries_test.go")
	writeEntryFile(t, unit, string(generated))
	writeEntryFile(t, host, `package host_test
import("bytes";"sync";"testing";"entryartifact/generated";rt "github.com/bashsharp/bashsharp/engine/lower/shellrt")
func TestEntries(t *testing.T){var group sync.WaitGroup;for range 16{group.Add(1);go func(){defer group.Done();var out,diagnostic bytes.Buffer;status,err:=generated.Execute(rt.WithStdio(nil,&out,&diagnostic));if status!=0||err!=nil||out.String()!="8\n1\n"||diagnostic.Len()!=0{t.Errorf("%d/%v/%q/%q",status,err,out.String(),diagnostic.String())}}()};group.Wait()}
`)
	binary := filepath.Join(dir, "entries.test")
	runEntryCommand(t, dir, "go", "test", "-mod=mod", "-race", "-c", "-o", binary, "./host")
	for _, path := range []string{unit, host} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(binary, "-test.timeout=15s")
	command.Dir = t.TempDir()
	command.Env = []string{"PATH=/no-tools", "GORACE=halt_on_error=1"}
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("concurrent artifact: %v\n%s", err, out)
	}
}
