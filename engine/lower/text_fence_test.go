//go:build full

package lower_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A built-in text row lowers: the body is embedded verbatim in the plan, the
// verb table is the export set (with its effect atoms) and the runtime is
// the row's lowered literal. A runner fence does not lower, and says so.
func TestLowerTextRowAndRunnerFence(t *testing.T) {
	polyglot.RegisterLanguage(polyglot.TextRow("fakecfg2", nil, polyglot.Text{Type: "fakecfg2", FileName: "fake.cfg", Tool: "fake-tool",
		Verbs: []polyglot.Verb{{Name: "show", Args: []string{"show", "{file}"}}, {Name: "apply", Args: []string{"apply", "{file}"}, Effects: []string{"world"}}}}))
	file := parse(t, "~~~fakecfg2 as cfg\nk = v\n~~~\nout := cfg.show()\necho \"$out\"\n", "text.bpp")
	result, err := lower.Compile(file, lower.Options{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	generated := string(result.Source)
	// The generated source is gofmt'd, so the literals carry gofmt's spacing.
	for _, want := range []string{
		`polyglot.Text{Type: "fakecfg2", FileName: "fake.cfg", Tool: "fake-tool", WorkDir: "", Shadow: []string(nil), Overlay: []string(nil), Verbs: []`,
		`{Name: "apply", Args: []string{"apply", "{file}"}, Tool: "", Env: []string(nil), Effects: []string{"world"}, Result: ""}`,
		`Source: "k = v\n"`,
		`Effects: []string{"world"}}`,
		`Variadic: true`,
		`Runner: ""`,
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated source lacks %q:\n%s", want, generated)
		}
	}
	// No alias: refused.
	if _, err := lower.Compile(parse(t, "~~~fakecfg2\nk = v\n~~~\n", "text.bpp"), lower.Options{}); err == nil || !strings.Contains(err.Error(), "needs an alias") {
		t.Errorf("unaliased text row: %v", err)
	}
	// A shell-function runner runs in the interpreter session a lowered
	// program does not carry: refused, naming the route.
	_, err = lower.Compile(parse(t, "r() { :; }\n~~~notes as n !r\nx\n~~~\n", "runner.bpp"), lower.Options{})
	if err == nil || !strings.Contains(err.Error(), "r is a shell function") || !strings.Contains(err.Error(), "func r(verb string, file string, args ...string) string") {
		t.Errorf("shell-function runner lowering: %v", err)
	}
}

// The runner shapes a lowered program cannot carry are refused at compile
// time by name, each with the route (a Bash# function runner lowers): a
// builtin or registered command, the interpreted-only rows, and a Bash#
// function that does not spell the runner contract.
func TestLowerRunnerFenceRefusals(t *testing.T) {
	row := polyglot.TextRow("fakedag", nil, polyglot.Text{Type: "fakedag", Verbs: []polyglot.Verb{{Name: "run"}}})
	row.InterpretedOnly = true
	polyglot.RegisterLanguage(row)
	for name, tc := range map[string]struct{ source, want string }{
		"builtin":    {"~~~notes as n !printf\nx\n~~~\n", "printf is not a Bash# function of this unit"},
		"undeclared": {"~~~notes as n !mytool\nx\n~~~\n", "wrap it in a Bash# function (func mytool(verb string, file string, args ...string) string)"},
		"no alias":   {"func r(verb string, file string, args ...string) string { return \"\" }\n~~~notes !r\nx\n~~~\n", "needs an alias"},
		"signature":  {"func r(verb string, file string) string { return \"\" }\n~~~notes as n !r\nx\n~~~\n", "does not have the runner signature func r(verb string, file string, args ...string) string"},
		"results":    {"func r(verb string, file string, args ...string) (string, error) { return \"\", nil }\n~~~notes as n !r\nx\n~~~\n", "does not have the runner signature"},
		"row":        {"~~~fakedag as ci\nx\n~~~\n", "processor is the running shell, which a lowered program does not carry"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := lower.Compile(parse(t, tc.source, "runner.bpp"), lower.Options{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got: %v", tc.want, err)
			}
		})
	}
}

// A runner that answers `methods` with a failure is a compile-time refusal
// carrying the runner's own diagnostic.
func TestLowerRunnerFenceMethodsFailure(t *testing.T) {
	source := `func r(verb string, file string, args ...string) string {
  if verb == "methods" { echo "r: no methods here" >&2; return "" }
  return ""
}
~~~notes as n !r
x
~~~
`
	_, err := lower.Compile(parse(t, source, "runner.bpp"), lower.Options{})
	if err == nil || !strings.Contains(err.Error(), "runner r declared no methods") || !strings.Contains(err.Error(), "r: no methods here") {
		t.Errorf("methods failure: %v", err)
	}
}

// The Plan of a lowered runner fence carries the exports the interpreter
// would prepare for the same source: the same declaration answers
// `methods`, through the same materialized file, whether it is declared
// before or after its fence.
func TestLowerRunnerFenceExportsMatchInterpreter(t *testing.T) {
	source := `~~~notes as n !r
hello
~~~
func r(verb string, file string, args ...string) string {
  if verb == "methods" {
    printf '%s\n' '{"name":"read"}' '{"name":"put","effects":["write"],"signature":{"params":["string"],"results":["string"]}}'
    return ""
  }
  return verb
}
`
	file := parse(t, source, "runner.bpp")
	result, err := lower.Compile(file, lower.Options{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	generated := string(result.Source)
	for _, want := range []string{
		`Runner: "r"`,
		`{Name: "read", Signature: __bpp0_polyglot.Signature{Params: []string{"string"}, Results: []string{"string"}, Dynamic: false, Variadic: true`,
		`{Name: "put", Signature: __bpp0_polyglot.Signature{Params: []string{"string"}, Results: []string{"string"}, Dynamic: false, Variadic: false`,
		`Effects: []string{"write"}`,
		`polyglot.RunnerFence{Type: "notes", Runner: "r", Invoke: __bpp0_foreignRunner0}`,
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated source lacks %q:\n%s", want, generated)
		}
	}
	// The interpreter prepares the same exports for the same unit.
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	probe := parse(t, source+"x := n.read()\necho \"$x\"\ny := n.put(\"v\")\necho \"$y\"\n", "runner.bpp")
	if err := runner.Run(context.Background(), probe); err != nil {
		t.Fatalf("interpreted: %v: %s", err, stderr.String())
	}
	if stdout.String() != "read\nput\n" {
		t.Fatalf("interpreted output %q", stdout.String())
	}
}

// A Bash# function runner lowers with the program: the same fence script
// interpreted and lowered gives byte-identical stdout and status, in the
// plain emitter mode (a runner of pure Bash# expressions) and under the
// execution runtime (a runner with shell statements, the shape every real
// runner takes). Covered: a runner that returns its value, one that prints
// it instead (its stdout, trailing newlines trimmed, is the value), the
// materialized file's contents reaching the runner, extra arguments, and a
// method whose body fails before its `return` (a returned value settles the
// status at 0, in both).
func TestRunnerFenceInterpretedNativeParity(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	for name, tc := range map[string]struct{ source, want string }{
		"plain": {`func r(verb string, file string, args ...string) string {
  if verb == "methods" { return "{\"name\":\"tag\"}" }
  return "tag:" + verb + ":" + file[len(file)-11:]
}
~~~notes as n !r
hello
~~~
x := n.tag()
echo "$x"
`, "tag:tag:fence.notes\n"},
		"execution": {`func r(verb string, file string, args ...string) string {
  if verb == "methods" {
    printf '%s\n' '{"name":"show"}' '{"name":"count","signature":{"params":["string","string"],"results":["string"]}}' '{"name":"fail"}' '{"name":"bad"}'
    return ""
  }
  if verb == "show" { cat "$file"; return "" }
  if verb == "count" { echo "count:${#args[@]}:${args[0]}:${args[1]}"; return "" }
  if verb == "fail" { echo "before failing"; return "value from a returning runner" }
  if verb == "bad" { false; return "" }
  return ""
}
~~~notes as n !r
hello from the fence
~~~
x := n.show()
echo "[$x]"
y := n.count("a", "b c")
echo "[$y]"
z := n.fail()
echo "[$z] status=$?"
w := n.bad()
echo "[$w] status=$?"
`, "[hello from the fence]\n[count:2:a:b c]\nbefore failing\n[value from a returning runner] status=0\n[] status=0\n"},
	} {
		t.Run(name, func(t *testing.T) {
			got := testPythonFenceInterpretedNativeParityAt(t, tc.source, filepath.Join("testdata", "runner.bpp"))
			if got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// An embed lowers like the inline fence it stands for: the file's bytes are
// the plan's Source, so the compiled program carries them.
func TestLowerEmbed(t *testing.T) {
	polyglot.RegisterLanguage(polyglot.TextRow("fakecfg3", nil, polyglot.Text{Type: "fakecfg3", FileName: "fake.cfg", Tool: "fake-tool",
		Verbs: []polyglot.Verb{{Name: "show", Args: []string{"show", "{file}"}}}}))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.cfg"), []byte("k = v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := parse(t, "embed fakecfg3 \"./app.cfg\" as cfg\nout := cfg.show()\necho \"$out\"\n", filepath.Join(dir, "embed.bpp"))
	result, err := lower.Compile(file, lower.Options{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !strings.Contains(string(result.Source), `Source: "k = v\n"`) {
		t.Errorf("generated source lacks the embedded bytes:\n%s", result.Source)
	}
}
