package gosource_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// TestSprint162ImportPathDiagnostics is an outside-corpus reproducer for the
// compiler's import-path rules (noder/import.go resolveImportPath and
// openPackage), applied by the importer before any package map or on-disk
// lookup, so go/types renders them as "could not import P (E)" exactly as
// types2 renders them for gc and keeps diagnosing the file's later imports.
// The expected list is `go tool compile -p diag reject.go` verbatim. Exact
// list equality is intentionally used: the negative checks cover a missing,
// extra, duplicate, wrong-line, wrong-wording and unexpected-success result,
// the same properties errorCheck enforces.
func TestSprint162ImportPathDiagnostics(t *testing.T) {
	base := filepath.Join("testdata", "sprint162", "diag", "import-path")
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	diagnostics := func(name string, data []byte) []string {
		t.Helper()
		_, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, gosource.Options{})
		if err == nil {
			return nil
		}
		list, ok := err.(gosource.ErrorList)
		if !ok {
			t.Fatalf("diagnostics lost types: %T", err)
		}
		out := make([]string, len(list))
		for i, diagnostic := range list {
			out[i] = diagnostic.Error()
		}
		return out
	}

	if got := diagnostics("positive.go", read("positive.go")); got != nil {
		t.Fatalf("positive control rejected: %q", got)
	}
	want := []string{
		`reject.go:3:10: could not import unicode//utf8 (non-canonical import path "unicode//utf8" (should be "unicode/utf8"))`,
		`reject.go:4:10: could not import /abs/b (import path cannot be absolute path)`,
		`reject.go:5:10: could not import main (cannot import "main")`,
		`reject.go:6:8: invalid import path (empty string)`,
		`reject.go:7:10: invalid import path (invalid character U+003A ':')`,
	}
	got := diagnostics("reject.go", read("reject.go.src"))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("errorCheck comparison mismatch\ngot: %q\nwant: %q", got, want)
	}

	negatives := map[string][]string{
		"missing":            got[1:],
		"extra":              append(append([]string{}, got...), "reject.go:8:1: extra"),
		"duplicate":          append(append([]string{}, got...), got[0]),
		"wrong-line":         append([]string{strings.Replace(got[0], ":3:", ":2:", 1)}, got[1:]...),
		"wrong-wording":      append([]string{strings.Replace(got[0], "non-canonical", "canonical", 1)}, got[1:]...),
		"importer-wording":   append([]string{`reject.go:3:10: could not import unicode//utf8 (go list failed)`}, got[1:]...),
		"unexpected-success": nil,
	}
	for name, candidate := range negatives {
		if reflect.DeepEqual(candidate, want) {
			t.Fatalf("negative %s was accepted", name)
		}
	}
	// gc exempts local names ("./x", "../x") from the canonical-form check
	// (noder/import.go islocalname); a relative import must reach the
	// relative-import rule, never a compiler path diagnostic.
	for _, local := range []string{"./b", "../b", "./b//c"} {
		src := []byte("package p\n\nimport _ \"" + local + "\"\n")
		for _, d := range diagnostics("local.go", src) {
			if strings.Contains(d, "non-canonical import path") || strings.Contains(d, "absolute path") {
				t.Fatalf("local import %q got a compiler path diagnostic: %q", local, d)
			}
		}
	}
}

// TestSprint162ScannerDiagnostics is an outside-corpus reproducer for gc's
// source-reader verdict on NUL bytes and invalid UTF-8 (syntax/source.go
// nextch): every occurrence is reported at its own position, under the
// caller's file name, with gc's one-equal-message-per-line filter. The
// expected list is `go tool compile -p p -e reject.go` verbatim; the
// positive control carries the same bytes inside escape sequences and valid
// multi-byte runes, which gc accepts. A run-time consumer that re-encodes
// the bytes (U+FFFD) before they reach Load is outside this verdict.
func TestSprint162ScannerDiagnostics(t *testing.T) {
	base := filepath.Join("testdata", "sprint162", "diag", "scanner")
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	diagnostics := func(name string, data []byte) []string {
		t.Helper()
		_, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, gosource.Options{})
		if err == nil {
			return nil
		}
		list, ok := err.(gosource.ErrorList)
		if !ok {
			t.Fatalf("diagnostics lost types: %T", err)
		}
		out := make([]string, len(list))
		for i, diagnostic := range list {
			out[i] = diagnostic.Error()
		}
		return out
	}

	if got := diagnostics("positive.go", read("positive.go")); got != nil {
		t.Fatalf("positive control rejected: %q", got)
	}
	want := []string{
		"reject.go:3:20: invalid NUL character",
		"reject.go:5:24: invalid NUL character",
		"reject.go:7:15: invalid NUL character",
		"reject.go:9:21: invalid UTF-8 encoding",
		"reject.go:11:21: invalid UTF-8 encoding",
		"reject.go:11:22: invalid NUL character",
		"reject.go:13:6: invalid UTF-8 encoding",
	}
	// The name is the caller's, not a working copy's: a runner attributes
	// the verdict by the name it passed.
	got := diagnostics("reject.go", read("reject.go.src"))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("errorCheck comparison mismatch\ngot: %q\nwant: %q", got, want)
	}
	negatives := map[string][]string{
		"missing":            got[1:],
		"extra":              append(append([]string{}, got...), "reject.go:14:1: extra"),
		"duplicate":          append(append([]string{}, got...), got[0]),
		"wrong-line":         append([]string{strings.Replace(got[0], ":3:", ":2:", 1)}, got[1:]...),
		"wrong-wording":      append([]string{strings.Replace(got[0], "invalid NUL character", "illegal character NUL", 1)}, got[1:]...),
		"working-copy-name":  append([]string{"tmp/reject.go:3:20: invalid NUL character"}, got[1:]...),
		"unexpected-success": nil,
	}
	for name, candidate := range negatives {
		if reflect.DeepEqual(candidate, want) {
			t.Fatalf("negative %s was accepted", name)
		}
	}
	// U+FFFD written as valid UTF-8 is not an invalid encoding: the bytes
	// that reach Load decide the verdict, so a re-encoded copy is accepted.
	reencoded := []byte(strings.ToValidUTF8(string(read("reject.go.src")), "�"))
	for _, d := range diagnostics("reencoded.go", reencoded) {
		if strings.Contains(d, "invalid UTF-8 encoding") {
			t.Fatalf("re-encoded source reported an invalid encoding: %q", d)
		}
	}
}

// TestSprint162CheckAfterParserDiagnostics is an outside-corpus reproducer
// for gc's rule on what follows its parser's verdict
// (cmd/compile/internal/noder/irgen.go checkFiles, base.SyntaxErrors): a
// "syntax error" ends compilation; after every other parser or scanner
// diagnostic gc type-checks its own tree and prints the checker's rows too,
// sorted by position, one of multiple equal messages per line. The expected
// lists are `go tool compile -p diag -e` verbatim. reject.go carries, in one
// file: a method with no receiver and one with two (gc's tree keeps a
// function and the first receiver — no repeated diagnostic), 3-index slices
// of a string with missing bounds (parser rows, then the checker's "3-index
// slice of string" on gc's kept node), a malformed literal (Bad in gc's
// tree — the checker is silent), two equal checker messages on one line
// (one printed), an unused label and a goto over a declaration (the
// parser's branch check reports them; the checker's copies are dropped as
// types2's IgnoreBranchErrors drops them).
func TestSprint162CheckAfterParserDiagnostics(t *testing.T) {
	base := filepath.Join("testdata", "sprint162", "diag", "continue")
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	diagnostics := func(name string, data []byte, options gosource.Options) []string {
		t.Helper()
		_, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, options)
		if err == nil {
			return nil
		}
		list, ok := err.(gosource.ErrorList)
		if !ok {
			t.Fatalf("diagnostics lost types: %T", err)
		}
		out := make([]string, len(list))
		for i, diagnostic := range list {
			out[i] = diagnostic.Error()
		}
		return out
	}

	if got := diagnostics("positive.go", read("positive.go"), gosource.Options{}); got != nil {
		t.Fatalf("positive control rejected: %q", got)
	}
	want := []string{
		"reject.go:3:9: method has no receiver",
		"reject.go:5:15: method has multiple receivers",
		"reject.go:11:13: middle index required in 3-index slice",
		"reject.go:11:14: final index required in 3-index slice",
		"reject.go:11:14: invalid operation: 3-index slice of string",
		"reject.go:13:15: final index required in 3-index slice",
		"reject.go:13:15: invalid operation: 3-index slice of string",
		"reject.go:15:13: hexadecimal literal has no digits",
		`reject.go:17:13: cannot use "a" + "b" (untyped string constant "ab") as int value in variable declaration`,
		`reject.go:19:16: cannot use "x" (untyped string constant) as int value in variable declaration`,
		"reject.go:22:1: label L defined and not used",
		"reject.go:26:7: goto N jumps over declaration of y at reject.go:27:6",
	}
	got := diagnostics("reject.go", read("reject.go.src"), gosource.Options{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("errorCheck comparison mismatch\ngot: %q\nwant: %q", got, want)
	}
	negatives := map[string][]string{
		"missing":            got[1:],
		"parser-only":        got[:2],
		"extra":              append(append([]string{}, got...), "reject.go:30:1: extra"),
		"duplicate":          append(append([]string{}, got...), got[9]),
		"repeated-receiver":  append(append([]string{}, got...), "reject.go:3:6: method has no receiver"),
		"bad-literal":        append(append([]string{}, got...), "reject.go:15:11: malformed constant: 0x"),
		"repeated-branch":    append(append([]string{}, got...), "reject.go:22:1: label L declared and not used"),
		"wrong-line":         append([]string{strings.Replace(got[0], ":3:", ":2:", 1)}, got[1:]...),
		"wrong-wording":      append([]string{strings.Replace(got[0], "no receiver", "no receivers", 1)}, got[1:]...),
		"unsorted":           append(append([]string{}, got[1:]...), got[0]),
		"unexpected-success": nil,
	}
	for name, candidate := range negatives {
		if reflect.DeepEqual(candidate, want) {
			t.Fatalf("negative %s was accepted", name)
		}
	}
	// A syntax error is the complete result: the parser's other rows before
	// it are kept, the checker never runs (line 7 is a checker error).
	wantSyntax := []string{
		"syntax.go:3:13: middle index required in 3-index slice",
		"syntax.go:3:14: final index required in 3-index slice",
		"syntax.go:5:13: syntax error: unexpected ), expected expression",
	}
	if got := diagnostics("syntax.go", read("syntax.go.src"), gosource.Options{}); !reflect.DeepEqual(got, wantSyntax) {
		t.Fatalf("syntax error did not end the verdict\ngot: %q\nwant: %q", got, wantSyntax)
	}
	// The checker-test policy: gc's parser rows (its branch check left to
	// go/types) first, every one of them, then go/types' complete output on
	// gc's tree — both equal messages on line 19 and go/types' own branch
	// wording, and no repeat of a parser row (Sprint 165: the check_test
	// runners' checkers run on their own parser's recovered tree and never
	// repeat it; TestSprint165RepeatedParserDiagnostic).
	policy := diagnostics("reject.go", read("reject.go.src"), gosource.Options{CheckAfterSyntaxErrors: true, CheckerBranchErrors: true})
	parserRows := []string{want[0], want[1], want[2], want[3], want[5], want[7]}
	if len(policy) < len(parserRows) || !reflect.DeepEqual(policy[:len(parserRows)], parserRows) {
		t.Fatalf("checker-test policy changed: %q", policy)
	}
	for _, row := range []string{
		`reject.go:19:21: cannot use "x" (untyped string constant) as int value in variable declaration`,
		"reject.go:22:1: label L declared and not used",
	} {
		if !slices.Contains(policy, row) {
			t.Fatalf("checker-test policy lost go/types' own row %q in %q", row, policy)
		}
	}
	for _, row := range []string{
		"reject.go:3:6: method has no receiver",
		"reject.go:15:11: malformed constant: 0x",
	} {
		if slices.Contains(policy, row) {
			t.Fatalf("checker-test policy repeated the parser's row %q in %q", row, policy)
		}
	}
}

// TestSprint162VersionErrorCause is an outside-corpus reproducer for gc's
// rendering of a language version error (noder/irgen.go checkFiles
// conf.Error): "requires goX.Y or later" names its cause — the file's
// //go:build version when that set the file's version, otherwise the -lang
// setting. The expected lists are `go tool compile -lang=… -e` verbatim.
func TestSprint162VersionErrorCause(t *testing.T) {
	base := filepath.Join("testdata", "sprint162", "diag", "version")
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	diagnostics := func(name string, data []byte, options gosource.Options) []string {
		t.Helper()
		_, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, options)
		if err == nil {
			return nil
		}
		list, ok := err.(gosource.ErrorList)
		if !ok {
			t.Fatalf("diagnostics lost types: %T", err)
		}
		out := make([]string, len(list))
		for i, diagnostic := range list {
			out[i] = diagnostic.Error()
		}
		return out
	}

	if got := diagnostics("positive.go", read("positive.go"), gosource.Options{GoVersion: "go1.22"}); got != nil {
		t.Fatalf("positive control rejected: %q", got)
	}
	wantBuild := []string{"build.go:6:12: cannot range over 10 (untyped int constant): requires go1.22 or later (file declares //go:build go1.21)"}
	if got := diagnostics("build.go", read("build.go.src"), gosource.Options{GoVersion: "go1.22"}); !reflect.DeepEqual(got, wantBuild) {
		t.Fatalf("//go:build cause\ngot: %q\nwant: %q", got, wantBuild)
	}
	wantLang := []string{
		"lang.go:4:12: cannot range over 10 (untyped int constant): requires go1.22 or later (-lang was set to go1.21; check go.mod)",
		"lang.go:6:12: cannot range over 10 (untyped int constant): requires go1.22 or later (-lang was set to go1.21; check go.mod)",
	}
	got := diagnostics("lang.go", read("lang.go.src"), gosource.Options{GoVersion: "go1.21"})
	if !reflect.DeepEqual(got, wantLang) {
		t.Fatalf("-lang cause\ngot: %q\nwant: %q", got, wantLang)
	}
	negatives := map[string][]string{
		"missing":            got[1:],
		"undecorated":        {strings.TrimSuffix(got[0], " (-lang was set to go1.21; check go.mod)"), got[1]},
		"wrong-cause":        {strings.Replace(got[0], "-lang was set to go1.21; check go.mod", "file declares //go:build go1.21", 1), got[1]},
		"wrong-line":         {strings.Replace(got[0], ":4:", ":3:", 1), got[1]},
		"unexpected-success": nil,
	}
	for name, candidate := range negatives {
		if reflect.DeepEqual(candidate, wantLang) {
			t.Fatalf("negative %s was accepted", name)
		}
	}
	// The checker-test policy reports go/types' message undecorated.
	policy := diagnostics("lang.go", read("lang.go.src"), gosource.Options{GoVersion: "go1.21", CheckAfterSyntaxErrors: true, CheckerBranchErrors: true})
	if want := []string{negatives["undecorated"][0], strings.TrimSuffix(got[1], " (-lang was set to go1.21; check go.mod)")}; !reflect.DeepEqual(policy, want) {
		t.Fatalf("checker-test policy changed\ngot: %q\nwant: %q", policy, want)
	}
}
