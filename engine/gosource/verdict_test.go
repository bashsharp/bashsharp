package gosource_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// diagnosticsOf loads one source and returns its rendered diagnostics
// (nil for an accepted program).
func diagnosticsOf(t *testing.T, name, src string) []string {
	t.Helper()
	_, err := gosource.Load([]gosource.Source{{Name: name, Data: []byte(src)}}, gosource.Options{})
	if err == nil {
		return nil
	}
	list, ok := err.(gosource.ErrorList)
	if !ok {
		t.Fatalf("diagnostics lost types: %T %v", err, err)
	}
	var out []string
	for _, e := range list {
		out = append(out, e.Error())
	}
	return out
}

func TestTypeErrorContinuations(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{
			"redeclare.go",
			"package p\nvar x int\nvar x string\n",
			"redeclare.go:3:5: x redeclared in this block\n\tredeclare.go:2:5: other declaration of x",
		},
		{
			"switch.go",
			"package p\nfunc f() {\n\tswitch 0 {\n\tcase 1:\n\tcase 1:\n\t}\n}\n",
			"switch.go:5:7: duplicate case 1 (constant of type int) in expression switch\n\tswitch.go:4:7: previous case",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gosource.Load([]gosource.Source{{Name: tc.name, Data: []byte(tc.src)}}, gosource.Options{})
			if err == nil {
				t.Fatal("invalid source accepted")
			}
			if got := err.Error(); got != tc.want {
				t.Fatalf("diagnostics differ\ngot:\n%s\nwant:\n%s", got, tc.want)
			}
			unindented := 0
			for _, line := range strings.Split(err.Error(), "\n") {
				if !strings.HasPrefix(line, "\t") {
					unindented++
				}
			}
			if unindented != 1 {
				t.Fatalf("got %d unindented diagnostics, want one", unindented)
			}
		})
	}
}

func TestCheckerBranchErrors(t *testing.T) {
	const src = "package p\nfunc f() {\nL:\n\tfor {\nL:\n\t\tbreak L\n\t}\n}\n"
	for _, tc := range []struct {
		name string
		opts gosource.Options
		want string
	}{
		{"gc", gosource.Options{}, "branch.go:5:1: label L already defined at branch.go:3:1"},
		{"go-types", gosource.Options{CheckerBranchErrors: true}, "branch.go:5:1: label L already declared\n\tbranch.go:3:1: other declaration of L"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gosource.Load([]gosource.Source{{Name: "branch.go", Data: []byte(src)}}, tc.opts)
			if err == nil {
				t.Fatal("duplicate label accepted")
			}
			if got := err.Error(); got != tc.want {
				t.Fatalf("diagnostic differs\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestCheckAfterSyntaxErrors(t *testing.T) {
	const src = "package p\nimport ;\nvar n int = \"wrong\"\n"
	const syntaxLine = "mixed.go:2:8: syntax error: missing import path"
	const checkerLine = `mixed.go:3:13: cannot use "wrong" (untyped string constant) as int value in variable declaration`
	for _, tc := range []struct {
		name string
		opts gosource.Options
		want string
	}{
		{"gc", gosource.Options{}, syntaxLine},
		{"go-types", gosource.Options{CheckAfterSyntaxErrors: true}, syntaxLine + "\n" + checkerLine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gosource.Load([]gosource.Source{{Name: "mixed.go", Data: []byte(src)}}, tc.opts)
			if err == nil {
				t.Fatal("invalid source accepted")
			}
			if got := err.Error(); got != tc.want {
				t.Fatalf("diagnostics differ\ngot:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}

	const validSyntax = "package p\nvar n int = \"wrong\"\n"
	var withoutOption string
	for _, opts := range []gosource.Options{{}, {CheckAfterSyntaxErrors: true}} {
		_, err := gosource.Load([]gosource.Source{{Name: "valid.go", Data: []byte(validSyntax)}}, opts)
		if err == nil {
			t.Fatal("type error accepted")
		}
		if withoutOption == "" {
			withoutOption = err.Error()
		} else if got := err.Error(); got != withoutOption {
			t.Fatalf("option changed diagnostics without syntax errors\ngot:  %q\nwant: %q", got, withoutOption)
		}
	}
}

// TestGoTypesParserDiagnostics keeps the go/types check_test runner's parser
// coordinates separate from gc's. The runner parses these forms with
// go/parser before go/types; types2 instead gets the gc syntax verdict.
func TestGoTypesParserDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
	}{
		{
			name:   "receiver",
			source: "package p\n\ntype T struct{}\nfunc ( /* method has no receiver */ ) _() {}\n",
			want:   "receiver.go:4:6: method has no receiver",
		},
		{
			name:   "slice",
			source: "package p\nfunc f(a []int) {\n\t_ = a[: : ]\n}\n",
			want:   "slice.go:3:8: middle index required in 3-index slice",
		},
		{
			name:   "for-post",
			source: "package p\nfunc f() {\n\tfor i := 0; i < 10; j := 0 {}\n}\n",
			want:   "for-post.go:3:22: cannot declare in post statement",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gosource.Load([]gosource.Source{{Name: tc.name + ".go", Data: []byte(tc.source)}}, gosource.Options{
				CheckAfterSyntaxErrors: true,
			})
			if err == nil {
				t.Fatal("invalid source accepted by the default gc verdict")
			}
			defaultList, ok := err.(gosource.ErrorList)
			if !ok || len(defaultList) == 0 {
				t.Fatalf("default diagnostics = %T %[1]v, want gc parser row", err)
			}
			if got := defaultList[0].Error(); got == tc.want {
				t.Fatalf("default parser coordinate = %q, want a distinct gc coordinate", got)
			}

			_, err = gosource.Load([]gosource.Source{{Name: tc.name + ".go", Data: []byte(tc.source)}}, gosource.Options{
				CheckAfterSyntaxErrors:   true,
				GoTypesParserDiagnostics: true,
			})
			if err == nil {
				t.Fatal("invalid source accepted")
			}
			list, ok := err.(gosource.ErrorList)
			if !ok || len(list) == 0 {
				t.Fatalf("diagnostics = %T %[1]v, want positioned parser row", err)
			}
			if got := list[0].Error(); got != tc.want {
				t.Fatalf("parser position = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGoTypesParserDiagnosticsDedupsRecoveredGoStatement(t *testing.T) {
	_, err := gosource.Load([]gosource.Source{{Name: "go.go", Data: []byte("package p\nfunc f() { go 1 }\n")}}, gosource.Options{
		CheckAfterSyntaxErrors:   true,
		GoTypesParserDiagnostics: true,
	})
	if err == nil {
		t.Fatal("invalid go statement accepted")
	}
	list, ok := err.(gosource.ErrorList)
	if !ok {
		t.Fatalf("diagnostics = %T, want ErrorList", err)
	}
	count := 0
	for _, row := range list {
		if strings.Contains(row.Error(), "expression in go must be function call") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("go statement diagnostic count = %d, want 1: %v", count, list)
	}
}

// TestSyntaxVerdictClasses covers one out-of-corpus reproducer per failure
// class from gosource/testdata/sprint154/parser/FINDINGS.md. Each case
// asserts the exact gc diagnostic (message, line, col — column is 1-based
// and counts bytes, like go/scanner) and the exact diagnostic count: when
// gc's parser rejects a file, its errors are the complete result.
func TestSyntaxVerdictClasses(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []string
	}{
		// Unexpected-token wording (corpus: switch2.go, issue18092.go).
		{"colon.go", "package p\n\nfunc f(x int) {\n\tswitch x {\n\tcase 1\n\t}\n}\n", []string{
			"colon.go:5:8: syntax error: unexpected newline, expected :",
		}},
		// Missing comma before newline (corpus: issue14520.go, issue22164.go).
		{"comma.go", "package p\n\nfunc f(a int\nb int) {\n}\n", []string{
			"comma.go:3:13: syntax error: unexpected newline in parameter list; possibly missing comma or )",
		}},
		// Else placement (corpus: syntax/else.go).
		{"else.go", "package p\n\nfunc f(x bool) {\n\tif x {\n\t} else x = false\n}\n", []string{
			"else.go:5:9: syntax error: else must be followed by if or statement block",
		}},
		// Scanner stage: BOM in the middle of the file (corpus: bombad.go).
		// gc's source layer drops the BOM and its scanner's segment for the
		// literal still runs to the newline, so types2 also reports the
		// literal as malformed (Sprint 165: `go tool compile -e` verbatim).
		{"bom.go", "package p\n\nvar x = 1\xEF\xBB\xBF\n", []string{
			"bom.go:3:9: malformed constant: 1\ufeff",
			"bom.go:3:10: invalid BOM in the middle of the file",
		}},
		// CheckBranches: label defined and not used (corpus: label.go) —
		// gc reports this at syntax stage, not from its type checker.
		{"label.go", "package p\n\nfunc f() {\nL:\n\tfor {\n\t}\n}\n", []string{
			"label.go:4:1: label L defined and not used",
		}},
		// CheckBranches: goto over declaration (corpus: goto.go).
		{"goto.go", "package p\n\nfunc f() {\n\tgoto L\n\tx := 1\n\t_ = x\nL:\n}\n", []string{
			"goto.go:4:7: goto L jumps over declaration of x at goto.go:5:4",
		}},
		// Column convention check on a line with multibyte runes before the
		// error: "αβγ" is nine bytes, so the byte-counting column is 18
		// (a rune-counting column would say 15).
		{"multibyte.go", "package p\n\nvar s = \"αβγ\" oops\n", []string{
			"multibyte.go:3:18: syntax error: unexpected name oops after top level declaration",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := diagnosticsOf(t, tc.name, tc.src)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("diagnostics differ from gc\ngot: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestSyntaxVerdictNegatives pins the verdict's boundaries: an accepted
// program reaches go/types as before, a rejected one never does, and a
// multi-error file reports each gc error once, in source order.
func TestSyntaxVerdictNegatives(t *testing.T) {
	// Accepted program: no syntax diagnostics; the existing go/types
	// diagnostic still appears.
	got := diagnosticsOf(t, "neg.go", "package p\n\nvar x int = \"s\"\n")
	want := []string{`neg.go:3:13: cannot use "s" (untyped string constant) as int value in variable declaration`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("accepted program lost its go/types diagnostic\ngot: %q\nwant: %q", got, want)
	}

	// Syntax error: gc's error is everything. The unused import "os" —
	// a certain go/types diagnostic — must not be reported, because gc
	// runs no type checker after a syntax error.
	got = diagnosticsOf(t, "synonly.go", "package p\n\nimport \"os\"\n\nfunc f() {\n\tx :=\n}\n")
	want = []string{"synonly.go:7:1: syntax error: unexpected }, expected expression"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("syntax-stage load leaked past gc's errors\ngot: %q\nwant: %q", got, want)
	}
	for _, d := range got {
		if strings.Contains(d, "imported and not used") {
			t.Fatalf("go/types diagnostic after a syntax error: %q", d)
		}
	}

	// Multi-error file: each error once, in source order.
	got = diagnosticsOf(t, "multi.go", "package p\n\nfunc f(x int) {\n\tswitch x {\n\tcase 1\n\tcase 2\n\t}\n}\n")
	want = []string{
		"multi.go:5:8: syntax error: unexpected newline, expected :",
		"multi.go:6:8: syntax error: unexpected newline, expected :",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("multi-error file not once-per-error in source order\ngot: %q\nwant: %q", got, want)
	}
}

// TestSyntaxVerdictReproducers wires the S154.3 reproducer pairs to the gc
// wording: every reject.go now fails with exactly the diagnostics gc prints
// (parser-range-three: gc's parser accepts the clause and types2 diagnoses
// it, range_arity.go), and every accept.go still loads cleanly.
func TestSyntaxVerdictReproducers(t *testing.T) {
	base := filepath.Join("testdata", "sprint154", "parser", "testdata", "reproducers")
	for _, tc := range []struct {
		dir    string
		reject []string
	}{
		{"checker-after-syntax", []string{`reject.go:3:14: invalid character '\'' in octal escape`}},
		{"checker-label-unused", []string{"reject.go:4:1: label L defined and not used"}},
		{"parser-case-colon", []string{"reject.go:5:8: syntax error: unexpected newline, expected :"}},
		{"parser-else", []string{"reject.go:5:9: syntax error: else must be followed by if or statement block"}},
		{"parser-range-three", []string{"reject.go:4:12: range clause permits at most two iteration variables", "reject.go:5:19: undefined: c"}},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			// reject.go.src: deliberately invalid Go, kept outside the *.go set the
			// repository-wide gofmt gate (scripts/fmtcheck.sh) parses; it is loaded
			// under the name reject.go so the diagnostics read as gc's would.
			data, err := os.ReadFile(filepath.Join(base, tc.dir, "reject.go.src"))
			if err != nil {
				t.Fatal(err)
			}
			got := diagnosticsOf(t, "reject.go", string(data))
			if !reflect.DeepEqual(got, tc.reject) {
				t.Fatalf("reject.go diagnostics differ from gc\ngot: %q\nwant: %q", got, tc.reject)
			}
			data, err = os.ReadFile(filepath.Join(base, tc.dir, "accept.go"))
			if err != nil {
				t.Fatal(err)
			}
			if got := diagnosticsOf(t, "accept.go", string(data)); got != nil {
				t.Fatalf("accept.go rejected: %q", got)
			}
		})
	}
}

// TestSyntaxVerdictPackageMap covers the explicit-package path: a syntax
// error in a mapped dependency is reported with gc's wording and stops the
// load with exactly gc's errors.
func TestSyntaxVerdictPackageMap(t *testing.T) {
	dep := gosource.PackageSpec{Path: "example.com/dep", Sources: []gosource.Source{
		{Name: "dep.go", Data: []byte("package dep\n\nfunc F(x bool) {\n\tif x {\n\t} else x = false\n}\n")},
	}}
	_, err := gosource.Load(
		[]gosource.Source{{Name: "main.go", Data: []byte("package main\n\nimport \"example.com/dep\"\n\nfunc main() { dep.F(true) }\n")}},
		gosource.Options{Packages: []gosource.PackageSpec{dep}},
	)
	if err == nil {
		t.Fatal("syntax error in mapped package accepted")
	}
	list, ok := err.(gosource.ErrorList)
	if !ok {
		t.Fatalf("diagnostics lost types: %T %v", err, err)
	}
	var got []string
	for _, e := range list {
		got = append(got, e.Error())
	}
	want := []string{"dep.go:5:9: syntax error: else must be followed by if or statement block"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapped-package diagnostics differ from gc\ngot: %q\nwant: %q", got, want)
	}
}
