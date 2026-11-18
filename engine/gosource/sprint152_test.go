//go:build full

package gosource_test

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
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestSprint152Converter runs one small out-of-corpus Go program per converter
// defect localized under testdata/sprint152/<mechanism>/. Each program is valid
// Go with deterministic output, so it is executed three ways that must agree:
// go run of the original, the Bash++ interpreter over the loaded program, and
// go run of the lowered Go emitted by lower.Compile. The lower.Compile step is
// what catches the LOWER-E* diagnostics these reductions were minted from.
func TestSprint152Converter(t *testing.T) {
	for _, tc := range []struct {
		mechanism, file string
	}{
		{"blank-target-paren", "blank-target-paren/blank_target_paren.go"},
		{"new-paren", "new-paren/new_paren.go"},
		{"const-defined-type", "const-defined-type/const_defined_type.go"},
		{"decl-order", "decl-order/decl_order.go"},
		{"import-aliasing", "import-aliasing/import_aliasing.go"},
		{"shadowed-builtin-const", "shadowed-builtin-const/shadowed_builtin_const.go"},
		{"tuple-eval-order", "tuple-eval-order/tuple_eval_order.go"},
		{"untyped-constants", "untyped-constants/untyped_constants.go"},
		{"directives", "directives/directives.go"},
	} {
		t.Run(tc.mechanism, func(t *testing.T) {
			path := filepath.Join("testdata", "sprint152", filepath.FromSlash(tc.file))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Base(tc.file)
			goOut, err := exec.Command("go", "run", path).Output()
			if err != nil {
				t.Fatalf("go run: %v", err)
			}
			program, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), program.File); err != nil || stderr.Len() > 0 {
				t.Fatalf("interpreter: err=%v stderr=%q", err, stderr.String())
			}
			if !bytes.Equal(stdout.Bytes(), goOut) {
				t.Fatalf("stdout differs from go run\ninterpreter: %q\ngo run:      %q", stdout.Bytes(), goOut)
			}
			result, err := lower.Compile(program.File, lower.Options{Origin: name})
			if err != nil {
				t.Fatalf("lower: %v", err)
			}
			generated := filepath.Join(t.TempDir(), "generated.go")
			if err := os.WriteFile(generated, result.Source, 0o600); err != nil {
				t.Fatal(err)
			}
			loweredOut, err := exec.Command("go", "run", generated).Output()
			if err != nil {
				t.Fatalf("go run lowered program: %v\n%s", err, result.Source)
			}
			if !bytes.Equal(loweredOut, goOut) {
				t.Fatalf("lowered stdout differs from go run\nlowered: %q\ngo run:  %q", loweredOut, goOut)
			}
		})
	}
}

// TestSprint152ConstInitializersAsWritten pins the C3 converter half for
// Go-source constants: the source initializer is retained even when go/types
// gives us a typed constant value. Without that, unsafe.Sizeof folds to "8",
// the generated Go no longer references unsafe, and gc rejects the hoisted
// import alias as unused.
func TestSprint152ConstInitializersAsWritten(t *testing.T) {
	path := filepath.Join("testdata", "sprint152", "const-initializers-as-written", "const_initializers_as_written.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(path)
	goOut, err := exec.Command("go", "run", path).Output()
	if err != nil {
		t.Fatalf("go run: %v", err)
	}
	program, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: name})
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	source := string(result.Source)
	if !strings.Contains(source, ".Sizeof([8]byte{})") {
		t.Fatalf("const initializer was not retained as written:\n%s", source)
	}
	generated := filepath.Join(t.TempDir(), "generated.go")
	if err := os.WriteFile(generated, result.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	loweredOut, err := exec.Command("go", "run", generated).Output()
	if err != nil {
		t.Fatalf("go run lowered program: %v\n%s", err, result.Source)
	}
	if !bytes.Equal(loweredOut, goOut) {
		t.Fatalf("lowered stdout differs from go run\nlowered: %q\ngo run:  %q", loweredOut, goOut)
	}
}

func TestSprint152PackageMapAlias3(t *testing.T) {
	read := func(rel string) gosource.Source {
		t.Helper()
		path := filepath.Join("testdata", "sprint152", "alias3", filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return gosource.Source{Name: filepath.Base(rel), Data: data}
	}
	program, err := gosource.Load([]gosource.Source{read("c/c.go")}, gosource.Options{
		RunMain:    true,
		ImportBase: "test",
		ImportPath: "test/c",
		Packages: []gosource.PackageSpec{
			{Path: "test/a", Sources: []gosource.Source{read("a/a.go")}},
			{Path: "test/b", Sources: []gosource.Source{read("b/b.go")}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: "c.go"})
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	generated := filepath.Join(t.TempDir(), "generated.go")
	if err := os.WriteFile(generated, result.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "run", generated).CombinedOutput(); err != nil {
		t.Fatalf("go run lowered program: %v\n%s\n%s", err, out, result.Source)
	}
}

func TestSprint152PackageMapNonMainPackageMainIdentifier(t *testing.T) {
	read := func(rel string) gosource.Source {
		t.Helper()
		path := filepath.Join("testdata", "sprint152", "issue24801", filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return gosource.Source{Name: filepath.Base(rel), Data: data}
	}
	program, err := gosource.Load([]gosource.Source{read("main/main.go")}, gosource.Options{
		RunMain:    true,
		ImportBase: "test",
		ImportPath: "test/main",
		Packages: []gosource.PackageSpec{
			{Path: "test/a", Sources: []gosource.Source{read("a/a.go")}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: "main.go"})
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	generated := filepath.Join(t.TempDir(), "generated.go")
	if err := os.WriteFile(generated, result.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "run", generated).CombinedOutput(); err != nil {
		t.Fatalf("go run lowered program: %v\n%s\n%s", err, out, result.Source)
	}
}

func TestSprint152ConstGroupedSpecsUseGroupCarrier(t *testing.T) {
	path := filepath.Join("testdata", "sprint152", "grouped-specs", "grouped_specs.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Load([]gosource.Source{{Name: "grouped_specs.go", Data: data}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	seenGroup := false
	for _, stmt := range program.File.Stmts {
		if group, ok := stmt.Cmd.(*syntax.BashPPConstGroup); ok {
			seenGroup = true
			if len(group.Specs) != 2 {
				t.Fatalf("const group specs = %d, want 2", len(group.Specs))
			}
		}
	}
	if !seenGroup {
		t.Fatalf("no const group carrier in lowered gosource AST")
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: "grouped_specs.go"})
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if !strings.Contains(string(result.Source), "const (") {
		t.Fatalf("const block was not preserved:\n%s", result.Source)
	}
}

// TestSprint152SyntheticCallPosition pins the C5 converter half: the synthetic
// `main` wrapper's call to the renamed source main must carry no borrowed
// //line. The first declaration is an import, so the old borrowed position
// stamped a //line on the call attributing diagnostics to the import's line.
func TestSprint152SyntheticCallPosition(t *testing.T) {
	path := filepath.Join("testdata", "sprint152", "synthetic-main-call", "synthetic_main_call.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(path)
	program, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: name})
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	// Find the wrapper's call to the renamed source main and assert no //line
	// directive sits between `func main() {` and that call.
	lines := strings.Split(string(result.Source), "\n")
	wrapper := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "func main() {" {
			wrapper = i
			break
		}
	}
	if wrapper < 0 {
		t.Fatalf("no main in lowered source:\n%s", result.Source)
	}
	// S152.1 C5: a Go source that declares main keeps it as the entry, so
	// there is no wrapper and no synthetic call to carry a position at all.
	if !strings.Contains(string(result.Source), "sourceMain") {
		return
	}
	found := false
	for i := wrapper + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasSuffix(trimmed, "sourceMain()") {
			found = true
			break
		}
		if strings.HasPrefix(trimmed, "//line ") {
			t.Fatalf("synthetic main call borrows a position: %q precedes the sourceMain call\n%s", trimmed, result.Source)
		}
	}
	if !found {
		t.Fatalf("no sourceMain call inside the synthetic wrapper:\n%s", result.Source)
	}
}

// TestSprint152Directives pins the C1 converter half: the //go:* directives
// written on the input's func and var declarations reach the generated Go
// above the declaration they document, and gc applies them there. Free
// comments and the file-level //go:generate do not travel.
func TestSprint152Directives(t *testing.T) {
	path := filepath.Join("testdata", "sprint152", "directives", "directives.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(path)
	program, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: name})
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	source := string(result.Source)
	// Each directive block must sit directly above its declaration, with
	// only the //line directive between.
	for _, want := range []struct{ directives, decl string }{
		{"//go:noinline\n", "func x("},
		{"//go:nosplit\n//go:norace\n", "func y("},
		{"//go:registerparams\n//go:noinline\n", "func z("},
		{"//go:noinline\n", "func (t *T) M("},
	} {
		at := strings.Index(source, want.decl)
		if at < 0 {
			t.Fatalf("no %q in lowered source:\n%s", want.decl, source)
		}
		before := source[:at]
		if i := strings.LastIndex(before, "\n//line "); i >= 0 {
			before = before[:i+1]
		}
		if !strings.HasSuffix(before, want.directives) {
			t.Errorf("%q is not preceded by %q:\n%s", want.decl, want.directives, source)
		}
	}
	for _, dropped := range []string{"go:generate", "Doc comment"} {
		if strings.Contains(source, dropped) {
			t.Errorf("%q travelled into the lowered source:\n%s", dropped, source)
		}
	}
	// gc reads the directives from where they landed.
	generated := filepath.Join(t.TempDir(), "generated.go")
	if err := os.WriteFile(generated, result.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("go", "build", "-gcflags=-m -m", "-o", os.DevNull, generated).CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	for _, want := range []string{"cannot inline x: marked go:noinline", "cannot inline z: marked go:noinline", "cannot inline (*T).M: marked go:noinline", "can inline y"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("gc did not report %q:\n%s", want, out)
		}
	}
}
