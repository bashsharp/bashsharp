package gosource_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// Sprint #219, Story #464: three parser/checker fidelity gaps between gc's
// front end (its own parser plus types2, checking the parser's recovered
// tree) and Load's (gc's parser for the verdict, go/types checking
// go/parser's tree). Each reproducer is outside the corpus and lists the
// diagnostics gc reports — `go tool compile -e` verbatim under the default
// policy, the parser's stream then the checker's rows under the
// checker-test policy — with negatives for the diagnostics a broader
// filter would lose: an independent error on the same line, and an unused
// or undefined identifier whose name also occurs elsewhere.

// TestSprint219PostDeclaration: gc's parser reports a short variable
// declaration in a for statement's post clause ("syntax error: cannot
// declare in post statement of for loop", at the ":="), and types2 relies
// on it. go/types reports the same condition itself, so under the
// checker-test policy the row appeared twice: gc's parser's and go/types'
// (at the statement, worded "cannot declare in post statement"). The
// checker's other rows at the same position and on the same line are its
// own and stay: the left-hand side both checkers evaluate ("undefined:
// a", "non-name a.b on left side of :="), the right-hand side's undefined
// name, and an unused variable declared in the loop body.
func TestSprint219PostDeclaration(t *testing.T) {
	src := "package p\nfunc f() {\n\tfor i := 0; i < 1; j := 0 { var unused int }\n\tfor i := 0; i < 1; a.b := 1 {}\n\tfor i := 0; i < 1; k := missing {}\n}\n"
	want := []string{
		"post.go:3:23: syntax error: cannot declare in post statement of for loop",
		"post.go:4:25: syntax error: cannot declare in post statement of for loop",
		"post.go:5:23: syntax error: cannot declare in post statement of for loop",
		"post.go:4:21: undefined: a",
		"post.go:4:21: non-name a.b on left side of :=",
		"post.go:4:21: undefined: a",
		"post.go:5:26: undefined: missing",
		"post.go:3:34: declared and not used: unused",
	}
	got := sprint165Diagnostics(t, "post.go", []byte(src), checkerTestPolicy)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checker-test policy\ngot: %q\nwant: %q", got, want)
	}
	// gc's stderr stops at the syntax error.
	if got := sprint165Diagnostics(t, "post.go", []byte(src), gosource.Options{}); !reflect.DeepEqual(got, want[:3]) {
		t.Fatalf("gc stderr\ngot: %q\nwant: %q", got, want[:3])
	}
	// A post statement that is not a declaration is checked as before.
	src = "package p\nfunc f() {\n\tvar j int\n\tfor i := 0; i < 1; j = missing {}\n\tfor i := 0; i < 1; i, j = 1, 2 { var unused int }\n}\n"
	want = []string{
		"post.go:3:6: declared and not used: j",
		"post.go:4:25: undefined: missing",
		"post.go:5:39: declared and not used: unused",
	}
	for _, options := range []gosource.Options{{}, checkerTestPolicy} {
		// The policies order checker diagnostics differently; this control
		// checks that every independent error survives.
		got := sprint165Diagnostics(t, "post.go", []byte(src), options)
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("assignment in post statement\ngot: %q\nwant: %q", got, want)
		}
	}
}

// TestSprint219TypeParameterConstraint: a type parameter list in which no
// parameter has a constraint. gc's parser reports "missing type
// constraint" and declares each name with a Bad constraint (parser.go
// paramList), which types2 ignores without a message; go/parser keeps the
// lone name as an unnamed field's constraint type, so go/types reported
// it undefined. The generic function without a body is both checkers'
// row; an undefined name in the body is go/types' own and stays, as is a
// list that go/parser already names (one parameter constrained).
func TestSprint219TypeParameterConstraint(t *testing.T) {
	src := "package p\nfunc T[P] () { var _ Q }\nfunc U[P, R] () {}\nfunc V[P any, Q] () {}\nfunc W[P] ()\nfunc X[P] () { var _ P }\n"
	want := []string{
		"tparam.go:2:9: syntax error: missing type constraint",
		"tparam.go:3:12: syntax error: missing type constraint",
		"tparam.go:4:16: syntax error: missing type constraint",
		"tparam.go:5:9: syntax error: missing type constraint",
		"tparam.go:6:9: syntax error: missing type constraint",
		"tparam.go:5:6: generic function is missing function body",
		"tparam.go:2:22: undefined: Q",
	}
	got := sprint165Diagnostics(t, "tparam.go", []byte(src), checkerTestPolicy)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checker-test policy\ngot: %q\nwant: %q", got, want)
	}
	if got := sprint165Diagnostics(t, "tparam.go", []byte(src), gosource.Options{}); !reflect.DeepEqual(got, want[:5]) {
		t.Fatalf("gc stderr\ngot: %q\nwant: %q", got, want[:5])
	}
	for _, name := range []string{"undefined: P", "undefined: R"} {
		if strings.Contains(strings.Join(got, "\n"), name) {
			t.Fatalf("type parameter reported undefined: %q", got)
		}
	}
}

// TestSprint219InvalidCharacter: an identifier written with a character
// that is not an identifier character. gc's scanner reports the character
// and keeps it in the identifier (☹x is one name, declared and referenced
// as such; neither checker looks up a name that is not a valid identifier),
// or, below utf8.RuneSelf, reports and skips it ($y declares y). go/scanner
// returned the character as an ILLEGAL token, after which go/parser's
// recovery lost the declaration around it and go/types reported the loss
// ("undefined: x" for `_ = ☹x`, "undefined: T" for the type declared after
// `func ☹m`), and lost the rows types2 reports on what remains. The
// expected list is `go tool compile -e` verbatim. The negatives are the
// rows a name-based filter would drop: `_ = ☹x` does not use x, whose
// unused declaration stays reported, as does y's, and an undefined name
// on the same line as an invalid character stays undefined.
func TestSprint219InvalidCharacter(t *testing.T) {
	src := "package p\nvar a☹b int\nvar _ = a☹b + missing\nfunc f() {\n\tx := 1\n\t_ = ☹x\n\t$y := 2\n}\nfunc g() { var unused int; a := 1; _ = a }\n"
	want := []string{
		"invalid.go:2:6: invalid character U+2639 '☹' in identifier",
		"invalid.go:3:10: invalid character U+2639 '☹' in identifier",
		"invalid.go:3:17: undefined: missing",
		"invalid.go:5:2: declared and not used: x",
		"invalid.go:6:6: invalid character U+2639 '☹' in identifier",
		"invalid.go:7:2: invalid character U+0024 '$'",
		"invalid.go:7:3: declared and not used: y",
		"invalid.go:9:16: declared and not used: unused",
	}
	got := sprint165Diagnostics(t, "invalid.go", []byte(src), gosource.Options{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gc stderr\ngot: %q\nwant: %q", got, want)
	}
	// The checker-test policy lists the parser's rows first, then the
	// checker's in its order (package-level declarations before bodies).
	want = []string{
		"invalid.go:2:6: invalid character U+2639 '☹' in identifier",
		"invalid.go:3:10: invalid character U+2639 '☹' in identifier",
		"invalid.go:6:6: invalid character U+2639 '☹' in identifier",
		"invalid.go:7:2: invalid character U+0024 '$'",
		"invalid.go:3:17: undefined: missing",
		"invalid.go:5:2: declared and not used: x",
		"invalid.go:7:3: declared and not used: y",
		"invalid.go:9:16: declared and not used: unused",
	}
	if got := sprint165Diagnostics(t, "invalid.go", []byte(src), checkerTestPolicy); !reflect.DeepEqual(got, want) {
		t.Fatalf("checker-test policy\ngot: %q\nwant: %q", got, want)
	}
	// The name is declared and referenced across files and through
	// selectors; a valid name that is undefined stays reported.
	sources := []gosource.Source{
		{Name: "a.go", Data: []byte("package p\n\nimport \"fmt\"\n\nvar (\n\t☹x int\n\t_ = ☹x\n\t_ = fmt.☹x\n\t_ = ☹fmt.Println\n\t_ = _世界\n\t_ = ☹_世界\n)\n\nfunc ☹m() {}\n\ntype T struct{}\n\nfunc (T) ☹m() {}\n")},
		{Name: "b.go", Data: []byte("package p\n\nfunc _() {\n\tvar x T\n\tx.☹m()\n\t☹m()\n\tvar y int\n}\n")},
	}
	want = []string{
		"a.go:6:2: invalid character U+2639 '☹' in identifier",
		"a.go:7:6: invalid character U+2639 '☹' in identifier",
		"a.go:8:10: invalid character U+2639 '☹' in identifier",
		"a.go:9:6: invalid character U+2639 '☹' in identifier",
		"a.go:10:6: undefined: _世界",
		"a.go:11:6: invalid character U+2639 '☹' in identifier",
		"a.go:14:6: invalid character U+2639 '☹' in identifier",
		"a.go:18:10: invalid character U+2639 '☹' in identifier",
		"b.go:5:4: invalid character U+2639 '☹' in identifier",
		"b.go:6:2: invalid character U+2639 '☹' in identifier",
		"b.go:7:6: declared and not used: y",
	}
	_, err := gosource.Load(sources, gosource.Options{})
	list, ok := err.(gosource.ErrorList)
	if !ok {
		t.Fatalf("diagnostics lost types: %T %v", err, err)
	}
	got = got[:0]
	for _, d := range list {
		got = append(got, d.Error())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("across files\ngot: %q\nwant: %q", got, want)
	}
	// A character inside a literal or a comment is not an identifier
	// character to either scanner and the program is accepted.
	if got := sprint165Diagnostics(t, "literal.go", []byte("package p\n\n// ☹ $\nvar _ = \"☹$\" + `$` + string('☹')\n"), gosource.Options{}); got != nil {
		t.Fatalf("literal rejected: %q", got)
	}
}
