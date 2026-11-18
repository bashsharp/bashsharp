// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A runner fence: the runner declares its methods, the alias exposes exactly
// those, an undeclared one is a prepare-time error, and PATH is never a
// runner.
func TestBashPPRunnerFenceShellFunction(t *testing.T) {
	dir := t.TempDir()
	src := `
tally() {
  case "$1" in
    methods) printf '%s\n' '{"name":"count"}' '{"name":"apply","effect":"world"}' ;;
    count) wc -l < "$2" | tr -d ' ' ;;
    apply) echo "applied:$3" ;;
  esac
}
~~~notes as n !tally
one
two
three
~~~
c := n.count()
echo "count=$c"
a := n.apply(prod)
echo "$a"
echo "file=${BASHPP_FENCE_FILE##*/} type=$BASHPP_FENCE_TYPE"
`
	stdout, stderr, err := runBashPPInDir(t, dir, src)
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr)
	}
	if stdout != "count=3\napplied:prod\nfile=fence.notes type=notes\n" {
		t.Fatalf("stdout = %q (stderr %q)", stdout, stderr)
	}
}

func TestBashPPRunnerFenceBashSharpFunction(t *testing.T) {
	dir := t.TempDir()
	src := `
func handle(verb string, file string, args ...string) string {
  if verb == "methods" {
    return "{\"name\":\"size\"}"
  }
  return "size-of:" + file[len(file)-9:]
}
~~~cfg as c !handle
k = v
~~~
s := c.size()
echo "$s"
`
	stdout, stderr, err := runBashPPInDir(t, dir, src)
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr)
	}
	if stdout != "size-of:fence.cfg\n" {
		t.Fatalf("stdout = %q (stderr %q)", stdout, stderr)
	}
}

func TestBashPPRunnerFenceRefusals(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct{ src, want string }{
		"undeclared method": {`
r() { [ "$1" = methods ] && echo '{"name":"a"}'; }
~~~t as x !r
~~~
x.b()
`, "x"},
		"no methods": {`
r() { :; }
~~~t as x !r
~~~
`, "declared no methods"},
		"malformed": {`
r() { echo not-json; }
~~~t as x !r
~~~
`, "not a method object"},
		"path is never a runner": {`
~~~t as x !ls
~~~
`, "PATH is never consulted"},
		"alias required": {`
r() { echo '{"name":"a"}'; }
~~~t !r
~~~
`, "needs an alias"},
		"unknown text type without runner": {`
~~~yaml as y
a: 1
~~~
`, `unsupported language "yaml"`},
	} {
		_, stderr, err := runBashPPInDir(t, dir, tc.src)
		if err == nil && !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: no error; stderr %q", name, stderr)
			continue
		}
		msg := stderr
		if err != nil {
			msg += err.Error()
		}
		if !strings.Contains(msg, tc.want) {
			t.Errorf("%s: %q does not mention %q", name, msg, tc.want)
		}
	}
}

// A built-in text row: a registered language whose runtime is polyglot.Text
// over a fake tool. The verb table is the export set and the tool receives
// the materialized artifact.
func TestBashPPTextRow(t *testing.T) {
	dir := t.TempDir()
	saved := polyglot.ToolResolver
	polyglot.ToolResolver = func(name string) ([]string, string, error) {
		if name == "fake-tool" {
			return []string{"/bin/sh", "-c", `printf '%s|%s|%s\n' "$0" "$(basename "$1")" "$(cat "$1" | tr -d '\n')"`}, "fake", nil
		}
		return nil, "", nil
	}
	polyglot.RegisterLanguage(polyglot.TextRow("fakecfg", []string{"fc"}, polyglot.Text{Type: "fakecfg", FileName: "fake.cfg", Tool: "fake-tool",
		Verbs: []polyglot.Verb{{Name: "show", Args: []string{"show", "{file}"}}, {Name: "apply", Args: []string{"apply", "{file}"}, Effects: []string{"world"}}}}))
	t.Cleanup(func() { polyglot.ToolResolver = saved })
	src := `
~~~fc as cfg
k=v
~~~
out := cfg.show()
echo "$out"
`
	stdout, stderr, err := runBashPPInDir(t, dir, src)
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr)
	}
	if stdout != "show|fake.cfg|k=v\n" {
		t.Fatalf("stdout = %q (stderr %q)", stdout, stderr)
	}
	_, stderr, err = runBashPPInDir(t, dir, "~~~fc as cfg\nk=v\n~~~\ncfg.destroy()\n")
	if err == nil && !strings.Contains(stderr, "cfg") {
		t.Fatalf("undeclared verb accepted: %q", stderr)
	}
	// The alias is required for a text row too.
	_, stderr, err = runBashPPInDir(t, dir, "~~~fc\nk=v\n~~~\n")
	if err == nil && !strings.Contains(stderr, "alias") {
		t.Fatalf("unaliased text row accepted: %q", stderr)
	}
	_ = context.Background
}

// The effect gate: a verb that declares effects is asked of the embedder's
// gate before it runs; a denial is the boundary status 126 with the gate's
// diagnostic, and a verb without effects never asks.
func TestBashPPForeignEffectGate(t *testing.T) {
	dir := t.TempDir()
	saved := ForeignEffectGate
	var asked []string
	ForeignEffectGate = func(_ context.Context, qualified string, effects []string) error {
		asked = append(asked, qualified+":"+strings.Join(effects, ","))
		for _, e := range effects {
			if e == "world" {
				return fmt.Errorf("%s: declared effects %s exceed the guard", qualified, strings.Join(effects, ","))
			}
		}
		return nil
	}
	t.Cleanup(func() { ForeignEffectGate = saved })
	src := `
r() {
  case "$1" in
    methods) printf '%s\n' '{"name":"plan","effects":["read"]}' '{"name":"apply","effects":["read","world"]}' '{"name":"peek"}' ;;
    *) echo "ran $1" ;;
  esac
}
~~~t as x !r
~~~
p := x.plan()
echo "$p"
k := x.peek()
echo "$k"
a := x.apply()
echo "status=$? a=[$a]"
`
	stdout, stderr, err := runBashPPInDir(t, dir, src)
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr)
	}
	if stdout != "ran plan\nran peek\nstatus=126 a=[]\n" {
		t.Fatalf("stdout = %q stderr = %q", stdout, stderr)
	}
	if !strings.Contains(stderr, "x.apply: declared effects read,world exceed the guard") {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Join(asked, " ") != "x.plan:read x.apply:read,world" {
		t.Fatalf("gate asked %v", asked)
	}
}

// An embed runs exactly like the same bytes written as an inline fence, the
// path relative to the script, not the cwd.
func TestBashPPEmbedRunnerFence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "list.notes"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := `
tally() {
  case "$1" in
    methods) echo '{"name":"count"}' ;;
    count) wc -l < "$2" | tr -d ' ' ;;
  esac
}
embed notes "./list.notes" as n !tally
c := n.count()
echo "count=$c"
`
	var stdout, stderr bytes.Buffer
	r, err := New(StdIO(nil, &stdout, &stderr), Dir(t.TempDir()), Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), filepath.Join(dir, "s.bsh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if stdout.String() != "count=3\n" {
		t.Fatalf("stdout = %q (stderr %q)", stdout.String(), stderr.String())
	}
}

func TestBashPPRunnerFenceAgentic(t *testing.T) {
	const fence = `
runner() {
 case "$1" in
 methods) echo '{"name":"run","agentic":true,"effect":"exec,net,spend"}' ;;
 run) echo CALLED ;;
 esac
}
~~~oracle as searcher !runner
existing binding
~~~
`
	for _, tc := range []struct {
		name, call string
		allowed    bool
	}{
		{"expression denied", "v := searcher.run()\necho \"$v\"", false},
		{"command denied", "searcher.run()", false},
		{"region", "agentic { v := searcher.run(); echo \"$v\"; }", true},
		{"marked function", "agentic func query() { v := searcher.run(); echo \"$v\"; }\nagentic { query(); }", true},
		{"unmarked function", "func query() { v := searcher.run(); echo \"$v\"; }\nagentic { query(); }", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, diagnostic, err := runBashPPInDir(t, t.TempDir(), fence+tc.call+"\n")
			if tc.allowed {
				if err != nil || out != "CALLED\n" {
					t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
				}
				return
			}
			if strings.Contains(out, "CALLED") || !strings.Contains(diagnostic, "agentic action requires") {
				t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
			}
		})
	}
}

func TestBashPPForeignDeclaredStringError(t *testing.T) {
	const language = "typed_oracle_fixture"
	polyglot.RegisterLanguage(polyglot.Language{Canonical: language, Text: true, NewRuntime: func(polyglot.RuntimeConfig) polyglot.LanguageRuntime {
		return polyglot.Embedded{RuntimeName: language,
			AnalyzeFunc: func(context.Context, string) ([]polyglot.Export, error) {
				return []polyglot.Export{{Name: "run", Agentic: true, Signature: polyglot.Signature{Params: []string{"string"}, Results: []string{"string", "error"}}}}, nil
			},
			CallFunc: func(_ context.Context, _ polyglot.Plan, _ string, args []any, _ map[string]any) (polyglot.CallResult, error) {
				if args[0] == "fail" {
					return polyglot.CallResult{Value: "partial"}, fmt.Errorf("raw failure")
				}
				return polyglot.CallResult{Value: "answer"}, nil
			}}
	}})
	out, diag, err := runBashPPInDir(t, t.TempDir(), `~~~typed_oracle_fixture as oracle
binding
~~~
agentic func query() {
 answer, err := oracle.run("success")
 if err != nil { echo unexpected; }
 echo "$answer"
 answer, err = oracle.run("fail")
 if err != nil { message := err.Error(); echo "$answer:$message"; }
}

agentic { query(); }
`)
	if err != nil || out != "answer\npartial:raw failure\n" || diag != "" {
		t.Fatalf("out=%q diag=%q err=%v", out, diag, err)
	}
}

func TestBashPPForeignDeclaredStringErrorCancellationIsFatal(t *testing.T) {
	const language = "typed_cancel_fixture"
	polyglot.RegisterLanguage(polyglot.Language{Canonical: language, Text: true, NewRuntime: func(polyglot.RuntimeConfig) polyglot.LanguageRuntime {
		return polyglot.Embedded{RuntimeName: language,
			AnalyzeFunc: func(context.Context, string) ([]polyglot.Export, error) {
				return []polyglot.Export{{Name: "run", Signature: polyglot.Signature{Params: []string{"string"}, Results: []string{"string", "error"}}}}, nil
			},
			CallFunc: func(_ context.Context, _ polyglot.Plan, _ string, args []any, _ map[string]any) (polyglot.CallResult, error) {
				if args[0] == "deadline" {
					return polyglot.CallResult{}, fmt.Errorf("worker: %w", context.DeadlineExceeded)
				}
				return polyglot.CallResult{}, fmt.Errorf("worker: %w", context.Canceled)
			}}
	}})
	for _, tc := range []struct {
		name string
		want error
	}{{"canceled", context.Canceled}, {"deadline", context.DeadlineExceeded}} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := runBashPPInDir(t, t.TempDir(), "~~~typed_cancel_fixture as f\nbinding\n~~~\nvalue, failure := f.run(\""+tc.name+"\")\necho unreachable\n")
			if out != "" || !errors.Is(err, tc.want) {
				t.Fatalf("out=%q err=%v, want fatal %v", out, err, tc.want)
			}
		})
	}
}

func TestBashPPLegacyErrorBindingKeepsRawBridgeFailureFatal(t *testing.T) {
	const language = "legacy_bridge_fixture"
	polyglot.RegisterLanguage(polyglot.Language{Canonical: language, Text: true, NewRuntime: func(polyglot.RuntimeConfig) polyglot.LanguageRuntime {
		return polyglot.Embedded{RuntimeName: language,
			AnalyzeFunc: func(context.Context, string) ([]polyglot.Export, error) {
				return []polyglot.Export{{Name: "run", Signature: polyglot.Signature{Results: []string{"string"}}}}, nil
			},
			CallFunc: func(context.Context, polyglot.Plan, string, []any, map[string]any) (polyglot.CallResult, error) {
				return polyglot.CallResult{}, fmt.Errorf("raw bridge failure")
			}}
	}})
	out, diag, err := runBashPPInDir(t, t.TempDir(), `~~~legacy_bridge_fixture as bridge
binding
~~~
value, failure := bridge.run()
`)
	if out != "" || !strings.Contains(diag, "raw bridge failure") || err == nil {
		t.Fatalf("out=%q diag=%q err=%v", out, diag, err)
	}
}
