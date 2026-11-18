package syntax

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestBashPPImportExactShapes(t *testing.T) {
	for _, src := range []string{
		`import "fmt"`, `import f "fmt"`, `import "encoding/json"`,
		`import _ "fmt"`, `import . "fmt"`,
		"import (\n\t\"fmt\"\n\tf \"log\"\n\t_ \"embed\"\n)",
		"import\n(\n\t\"fmt\"\n)", `import ()`, "import (\n\t\"fmt\";\n)",
		`import "net/http"`, `import "crypto/x509"`, `import "testing/fstest"`,
		`import "syscall/js"`, `import "unsafe"`, `import f "\x66mt"`,
		`import "example.com/local/pkg"`, `import p "local/pkg"`,
		`import _ "example.com/sideeffect"`, `import . "example.com/dot"`,
	} {
		for _, chunk := range []int{0, 1} {
			var rd interface{ Read([]byte) (int, error) } = strings.NewReader(src)
			if chunk == 1 {
				rd = &oneByteReader{r: strings.NewReader(src)}
			}
			f, err := NewParser(Variant(LangBashPP)).Parse(rd, "")
			if err != nil {
				t.Fatalf("%q chunk=%d: %v", src, chunk, err)
			}
			if _, ok := f.Stmts[0].Cmd.(*BashPPImport); !ok {
				t.Fatalf("%q: got %T", src, f.Stmts[0].Cmd)
			}
		}
	}
}

type oneByteReader struct{ r *strings.Reader }

func (r *oneByteReader) Read(p []byte) (int, error) { return r.r.Read(p[:1]) }

func TestBashPPImportReservedNearMisses(t *testing.T) {
	shapes := []string{
		`import`, `import fmt`, `import 'fmt'`, `import "$pkg"`,
		`import if "fmt"`, `import f "fmt" extra`,
		`X=1 import "fmt"`, `import "fmt" >out`, `import "bad path"`,
		"import \"fmt\\nlog\"", `import "fmt\\"`,
		`import "./fmt"`, `import "../fmt"`, `import "/tmp/x"`,
		`import "fmt/"`, `import "fmt//x"`, `import "C:fmt"`,
		"import (\n\t\"fmt\" extra\n)",
		`import (; "fmt")`, "import (\n\t\"fmt\";;\n)", "import (\n\t\"fmt\" \"log\"\n)",
	}
	for _, src := range shapes {
		assertImportReservedError(t, src, false)
		assertImportReservedError(t, src, true)
	}
}

func TestBashPPImportShellEscapes(t *testing.T) {
	for _, src := range []string{`"import" "fmt"`, `'import' "fmt"`, `command import "fmt"`} {
		assertImportFallbackExact(t, src, false)
		assertImportFallbackExact(t, src, true)
	}
}

func assertImportReservedError(t *testing.T, src string, oneByte bool) {
	t.Helper()
	var rd interface{ Read([]byte) (int, error) } = strings.NewReader(src)
	if oneByte {
		rd = &oneByteReader{r: strings.NewReader(src)}
	}
	_, err := NewParser(Variant(LangBashPP)).Parse(rd, "reserved.sh")
	want := "reserved.sh:1:1: invalid import statement; use command import to invoke a shell command"
	if strings.HasPrefix(src, "X=1 ") {
		want = "reserved.sh:1:5: invalid import statement; use command import to invoke a shell command"
	}
	switch src {
	case "import (\n\t`fmt`\n)\n", "import (\n\t'fmt'\n)\n", "import (\n\t\"fmt\" extra\n)", `import (; "fmt")`, "import (\n\t\"fmt\";;\n)", "import (\n\t\"fmt\" \"log\"\n)":
		want = "reserved.sh:1:1: `foo(` must be followed by `)`"
	}

	if fmt.Sprint(err) != want {
		t.Errorf("reserved import %q oneByte=%v: got %v; want %s", src, oneByte, err, want)
	}
}

func assertImportFallbackExact(t *testing.T, src string, oneByte bool) {
	t.Helper()
	parse := func(lang LangVariant) (*File, error) {
		var rd interface{ Read([]byte) (int, error) } = strings.NewReader(src)
		if oneByte {
			rd = &oneByteReader{r: strings.NewReader(src)}
		}
		return NewParser(Variant(lang)).Parse(rd, "fallback.sh")
	}
	bash, bashErr := parse(LangBash)
	pp, ppErr := parse(LangBashPP)
	if !reflect.DeepEqual(bash, pp) || fmt.Sprint(bashErr) != fmt.Sprint(ppErr) || reflect.TypeOf(bashErr) != reflect.TypeOf(ppErr) {
		t.Fatalf("fallback differs for %q oneByte=%v: bash=%#v err=%T %v; pp=%#v err=%T %v", src, oneByte, bash, bashErr, bashErr, pp, ppErr, ppErr)
	}
}

func TestGo127StdlibAllowlistProvenanceAndNearMisses(t *testing.T) {
	if go127StdlibSourceSHA256 != "76188f97e2bc012cb716a6e21d49ff38858eed94a1845ee3534e74a8208ff291" {
		t.Fatalf("unreviewed Go 1.27 source inventory: %s", go127StdlibSourceSHA256)
	}
	if !slices.IsSorted(go127StdlibImports[:]) {
		t.Fatal("Go 1.27 standard-library allowlist is not sorted")
	}
	for i, path := range go127StdlibImports {
		if i > 0 && path == go127StdlibImports[i-1] {
			t.Fatalf("duplicate allowlist path %q", path)
		}
	}
	joined := strings.Join(go127StdlibImports[:], "\n") + "\n"
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(joined))); got != "de444f71390a90f274b5176d8da92480ab72e2992d8885823712cd927e289f6c" {
		t.Fatalf("unreviewed Go 1.27 import allowlist checksum: %s", got)
	}

	// Mutate every reviewed name across excluded namespace classes. P2C admits
	// valid non-standard paths into the typed AST, while the reviewed standard
	// library inventory itself remains exact.
	for _, path := range go127StdlibImports {
		for _, near := range []string{
			"/" + path, "./" + path, "../" + path,
			"cmd/" + path, "internal/" + path, "vendor/" + path,
			path + "/internal", path + "/vendor", path + "_test", path + ".module",
		} {
			if isGo127StdlibImport(near) {
				t.Fatalf("near miss %q unexpectedly allowed (from %q)", near, path)
			}
			if strings.HasPrefix(near, "/") || strings.HasPrefix(near, "./") || strings.HasPrefix(near, "../") {
				assertImportReservedError(t, `import "`+near+`"`, true)
			}
		}
	}
}

func TestBashPPImportClassicAndPOSIXNeverClaim(t *testing.T) {
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		for _, src := range []string{`import "fmt"`, `import f "fmt"`, "import (\n\t\"fmt\"\n)", "import\n(\n\t\"fmt\"\n)"} {
			want, wantErr := NewParser(Variant(lang)).Parse(strings.NewReader(src), "")
			got, gotErr := NewParser(Variant(lang)).Parse(&oneByteReader{r: strings.NewReader(src)}, "")
			if !reflect.DeepEqual(got, want) || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) ||
				reflect.TypeOf(gotErr) != reflect.TypeOf(wantErr) {
				t.Fatalf("%v one-byte parse differs for %q: normal=%#v err=%T %v; one-byte=%#v err=%T %v",
					lang, src, want, wantErr, wantErr, got, gotErr, gotErr)
			}
			for _, f := range []*File{want, got} {
				if len(f.Stmts) > 0 {
					if _, ok := f.Stmts[0].Cmd.(*BashPPImport); ok {
						t.Fatalf("%v claimed %q", lang, src)
					}
				}
			}
		}
	}
}

func TestBashPPGroupedImportCanonicalPrint(t *testing.T) {
	for _, test := range []struct {
		src, want string
	}{
		{"import\n(\n\t\"fmt\"\n)\n", "import (\n\t\"fmt\"\n)\n"},
		{"import ()\n", "import ()\n"},
		{"import (\n\t\"fmt\";\n)\n", "import (\n\t\"fmt\"\n)\n"},
	} {
		f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(test.src), "")
		if err != nil {
			t.Fatalf("parse %q: %v", test.src, err)
		}
		var out bytes.Buffer
		if err := NewPrinter().Print(&out, f); err != nil {
			t.Fatalf("print %q: %v", test.src, err)
		}
		if got := out.String(); got != test.want {
			t.Fatalf("print %q: got %q, want %q", test.src, got, test.want)
		}
	}
}

func TestBashPPGroupedImportSameLineParsePrintReparse(t *testing.T) {
	for _, test := range []struct {
		src, want string
	}{
		{`import ("fmt")`, "import (\n\t\"fmt\"\n)\n"},
		{`import ("fmt";)`, "import (\n\t\"fmt\"\n)\n"},
		{`import ("fmt"; "log")`, "import (\n\t\"fmt\"\n\t\"log\"\n)\n"},
		{`import ("fmt") # after`, "import (\n\t\"fmt\"\n) # after\n"},
	} {
		for _, oneByte := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/oneByte=%t", test.src, oneByte), func(t *testing.T) {
				parse := func(src string) *File {
					var rd interface{ Read([]byte) (int, error) } = strings.NewReader(src)
					if oneByte {
						rd = &oneByteReader{r: strings.NewReader(src)}
					}
					f, err := NewParser(Variant(LangBashPP), KeepComments(true)).Parse(rd, "")
					if err != nil {
						t.Fatalf("parse %q: %v", src, err)
					}
					if len(f.Stmts) != 1 {
						t.Fatalf("parse %q: got %d statements, want 1", src, len(f.Stmts))
					}
					if _, ok := f.Stmts[0].Cmd.(*BashPPImport); !ok {
						t.Fatalf("parse %q: got %T, want *BashPPImport", src, f.Stmts[0].Cmd)
					}
					return f
				}
				print := func(f *File) string {
					var out bytes.Buffer
					if err := NewPrinter().Print(&out, f); err != nil {
						t.Fatal(err)
					}
					return out.String()
				}

				got := print(parse(test.src))
				if got != test.want {
					t.Fatalf("print %q: got %q, want %q", test.src, got, test.want)
				}
				if got2 := print(parse(got)); got2 != got {
					t.Fatalf("print-reparse is not idempotent: first %q, second %q", got, got2)
				}
			})
		}
	}
}

func TestBashPPImportPrintWalk(t *testing.T) {
	src := "import\n# between\n(\n# first\n\"fmt\" # inline\n# second\nf \"log\"\n# last\n)\n"
	f, err := NewParser(Variant(LangBashPP), KeepComments(true)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	want := "import\n# between\n(\n\t# first\n\t\"fmt\" # inline\n\t# second\n\tf \"log\"\n\t# last\n)\n"
	if err := NewPrinter().Print(&out, f); err != nil || out.String() != want {
		t.Fatalf("%q %v", out.String(), err)
	}
	imp := f.Stmts[0].Cmd.(*BashPPImport)
	if len(imp.Comments) != 1 || len(imp.Specs) != 2 || len(imp.Specs[0].Comments) != 1 ||
		len(imp.Specs[1].Comments) != 2 || len(imp.Last) != 1 {
		t.Fatalf("comments were not retained in typed nodes: %#v", imp)
	}
	seen, specs, comments := false, 0, 0
	Walk(f, func(n Node) bool {
		if _, ok := n.(*BashPPImport); ok {
			seen = true
		}
		if _, ok := n.(*BashPPImportSpec); ok {
			specs++
		}
		if _, ok := n.(*Comment); ok {
			comments++
		}
		return true
	})
	if !seen || specs != 2 || comments != 5 {
		t.Fatal("walk missed BashPPImport")
	}
}

func TestBashPPPythonImport(t *testing.T) {
	tests := []struct {
		src, want, environment, alias string
	}{
		{`import python "nanochat.execution"`, "import python \"nanochat.execution\"\n", "", "execution"},
		{`import python "nanochat.execution" as nano`, "import python \"nanochat.execution\" as nano\n", "", "nano"},
		{`import python[training] "nanochat.execution" as nano`, "import python[training] \"nanochat.execution\" as nano\n", "training", "nano"},
		// B9: a .py file by relative, absolute or Windows path; the default
		// alias is the file stem.
		{`import python "./tools/greet.py"`, "import python \"./tools/greet.py\"\n", "", "greet"},
		{`import python "../lib/helpers.py" as h`, "import python \"../lib/helpers.py\" as h\n", "", "h"},
		{`import python "/opt/tools/greet.py"`, "import python \"/opt/tools/greet.py\"\n", "", "greet"},
		{`import python "greet.py"`, "import python \"greet.py\"\n", "", "greet"},
		{`import python "C:/tools/greet.py"`, "import python \"C:/tools/greet.py\"\n", "", "greet"},
		{`import python "C:\\tools\\greet.py"`, "import python \"C:\\\\tools\\\\greet.py\"\n", "", "greet"},
		{`import python "/c/tools/GREET.PY" as greet`, "import python \"/c/tools/GREET.PY\" as greet\n", "", "greet"},
	}
	for _, test := range tests {
		for _, oneByte := range []bool{false, true} {
			var input interface{ Read([]byte) (int, error) } = strings.NewReader(test.src)
			if oneByte {
				input = &oneByteReader{r: strings.NewReader(test.src)}
			}
			file, err := NewParser(Variant(LangBashPP)).Parse(input, "python.bpp")
			if err != nil {
				t.Fatalf("parse %q: %v", test.src, err)
			}
			imp, ok := file.Stmts[0].Cmd.(*BashPPImport)
			if !ok || imp.Language == nil || imp.Language.Value != "python" {
				t.Fatalf("parse %q: got %#v", test.src, file.Stmts[0].Cmd)
			}
			got := ""
			if imp.Environment != nil {
				got = imp.Environment.Value
				if imp.Lbrack.Col()+1 != imp.Environment.Pos().Col() || imp.Environment.End().Col() != imp.Rbrack.Col() {
					t.Fatalf("environment positions: [%v %v %v]", imp.Lbrack, imp.Environment.Pos(), imp.Rbrack)
				}
			}
			if got != test.environment {
				t.Fatalf("environment = %q, want %q", got, test.environment)
			}
			alias, _ := BashPPDerivedImportAlias(imp.Path.Parts[0].(*Lit).Value)
			if imp.Alias != nil {
				alias = imp.Alias.Value
			}
			wantEnd := imp.Path.End()
			if imp.Alias != nil {
				wantEnd = imp.Alias.End()
			}
			if alias != test.alias || imp.End() != wantEnd {
				t.Fatalf("alias/end = %q/%v", alias, imp.End())
			}
			var out bytes.Buffer
			if err := NewPrinter().Print(&out, file); err != nil || out.String() != test.want {
				t.Fatalf("print = %q, %v; want %q", out.String(), err, test.want)
			}
			seen := map[string]bool{}
			Walk(imp, func(node Node) bool {
				if lit, ok := node.(*Lit); ok {
					seen[lit.Value] = true
				}
				return true
			})
			if !seen["python"] || !seen[imp.Path.Parts[0].(*Lit).Value] || test.environment != "" && !seen[test.environment] {
				t.Fatalf("walk literals = %#v", seen)
			}
		}
	}
}

func TestBashPPPythonImportReservedNearMisses(t *testing.T) {
	for _, src := range []string{
		`import python`,
		`import python "mod" as`, `import python "mod" alias x`,
	} {
		assertImportReservedError(t, src, false)
		assertImportReservedError(t, src, true)
	}
	for _, src := range []string{`import python "mod" as _`, `import python "mod" as .`} {
		assertImportReservedError(t, src, false)
		assertImportReservedError(t, src, true)
	}
	// A file operand must name a .py source, and a stem that is not an
	// identifier needs an explicit alias.
	for _, src := range []string{
		`import python "./tools/greet"`, `import python "./tools/.py"`,
		`import python "./tools/my-greet.py"`, `import python "./tools/1st.py"`,
	} {
		assertImportReservedError(t, src, false)
		assertImportReservedError(t, src, true)
	}
	// Without a .py suffix a plain relative operand keeps its existing
	// reading: a Go import of "tools/greet" bound as `python`.
	file, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(`import python "tools/greet"`), "")
	if err != nil {
		t.Fatal(err)
	}
	if imp, ok := file.Stmts[0].Cmd.(*BashPPImport); !ok || imp.Language != nil || imp.Alias == nil || imp.Alias.Value != "python" {
		t.Fatalf("Go import reading lost: %#v", file.Stmts[0].Cmd)
	}
	if _, ok := BashPPDerivedImportAlias("./tools/my-greet.py"); ok {
		t.Fatal("derived an alias from a non-identifier stem")
	}
	if alias, ok := BashPPDerivedImportAlias(`C:\tools\Greet.PY`); !ok || alias != "Greet" {
		t.Fatalf("derived alias for a Windows path = %q, %t", alias, ok)
	}
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		file, err := NewParser(Variant(lang)).Parse(strings.NewReader(`import python "mod" as py`), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, claimed := file.Stmts[0].Cmd.(*BashPPImport); claimed {
			t.Fatalf("%v claimed Python import", lang)
		}
	}
}
