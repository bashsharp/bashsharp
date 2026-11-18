package lower_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// columnZero matches a generated line directive with column 0, which the Go
// compiler rejects ("invalid column number: 0"). A position with no column —
// one governed by a line-only user directive — must use the "//line file:N"
// form instead.
var columnZero = regexp.MustCompile(`(?m)^//line .*:0$`)

// userDirectiveSrc carries a user line-only //line directive before a
// statement that observes its own reported position.
const userDirectiveSrc = `package main

import (
	"fmt"
	"runtime"
)

func main() {
//line /foo/bar.go:123
	_, f, l, _ := runtime.Caller(0)
	fmt.Printf("%s:%d\n", f, l)
}
`

func compileGoSource(t *testing.T, name, src string) *lower.Result {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(src), name, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: name})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// goRun compiles and runs generated Go with the host toolchain and returns
// its combined output. The generated program is self-contained std-only Go.
func goRun(t *testing.T, generated []byte) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go binary in PATH: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), generated, 0o666); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "run", "main.go")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n--- output\n%s\n--- generated\n%s", err, out, generated)
	}
	return string(out)
}

// A user //line directive with no column must not become a ":0" directive,
// and the emitted directive must reproduce the user's filename and line, so
// runtime.Caller reports the position the Go compiler would have reported
// for the original source.
func TestSourceMapUserLineDirective(t *testing.T) {
	result := compileGoSource(t, "main.go", userDirectiveSrc)
	if m := columnZero.Find(result.Source); m != nil {
		t.Errorf("generated source has a column-0 line directive %q\n%s", m, result.Source)
	}
	if !bytes.Contains(result.Source, []byte("//line /foo/bar.go:123\n")) {
		t.Errorf("generated source does not reproduce the user directive position\n%s", result.Source)
	}
	if out := goRun(t, result.Source); !strings.Contains(out, "/foo/bar.go:123") {
		t.Errorf("runtime.Caller reported %q, want /foo/bar.go:123", out)
	}
}

// directiveFormsSrc mirrors test/fixedbugs/issue22662.go: every //line and
// /*line*/ form, self-checked through runtime.Caller(1). The compiler resolves
// a relative directive filename against the source directory, so check accepts
// the file as a path-component suffix (go.dev/issue/70478).
const directiveFormsSrc = `package main

import (
	"fmt"
	"runtime"
	"strings"
)

func check(file string, line int) {
	_, f, l, ok := runtime.Caller(1)
	if !ok {
		panic("runtime.Caller(1) failed")
	}
	want := "/" + strings.TrimPrefix(file, "/")
	if (f != file && !strings.HasSuffix(f, want)) || l != line {
		panic(fmt.Sprintf("got %s:%d; want %s:%d", f, l, file, line))
	}
}

func main() {
//line :1
	check("??", 1)
//line foo.go:1
	check("foo.go", 1)
//line bar.go:10:20
	check("bar.go", 10)
//line :11:22
	check("bar.go", 11)
	/*line :30*/ check("??", 30)
	/*line foo.go:20*/ check("foo.go", 20)
}
`

func TestSourceMapLineDirectiveForms(t *testing.T) {
	result := compileGoSource(t, "main.go", directiveFormsSrc)
	if m := columnZero.Find(result.Source); m != nil {
		t.Errorf("generated source has a column-0 line directive %q\n%s", m, result.Source)
	}
	goRun(t, result.Source)
}

// The map must keep both halves of a directive-governed position: the
// physical origin (file name and byte offset) and the adjusted position the
// Go compiler would have reported (directive filename, line, and no column
// for a line-only directive).
func TestSourceMapRecordsPhysicalAndAdjusted(t *testing.T) {
	result := compileGoSource(t, "main.go", userDirectiveSrc)
	if err := result.ValidateMappings(); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range result.Mappings {
		if m.Node != "BashPPShortDecl" {
			continue
		}
		found = true
		if m.Source != "main.go" {
			t.Errorf("physical source = %q, want main.go", m.Source)
		}
		if want := uint(strings.Index(userDirectiveSrc, "_, f, l")); m.SourceOffset != want {
			t.Errorf("physical offset = %d, want %d", m.SourceOffset, want)
		}
		if m.Pos.Line() != 123 {
			t.Errorf("adjusted line = %d, want 123", m.Pos.Line())
		}
		if m.Pos.Col() != 0 {
			t.Errorf("adjusted column = %d, want 0 (line-only directive)", m.Pos.Col())
		}
	}
	if !found {
		t.Fatalf("no mapping for the Caller statement: %+v", result.Mappings)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources = %+v, want one", result.Sources)
	}
	directives := result.Sources[0].LineDirectives
	if len(directives) != 1 || directives[0].Filename != "/foo/bar.go" {
		t.Fatalf("line directives = %+v, want one for /foo/bar.go", directives)
	}
	if want := uint(strings.Index(userDirectiveSrc, "\t_, f, l")); directives[0].Offset != want {
		t.Errorf("directive offset = %d, want %d (start of the governed line)", directives[0].Offset, want)
	}
}
