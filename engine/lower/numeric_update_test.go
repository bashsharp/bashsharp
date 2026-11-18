//go:build full

package lower

import (
	"bytes"
	"context"
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

// The referenced public sources. Both are accepted programs whose update is
// rejected at runtime, which is what makes them the contract for this helper:
// the diagnostic, the retained target and the statements after it all have to
// survive the lowering unchanged.
const numericFloatSource = "func main() {\n var value float64 = 1\n value /= 0\n printf ':immediate=%s' \"$value\"\n value += 2\n printf ':subsequent=%s' \"$value\"\n}\nmain()\n"

const numericIntegerSource = "func main() {\n var n int = 7\n n /= 0\n printf ':%s' \"$n\"\n}\nmain()\n"

// The behaviour artifact has no source counterpart because its operands are
// instrumented Go calls; it exists to observe the order and the arity of the
// evaluations the emitted statement performs.
const numericBehaviourSource = "type Counter uint8\nfunc main() {\n values := []int{10, 20}\n var counter Counter = 255\n var kept int = 7\n values[1] += 5\n counter += 1\n kept /= 0\n}\nmain()\n"

func numericUpdates(t *testing.T, path, source string) (*emitter, []*syntax.BashPPUpdate) {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), path)
	if err != nil {
		t.Fatal(err)
	}
	var updates []*syntax.BashPPUpdate
	syntax.Walk(file, func(node syntax.Node) bool {
		if update, ok := node.(*syntax.BashPPUpdate); ok {
			updates = append(updates, update)
		}
		return true
	})
	return &emitter{prefix: "nu_", options: Options{Origin: path}}, updates
}

// The emitted statement is asserted whole: its shape is the contract with the
// dispatcher core wires, and every part of it carries a requirement — the
// address is the first operand, the value is converted to the target's own
// type, the source operator is passed through unchanged, and the failure is
// reported without deciding whether the program continues.
func TestNumericUpdateEmittedStatement(t *testing.T) {
	e, updates := numericUpdates(t, "updates/float-divzero-neg.bpp", numericFloatSource)
	if len(updates) != 2 {
		t.Fatalf("got %d updates", len(updates))
	}
	got, err := e.numericUpdate(updates[0], "value", "0", "float64")
	if err != nil {
		t.Fatal(err)
	}
	want := `if nu_updateFailure := nu_rt.NumericUpdate(&(value), float64(0), "/=", nu_rt.ValueSite{File:"updates/float-divzero-neg.bpp", Name:"value", Line:3, Column:8, Offset:44}); nu_updateFailure != nil {nu_rt.Fail(nu_updateFailure); nu_rt.Status = nu_rt.ExitCode(nu_updateFailure)}`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if !e.bridge {
		t.Fatal("the runtime bridge was not requested")
	}
	// Inside a program the same failure is reported on the program instead of
	// the package status, and still leaves continuation to the caller.
	e.execution = true
	got, err = e.numericUpdate(updates[1], "value", "2", "float64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, ".Fail(nu_updateFailure)") || strings.Contains(got, "nu_rt.Status") {
		t.Fatalf("execution reporting: %s", got)
	}
	if !strings.Contains(got, `float64(2), "+=", `) {
		t.Fatalf("operator or converted value: %s", got)
	}
}

func TestNumericUpdateEmitterNegatives(t *testing.T) {
	e, updates := numericUpdates(t, "updates/int-divzero-neg.bpp", numericIntegerSource)
	update := updates[0]
	for _, tc := range []struct {
		name                    string
		node                    *syntax.BashPPUpdate
		target, rhs, targetType string
		wantCode                string
	}{
		{"absent statement", nil, "n", "0", "int", CodeExpr},
		{"absent operator", &syntax.BashPPUpdate{Target: update.Target, Value: update.Value}, "n", "0", "int", CodeExpr},
		{"absent target expression", &syntax.BashPPUpdate{Op: update.Op, Value: update.Value}, "n", "0", "int", CodeExpr},
		{"absent lowered target", update, "", "0", "int", CodeExpr},
		{"absent lowered value", update, "n", "", "int", CodeExpr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := e.numericUpdate(tc.node, tc.target, tc.rhs, tc.targetType)
			if err == nil {
				t.Fatalf("accepted: %s", text)
			}
			list, ok := err.(ErrorList)
			if !ok || len(list) != 1 || list[0].Code != tc.wantCode {
				t.Fatalf("got %v, want %s", err, tc.wantCode)
			}
		})
	}
	// An operator this helper does not implement is refused rather than
	// emitted as a call the runtime would reject only at run time.
	exponent := &syntax.BashPPUpdate{Target: update.Target, Op: &syntax.Lit{Value: "**="}, Value: update.Value, TargetWord: update.TargetWord, ValueWord: update.ValueWord}
	if _, err := e.numericUpdate(exponent, "n", "2", "int"); err == nil {
		t.Fatal("unsupported operator was emitted")
	} else if list, ok := err.(ErrorList); !ok || list[0].Code != CodeUnsupported {
		t.Fatalf("got %v", err)
	}
}

// A constant reaches the helper as the target's own type; anything already
// carrying a type, and any shift count, is passed through untouched.
func TestNumericUpdateValueTyping(t *testing.T) {
	e, updates := numericUpdates(t, "updates/float-divzero-neg.bpp", numericFloatSource)
	for _, tc := range []struct{ name, op, rhs, targetType, want string }{
		{"untyped constant", "+=", "2", "float64", "float64(2)"},
		{"named width", "+=", "1", "Counter", "Counter(1)"},
		{"unknown target type", "+=", "2", "", "2"},
		{"composite target type", "+=", "2", "[]int", "2"},
		{"shift count", "<<=", "2", "uint8", "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &syntax.BashPPUpdate{Target: updates[0].Target, Op: &syntax.Lit{Value: tc.op}, Value: updates[0].Value}
			if got := e.numericUpdateValue(node, tc.rhs, tc.targetType); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
	// A right-hand side that already has a type is never re-converted.
	call := &syntax.BashPPUpdate{Target: updates[0].Target, Op: &syntax.Lit{Value: "+="}, Value: &syntax.BashPPIdent{Name: &syntax.Lit{Value: "other"}}}
	if got := e.numericUpdateValue(call, "other", "float64"); got != "other" {
		t.Fatalf("typed operand was converted: %s", got)
	}
}

// TestNumericUpdateArtifactParity builds the Go the emitter produces, runs it,
// and requires the bytes, the status and the retained state to be the engine's
// for the same source.
func TestNumericUpdateArtifactParity(t *testing.T) {
	cases := []struct{ name, path, source, header, body string }{
		{name: "float_divzero", path: "updates/float-divzero-neg.bpp", source: numericFloatSource},
		{name: "int_divzero", path: "updates/int-divzero-neg.bpp", source: numericIntegerSource},
	}
	for i, tc := range cases {
		e, updates := numericUpdates(t, tc.path, tc.source)
		var statements []string
		switch tc.name {
		case "float_divzero":
			first, err := e.numericUpdate(updates[0], "value", "0", "float64")
			if err != nil {
				t.Fatal(err)
			}
			second, err := e.numericUpdate(updates[1], "value", "2", "float64")
			if err != nil {
				t.Fatal(err)
			}
			statements = []string{
				"var value float64 = 1",
				first,
				`_ = nu_rt.Printf(":immediate=%s", value)`,
				second,
				`_ = nu_rt.Printf(":subsequent=%s", value)`,
			}
		default:
			only, err := e.numericUpdate(updates[0], "n", "0", "int")
			if err != nil {
				t.Fatal(err)
			}
			statements = []string{"var n int = 7", only, `_ = nu_rt.Printf(":%s", n)`}
		}
		cases[i].body = strings.Join(statements, "\n")
	}
	dir := t.TempDir()
	numericModule(t, dir)
	for _, tc := range cases {
		numericPackage(t, dir, tc.name, "package main\n\nimport nu_rt \"github.com/bashsharp/bashsharp/engine/lower/shellrt\"\n\n"+tc.header+"func main() {\n"+tc.body+"\nnu_rt.Exit()\n}\n")
	}
	// The behaviour artifact observes what the emitted statement does to its
	// operands rather than what it reports.
	behaviour, updates := numericUpdates(t, "updates/behaviour.bpp", numericBehaviourSource)
	if len(updates) != 3 {
		t.Fatalf("got %d updates", len(updates))
	}
	element, err := behaviour.numericUpdate(updates[0], "values[index()]", "operand()", "int")
	if err != nil {
		t.Fatal(err)
	}
	named, err := behaviour.numericUpdate(updates[1], "counter", "1", "Counter")
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := behaviour.numericUpdate(updates[2], "kept", "divisor()", "int")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(named, "Counter(1)") {
		t.Fatalf("named width operand: %s", named)
	}
	numericPackage(t, dir, "behaviour", `package main

import nu_rt "github.com/bashsharp/bashsharp/engine/lower/shellrt"

type Counter uint8

var log string
var values = []int{10, 20}

func index() int    { log += "i"; return 1 }
func operand() int  { log += "o"; return 5 }
func divisor() int  { log += "d"; return 0 }

func main() {
	var counter Counter = 255
	var kept int = 7
`+element+"\n"+named+"\n"+rejected+`
	_ = nu_rt.Printf("order=%s values=%s,%s counter=%s kept=%s status=%s\n", log, values[0], values[1], counter, kept, nu_rt.Status)
	nu_rt.Exit()
}
`)
	binaries := numericBuild(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, status := numericRun(ctx, t, binaries, tc.name)
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.source), tc.path)
			if err != nil {
				t.Fatal(err)
			}
			var interpreted, diagnostic bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &interpreted, &diagnostic),
				interp.Env(expand.ListEnviron("PATH=/no-tools")), interp.WithBashCompatErrors(true))
			if err != nil {
				t.Fatal(err)
			}
			interpretedStatus := checkedTestStatus(runner.Run(ctx, file))
			if status != interpretedStatus || stdout != interpreted.String() || stderr != diagnostic.String() {
				t.Fatalf("artifact=(%d,%q,%q) interpreted=(%d,%q,%q)", status, stdout, stderr, interpretedStatus, interpreted.String(), diagnostic.String())
			}
			// The engine's own bytes, recorded so a silent agreement on the
			// wrong text cannot pass as parity.
			wantOut, wantErr := ":immediate=1:subsequent=3", tc.path+": line 3: BASHPP-EUPDATE-OP: BASHPP-EUPDATE-NONFINITE: runtime floating-point division by zero is unsupported by the scalar carrier\n"
			if tc.name == "int_divzero" {
				wantOut, wantErr = ":7", tc.path+": line 3: BASHPP-EUPDATE-OP: BASHPP-EEXPR-DIVZERO: division by zero\n"
			}
			if stdout != wantOut || stderr != wantErr || status != 0 {
				t.Fatalf("got (%d,%q,%q)", status, stdout, stderr)
			}
		})
	}
	t.Run("behaviour", func(t *testing.T) {
		stdout, stderr, status := numericRun(ctx, t, binaries, "behaviour")
		// The target is evaluated before the right-hand side and each exactly
		// once ("io"); a successful update commits; a named width wraps in its
		// own type; the rejected update keeps its target at 7 while the effect
		// its operand already had is retained ("d"); the failure recorded the
		// engine's status 2, and the statement after it still ran — which is
		// also why the process itself exits 0, the status of that last
		// statement rather than of the update.
		want := "order=iod values=10,25 counter=0 kept=7 status=2\n"
		if stdout != want || status != 0 {
			t.Fatalf("got (%d,%q)", status, stdout)
		}
		if stderr != "updates/behaviour.bpp: line 8: BASHPP-EUPDATE-OP: BASHPP-EEXPR-DIVZERO: division by zero\n" {
			t.Fatalf("diagnostic %q", stderr)
		}
	})
}

func numericModule(t *testing.T, dir string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	module := "module numericartifact\n\ngo 1.25\n\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + filepath.ToSlash(root) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
}

func numericPackage(t *testing.T, dir, name, source string) {
	t.Helper()
	packageDir := filepath.Join(dir, name)
	if err := os.Mkdir(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "main.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func numericBuild(t *testing.T, dir string) string {
	t.Helper()
	binaries := filepath.Join(dir, "bin")
	if err := os.Mkdir(binaries, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-mod=mod", "-o", binaries+string(os.PathSeparator), "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return binaries
}

func numericRun(ctx context.Context, t *testing.T, binaries, name string) (string, string, int) {
	t.Helper()
	command := exec.CommandContext(ctx, filepath.Join(binaries, name))
	command.Dir = t.TempDir()
	command.Env = []string{"PATH=/no-tools"}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	status := checkedTestStatus(command.Run())
	return stdout.String(), stderr.String(), status
}
