package gosource_test

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// importdecl0a.go and importdecl0b.go are one package that must be checked
// jointly: file-scoped dot-imports in one file and invalid import paths in the
// other. They parse cleanly, so every annotated /* ERROR ... */ is a semantic
// diagnostic. An invalid import path is reported through the checker's Error
// callback, never the importer, so the frontend must keep collecting positioned
// diagnostics across both files instead of aborting on the first one. These are
// the go/types and cmd/compile/internal/types2 TestCheck/importdecl0 roots.
var importdeclFixtures = []struct {
	name, sha string
}{
	{"importdecl0a.go", "917ee6d97fa48e038f5fc2db6dbc719a42f2bcdd13c412a783a52d660a87375d"},
	{"importdecl0b.go", "2577e408851b08cc0ed04e521fa02d9f6fe5945160f416c827b38ad954e6395c"},
}

func loadImportdeclFixtures(t *testing.T) []gosource.Source {
	t.Helper()
	var sources []gosource.Source
	for _, f := range importdeclFixtures {
		data, err := os.ReadFile(filepath.Join("testdata", "importdecl", f.name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != f.sha {
			t.Fatalf("original source %s changed: %s", f.name, got)
		}
		sources = append(sources, gosource.Source{Name: f.name, Data: data})
	}
	return sources
}

// nativeImportdeclDiagnostics mirrors the pinned SDK's go/types/check_test.go
// parseFiles -> testFiles flow independently of the product converter, using the
// same default importer. Secondary "\t"-prefixed clarifying messages are kept
// here; the comparison keeps the full multiset so positions and columns match.
func nativeImportdeclDiagnostics(t *testing.T, sources []gosource.Source) []string {
	t.Helper()
	sources = append([]gosource.Source(nil), sources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	fset := token.NewFileSet()
	var files []*ast.File
	var errs []string
	for _, s := range sources {
		file, err := parser.ParseFile(fset, s.Name, s.Data, parser.AllErrors|parser.SkipObjectResolution)
		if file == nil {
			t.Fatal("native parser returned nil AST", err)
		}
		files = append(files, file)
		if list, ok := err.(scanner.ErrorList); ok {
			for _, e := range list {
				errs = append(errs, e.Error())
			}
		} else if err != nil {
			errs = append(errs, err.Error())
		}
	}
	conf := types.Config{Importer: importer.Default(), Error: func(err error) { errs = append(errs, err.Error()) }}
	conf.Check(files[0].Name.Name, fset, files, nil)
	sort.Strings(errs)
	return errs
}

func TestGoSourceImportDeclJointPackage(t *testing.T) {
	sources := loadImportdeclFixtures(t)
	want := nativeImportdeclDiagnostics(t, sources)

	p, err := gosource.Load(sources, gosource.Options{})
	if p != nil || err == nil {
		t.Fatalf("negative fixture accepted: program=%v error=%v", p, err)
	}
	list, ok := err.(gosource.ErrorList)
	if !ok {
		t.Fatalf("diagnostics lost types: %T", err)
	}

	var got []string
	for _, e := range list {
		if _, ok := e.(types.Error); !ok {
			t.Fatalf("non-checker diagnostic on a cleanly parsing package: %T %v", e, e)
		}
		got = append(got, e.Error())
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("frontend diagnostics differ from native joint check flow\ngot:  %q\nwant: %q", got, want)
	}

	// The three invalid import paths in importdecl0b.go must each surface as a
	// positioned diagnostic. Before joint collection the frontend aborted on the
	// first one (candidate013 emitted a single "invalid import path" and exit 2).
	for _, at := range []string{
		"importdecl0b.go:15:2: invalid import path",
		"importdecl0b.go:16:2: invalid import path",
		"importdecl0b.go:17:2: invalid import path",
	} {
		if !containsPrefix(got, at) {
			t.Fatalf("missing invalid-import-path diagnostic %q in %q", at, got)
		}
	}
	// Joint semantics: a dot-import in one file collides with a local decl, and a
	// blank dot-import in the sibling file is unused.
	for _, needle := range []string{
		"importdecl0a.go:41:6: Value already declared through dot-import of package reflect",
		`importdecl0b.go:11:8: "unsafe" imported and not used`,
	} {
		if !containsPrefix(got, needle) {
			t.Fatalf("missing joint-package diagnostic %q in %q", needle, got)
		}
	}
}

// TestGoSourceImportDeclAnnotationsMatch replays the source-positioned matching
// the pinned SDK's check_test.go performs: every embedded ERROR/ERRORx pattern
// is satisfied by exactly one primary diagnostic at its file and line, with no
// unexpected primary diagnostics left over. Secondary "\t" continuation lines
// are dropped, exactly as check_test.go's Error callback drops ": \t" messages.
func TestGoSourceImportDeclAnnotationsMatch(t *testing.T) {
	sources := loadImportdeclFixtures(t)
	_, err := gosource.Load(sources, gosource.Options{})
	list, ok := err.(gosource.ErrorList)
	if !ok {
		t.Fatalf("diagnostics lost types: %T %v", err, err)
	}

	type annot struct {
		file    string
		line    int
		pattern string
		regex   bool
	}
	annotRE := regexp.MustCompile(`/\* (ERRORx?) (.*?) \*/`)
	var want []annot
	for _, s := range sources {
		for i, text := range strings.Split(string(s.Data), "\n") {
			for _, m := range annotRE.FindAllStringSubmatch(text, -1) {
				unquoted, uerr := strconv.Unquote(strings.TrimSpace(m[2]))
				if uerr != nil {
					t.Fatalf("%s:%d: cannot unquote annotation %q: %v", s.Name, i+1, m[2], uerr)
				}
				want = append(want, annot{s.Name, i + 1, unquoted, m[1] == "ERRORx"})
			}
		}
	}
	if len(want) == 0 {
		t.Fatal("no ERROR annotations parsed from fixtures")
	}

	// Primary diagnostics only: drop the checker's secondary continuation lines.
	posRE := regexp.MustCompile(`^(.+):(\d+):\d+: (.*)$`)
	type primary struct {
		file string
		line int
		msg  string
	}
	var got []primary
	for _, e := range list {
		text := e.Error()
		if strings.Contains(text, ": \t") {
			continue
		}
		m := posRE.FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("unpositioned primary diagnostic: %q", text)
		}
		line, _ := strconv.Atoi(m[2])
		got = append(got, primary{m[1], line, m[3]})
	}

	used := make([]bool, len(got))
	for _, w := range want {
		matched := -1
		for i, g := range got {
			if used[i] || g.file != w.file || g.line != w.line {
				continue
			}
			if w.regex {
				if regexp.MustCompile(w.pattern).MatchString(g.msg) {
					matched = i
					break
				}
			} else if strings.Contains(g.msg, w.pattern) {
				matched = i
				break
			}
		}
		if matched < 0 {
			t.Fatalf("no diagnostic satisfies %s:%d %q", w.file, w.line, w.pattern)
		}
		used[matched] = true
	}
	for i, g := range got {
		if !used[i] {
			t.Fatalf("unexpected diagnostic %s:%d: %s", g.file, g.line, g.msg)
		}
	}
	t.Logf("matched %d source-positioned annotations across %d files", len(want), len(sources))
}

func containsPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
