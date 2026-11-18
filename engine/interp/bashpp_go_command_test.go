//go:build full

package interp_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPGoCommandAndTypedCallsStayDistinct(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	source := `~~~go as go
import ("fmt"; "os")
func Show() { fmt.Printf("argv=%q\n", os.Args[1:]) }
func Exit() { os.Exit(3) }
func Value() string { return "typed" }
~~~
go.Show one "two words"
go.Exit
echo "status=$?"
value := go.Value()
echo "value=$value"
`
	out, diagnostic, err := runPolyglot(t, source)
	if err != nil || diagnostic != "" || out != "argv=[\"one\" \"two words\"]\nstatus=3\nvalue=typed\n" {
		t.Fatalf("output=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
	if strings.Contains(out, "BASHPP") {
		t.Fatal("worker control leaked to guest stdout")
	}
}

func TestBashPPGoCommandPreservesCallerEnvironment(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	const source = `~~~go as go
import ("fmt"; "os")
func Show() { fmt.Printf("%q|%q|%q|%q\n", os.Getenv("BASHY_AGENT_MANIFEST"), os.Getenv("BASHY_MODEL_SESSION"), os.Getenv("BASHY_OUTPUT_PARENT"), os.Getenv("SHLVL")) }
func Order() { fmt.Printf("%q\n", os.Environ()) }
func Typed() string { return os.Getenv("BASHY_AGENT_MANIFEST") }
~~~
go.Order
go.Show
export BASHY_AGENT_MANIFEST=host-manifest
go.Show
export BASHY_AGENT_MANIFEST=caller-manifest
export BASHY_MODEL_SESSION=caller-session
export BASHY_OUTPUT_PARENT=
unset SHLVL
go.Show
value := go.Typed()
echo "typed=$value"
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "caller.bpp")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		caller []string
		order  string
		first  string
	}{
		"absent":               {[]string{}, "[]", `""|""|""|""`},
		"same_value_and_empty": {[]string{"SHLVL=9", "BASHY_AGENT_MANIFEST=host-manifest", "BASHY_MODEL_SESSION="}, `["SHLVL=9" "BASHY_AGENT_MANIFEST=host-manifest" "BASHY_MODEL_SESSION="]`, `"host-manifest"|""|""|"9"`},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			shellEnv := append(os.Environ(), "SHLVL=9", "BASHY_AGENT_MANIFEST=host-manifest", "BASHY_MODEL_SESSION=host-session", "BASHY_OUTPUT_PARENT=host-parent")
			runner, err := interp.New(interp.Lang(syntax.LangBashPP),
				interp.Env(expand.ListEnviron(shellEnv...)),
				interp.GoSourceEnv(tc.caller), interp.StdIO(nil, &stdout, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(context.Background(), file)
			want := tc.order + "\n" + tc.first + "\n\"host-manifest\"|" + strings.Split(tc.first, "|")[1] + "|" + strings.Split(tc.first, "|")[2] + "|" + strings.Split(tc.first, "|")[3] + "\n\"caller-manifest\"|\"caller-session\"|\"\"|\"\"\ntyped=" + os.Getenv("BASHY_AGENT_MANIFEST") + "\n"
			if err != nil || stderr.Len() != 0 || stdout.String() != want {
				t.Fatalf("output=%q diagnostic=%q err=%v, want %q", stdout.String(), stderr.String(), err, want)
			}
		})
	}
}

func TestBashPPGoCommandEnvironmentScopeRestoration(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	const source = `~~~go as go
import ("fmt"; "os")
func Show() { fmt.Printf("%q\n", os.Getenv("BASHY_AGENT_MANIFEST")) }
~~~
go.Show
BASHY_AGENT_MANIFEST=temporary go.Show
go.Show
export BASHY_AGENT_MANIFEST=authored
go.Show
BASHY_AGENT_MANIFEST=temporary-again go.Show
go.Show
export -n BASHY_AGENT_MANIFEST
go.Show
f() { local BASHY_AGENT_MANIFEST=local-value; export BASHY_AGENT_MANIFEST; go.Show; }
f
go.Show
(export BASHY_AGENT_MANIFEST=subshell; go.Show)
go.Show
{ export BASHY_AGENT_MANIFEST=background; go.Show; } &
wait
go.Show
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "scope.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	runner, err := interp.New(interp.Lang(syntax.LangBashPP),
		interp.Env(expand.ListEnviron(append(os.Environ(), "BASHY_AGENT_MANIFEST=startup-only")...)),
		interp.GoSourceEnv([]string{}), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), file)
	want := "\"\"\n\"temporary\"\n\"\"\n\"authored\"\n\"temporary-again\"\n\"authored\"\n\"\"\n\"local-value\"\n\"\"\n\"subshell\"\n\"\"\n\"background\"\n\"\"\n"
	if err != nil || stderr.Len() != 0 || stdout.String() != want {
		t.Fatalf("output=%q diagnostic=%q err=%v, want %q", stdout.String(), stderr.String(), err, want)
	}
}
