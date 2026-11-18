//go:build full

package interp

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPImportRegistryContract(t *testing.T) {
	eval := &recordingBashPPEval{resolved: map[string]string{"fmt": "fmt", "log": "log"}}
	r := newInjectedBashPPRunner(t, eval)
	run := func(src string) error { return r.Run(context.Background(), parseBashPPInternal(t, src)) }
	if err := run("import \"fmt\"\nimport \"fmt\"\n"); err != nil {
		t.Fatal(err)
	}
	if got := r.bashPPImports; len(got) != 1 || got["fmt"] != "fmt" {
		t.Fatalf("imports %#v", got)
	}
	before := maps.Clone(r.bashPPImports)
	if err := run("import fmt \"log\"\n"); err == nil || !strings.Contains(err.Error(), "import name fmt") {
		t.Fatalf("alias collision: %v", err)
	}
	if !maps.Equal(before, r.bashPPImports) {
		t.Fatalf("alias collision mutated registry: %#v", r.bashPPImports)
	}
	if err := run("import f \"fmt\"\n"); err == nil || !strings.Contains(err.Error(), "import path") {
		t.Fatalf("path collision: %v", err)
	}
	if !maps.Equal(before, r.bashPPImports) {
		t.Fatalf("path collision mutated registry: %#v", r.bashPPImports)
	}
	r.Reset()
	if len(r.bashPPImports) != 0 {
		t.Fatalf("Reset retained imports: %#v", r.bashPPImports)
	}
	if r.bashPPTools.goBinary != "/reviewed/go" || r.bashPPTools.eval != eval {
		t.Fatal("Reset lost injected toolchain identity")
	}
}

func TestBashPPImportedValuesValidateBeforeObjectCreation(t *testing.T) {
	eval := &recordingBashPPEval{
		resolved: map[string]string{"bad": "bad"},
		values:   []any{func() {}},
	}
	r := newInjectedBashPPRunner(t, eval)
	err := r.Run(context.Background(), parseBashPPInternal(t, "import \"bad\"\nobj := bad.Value()\n"))
	if !errors.Is(err, ExitStatus(2)) {
		t.Fatalf("Run error = %v, want exit status 2", err)
	}
	if got, ok := r.Object("obj"); ok {
		t.Fatalf("invalid imported value created object: %#v", got)
	}
}

func TestBashPPInteractiveImportIsActuallyIdempotent(t *testing.T) {
	eval := &recordingBashPPEval{resolved: map[string]string{"fmt": "fmt"}}
	r := newInjectedBashPPRunner(t, eval)
	// Interactive clients parse and Run one statement at a time. Exercise
	// separate syntax trees and Run calls, not two imports in one file.
	for range 2 {
		if err := r.Run(context.Background(), parseBashPPInternal(t, "import \"fmt\"\n")); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.bashPPImports; len(got) != 1 || got["fmt"] != "fmt" {
		t.Fatalf("imports %#v", got)
	}
	if err := r.Run(context.Background(), parseBashPPInternal(t, "fmt.Println(\"still-live\")\n")); err != nil {
		t.Fatal(err)
	}
	eval.mu.Lock()
	defer eval.mu.Unlock()
	if len(eval.calls) != 1 || eval.calls[0].Imports["fmt"] != "fmt" {
		t.Fatalf("calls %#v", eval.calls)
	}
}

func TestBashPPLiveEnableInitializesEvaluatorAndNamespace(t *testing.T) {
	r, err := New(StdIO(nil, io.Discard, io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	// The input is parsed as an interactive Bash++ client would parse it;
	// execution starts in Classic Bash and activates the dialect live.
	if err := r.Run(context.Background(), parseBashPPInternal(t, "set -o bashpp\n")); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.bashPPTools.eval.(*policyBashPPEvaluator); !ok {
		t.Fatalf("live enable installed %T, want policy evaluator", r.bashPPTools.eval)
	}
	eval := &recordingBashPPEval{resolved: map[string]string{"fmt": "fmt"}}
	r.bashPPTools = bashPPToolchain{goBinary: "/reviewed/go", eval: eval}
	if err := r.Run(context.Background(), parseBashPPInternal(t, "import \"fmt\"\nfmt.Println(\"live\")\n")); err != nil {
		t.Fatal(err)
	}
	if r.bashPPScope == nil || r.bashPPImports["fmt"] != "fmt" || len(eval.calls) != 1 {
		t.Fatalf("live state scope=%p imports=%#v calls=%d", r.bashPPScope, r.bashPPImports, len(eval.calls))
	}
}

func TestBashPPSubshellImportLifecycleThroughRunner(t *testing.T) {
	eval := &recordingBashPPEval{resolved: map[string]string{"fmt": "fmt", "log": "log"}}
	r := newInjectedBashPPRunner(t, eval)
	if err := r.Run(context.Background(), parseBashPPInternal(t,
		"import \"fmt\"\n(\nimport \"log\"\nfmt.Println(\"child\")\n)\nfmt.Println(\"parent\")\n")); err != nil {
		t.Fatal(err)
	}
	if len(r.bashPPImports) != 1 || r.bashPPImports["fmt"] != "fmt" {
		t.Fatalf("child namespace leaked: %#v", r.bashPPImports)
	}
	eval.mu.Lock()
	defer eval.mu.Unlock()
	if len(eval.calls) != 2 || eval.calls[0].Imports["log"] != "log" || eval.calls[1].Imports["log"] != "" {
		t.Fatalf("subshell call namespaces %#v", eval.calls)
	}
}

func TestBashPPGroupedImportIsAtomicAndUsesGoAliases(t *testing.T) {
	eval := &recordingBashPPEval{resolved: map[string]string{"fmt": "fmt", "log": "log", "embed": "embed"}}
	r := newInjectedBashPPRunner(t, eval)
	run := func(src string) error { return r.Run(context.Background(), parseBashPPInternal(t, src)) }
	if err := run("import (\n f \"fmt\"\n _ \"embed\"\n . \"log\"\n)\n"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"f": "fmt", "_:embed": "embed", ".:log": "log"}
	if !maps.Equal(r.bashPPImports, want) {
		t.Fatalf("imports %#v, want %#v", r.bashPPImports, want)
	}
	before := maps.Clone(r.bashPPImports)
	if err := run("import (\n j \"encoding/json\"\n f \"log\"\n)\n"); err == nil {
		t.Fatal("expected grouped alias collision")
	}
	if !maps.Equal(r.bashPPImports, before) {
		t.Fatalf("failed group mutated registry: %#v", r.bashPPImports)
	}
	for _, src := range []string{
		"import (\n . \"fmt\"\n . \"fmt\"\n)\n",
		"import (\n _ \"embed\"\n _ \"embed\"\n)\n",
		"import (\n f \"fmt\"\n other \"fmt\"\n)\n",
	} {
		duplicateRunner := newInjectedBashPPRunner(t, eval)
		err := duplicateRunner.Run(context.Background(), parseBashPPInternal(t, src))
		if err == nil || !strings.Contains(err.Error(), "duplicate import path") {
			t.Fatalf("duplicate group %q: %v", src, err)
		}
		if len(duplicateRunner.bashPPImports) != 0 {
			t.Fatalf("duplicate group mutated registry: %#v", duplicateRunner.bashPPImports)
		}
	}
	r.Reset()
	if len(r.bashPPImports) != 0 {
		t.Fatalf("Reset retained group: %#v", r.bashPPImports)
	}
}

func TestBashPPImportUsesInterpretedPath(t *testing.T) {
	eval := &recordingBashPPEval{resolved: map[string]string{"fmt": "fmt"}}
	r := newInjectedBashPPRunner(t, eval)
	if err := r.Run(context.Background(), parseBashPPInternal(t, `import "\x66mt"`)); err != nil {
		t.Fatal(err)
	}
	if got := r.bashPPImports["fmt"]; got != "fmt" {
		t.Fatalf("decoded path = %q", got)
	}
}

func TestBashPPImportSubshellIsolationAndCancellation(t *testing.T) {
	eval := &recordingBashPPEval{resolved: map[string]string{"fmt": "fmt"}}
	r := newInjectedBashPPRunner(t, eval)
	if err := r.Run(context.Background(), parseBashPPInternal(t, "import \"fmt\"\n")); err != nil {
		t.Fatal(err)
	}
	child := r.subshell(false)
	child.bashPPImports["log"] = "log"
	if _, ok := r.bashPPImports["log"]; ok {
		t.Fatal("subshell import leaked to parent session")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.Run(ctx, parseBashPPInternal(t, "fmt.Println(\"no\")\n"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestBashPPImportSubshellsRunWithoutSharedRegistry(t *testing.T) {
	eval := &recordingBashPPEval{resolved: map[string]string{"fmt": "fmt"}}
	parent := newInjectedBashPPRunner(t, eval)
	if err := parent.Run(context.Background(), parseBashPPInternal(t, "import \"fmt\"\n")); err != nil {
		t.Fatal(err)
	}
	children := make([]*Runner, 8)
	for i := range children {
		children[i] = parent.subshell(false)
	}
	call := parseBashPPInternal(t, "fmt.Println(\"x\")\n")
	var wg sync.WaitGroup
	for _, child := range children {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := child.Run(context.Background(), call); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(parent.bashPPImports) != 1 {
		t.Fatalf("parent registry mutated: %#v", parent.bashPPImports)
	}
}

func TestNativeBashPPEvaluatorRejectsArgumentInjection(t *testing.T) {
	e := nativeBashPPEvaluator{}
	err := e.Call(context.Background(), bashPPEvalRequest{
		Go: "/must/not/run", Imports: map[string]string{"fmt": "fmt"},
		Selector: []string{"fmt", "Println"}, Args: []string{`"ok"); panic("injected"`},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid Go argument") {
		t.Fatalf("got %v", err)
	}
}

func TestNativeBashPPEvaluatorImportsPackagesUsedByArguments(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Go program")
	}
	dir := t.TempDir()
	var stdout, stderr strings.Builder
	req := nativeResolveRequest(dir, "GOWORK=off")
	req.Stdout, req.Stderr = &stdout, &stderr
	req.Imports = map[string]string{"fmt": "fmt", "os": "os"}
	req.Selector = []string{"fmt", "Fprintln"}
	req.Args = []string{"os.Stdout", `"argument-import"`}
	if err := (nativeBashPPEvaluator{}).Call(context.Background(), req); err != nil {
		t.Fatalf("call: %v: %s", err, stderr.String())
	}
	if stdout.String() != "argument-import\n" || stderr.String() != "" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestBashPPGoIdentityIgnoresPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got, err := bashPPGoIdentity()
	if err != nil {
		t.Fatal(err)
	}
	goName := "go"
	if runtime.GOOS == "windows" {
		goName += ".exe"
	}
	want, err := filepath.EvalSymlinks(filepath.Join(got.Root, "bin", goName))
	if err != nil {
		t.Fatal(err)
	}
	want, _ = filepath.Abs(want)
	if got.Binary != want {
		t.Fatalf("Go identity %q, want %q", got.Binary, want)
	}
	if got.Version != bashPPReviewedGoVersion || got.GOOS != runtime.GOOS || got.GOARCH != runtime.GOARCH {
		t.Fatalf("Go identity = %#v", got)
	}
}

func TestBashPPGoIdentityWithoutRuntimeGOROOT(t *testing.T) {
	const childEnv = "BASHPP_TEST_EMPTY_RUNTIME_GOROOT"
	if os.Getenv(childEnv) == "1" {
		if got := runtime.GOROOT(); got != "" {
			t.Fatalf("runtime.GOROOT() = %q, want empty", got)
		}
		if pathGo, err := exec.LookPath("go"); err == nil {
			t.Fatalf("scrubbed PATH resolved go as %q", pathGo)
		}
		var stdout, stderr strings.Builder
		r, err := New(StdIO(nil, &stdout, &stderr), Lang(syntax.LangBashPP))
		if err != nil {
			t.Fatal(err)
		}
		err = r.Run(context.Background(), parseBashPPInternal(t, `import "net/url"
endpoint, _ := url.Parse("https://example.test/original")
readonly endpoint
endpoint.Host = "changed.test"
`))
		if stdout.String() != "" {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
		const wantStderr = "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"endpoint\" through field .Host\n"
		if stderr.String() != wantStderr {
			t.Fatalf("stderr = %q, want %q", stderr.String(), wantStderr)
		}
		if !errors.Is(err, ExitStatus(2)) {
			t.Fatalf("error = %v, want exit status 2", err)
		}
		return
	}

	if testing.Short() {
		t.Skip("rebuilds the interp test binary with -trimpath")
	}
	if _, err := bashPPGoIdentity(); err != nil {
		t.Fatalf("prepare reviewed Go toolchain: %v", err)
	}
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	_, goBinary, err := bashPPGoBootstrap(name)
	if err != nil {
		t.Fatal(err)
	}
	testBinary := filepath.Join(t.TempDir(), "interp.test")
	if runtime.GOOS == "windows" {
		testBinary += ".exe"
	}
	build := exec.Command(goBinary, "test", "-c", "-trimpath", "-o", testBinary, "github.com/bashsharp/bashsharp/engine/interp")
	build.Env = append(bashPPEnvWithout(os.Environ(), "GOROOT", "GOTOOLCHAIN"), "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build trimpath child: %v\n%s", err, out)
	}
	cmd := exec.Command(testBinary, "-test.run=^TestBashPPGoIdentityWithoutRuntimeGOROOT$")
	cmd.Env = append(bashPPEnvWithout(os.Environ(), "GOROOT", "PATH", "GOTOOLCHAIN", "GOSH_PROG", "GOSH_CMD"),
		childEnv+"=1", "CGO_ENABLED=0", "GOTOOLCHAIN=local", "PATH="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("trimpath child: %v\n%s", err, out)
	}
}

func bashPPEnvWithout(env []string, names ...string) []string {
	remove := make(map[string]bool, len(names))
	for _, name := range names {
		remove[name] = true
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if !remove[name] {
			out = append(out, entry)
		}
	}
	return out
}

func TestBashPPGoIdentityMutationRejection(t *testing.T) {
	got, err := bashPPGoIdentity()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*bashPPGoIdentityInfo)
		match  string
	}{
		{"version", func(v *bashPPGoIdentityInfo) { v.Version = "go1.27.0" }, "not reviewed"},
		{"hash", func(v *bashPPGoIdentityInfo) { v.SHA256 = "0" + v.SHA256[1:] }, "checksum"},
		{"path", func(v *bashPPGoIdentityInfo) { v.Binary += "x" }, "binary path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutant := got
			test.mutate(&mutant)
			if err := validateBashPPGoIdentity(mutant, bashPPGoReviews); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("mutation accepted: %v", err)
			}
		})
	}
}

func TestBashPPEvalRequestPinsReviewedGoEnvironment(t *testing.T) {
	r, err := New(Env(expand.ListEnviron("GOROOT=/wrong", "GOTOOLCHAIN=go1.26.5")), Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	r.bashPPTools = bashPPToolchain{goBinary: "/reviewed/go", goRoot: "/reviewed", goVersion: bashPPReviewedGoVersion, eval: &recordingBashPPEval{}}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"GOROOT=/reviewed": false, "GOTOOLCHAIN=" + bashPPReviewedGoVersion: false}
	for _, entry := range req.Env {
		if _, ok := want[entry]; ok {
			want[entry] = true
		}
		if entry == "GOROOT=/wrong" || entry == "GOTOOLCHAIN=go1.26.5" {
			t.Fatalf("unreviewed environment retained: %q", entry)
		}
	}
	for entry, seen := range want {
		if !seen {
			t.Errorf("missing %q", entry)
		}
	}
}

func TestNativeBashPPSelectorExitStatusIsExact(t *testing.T) {
	r := newInjectedBashPPRunner(t, nativeBashPPEvaluator{})
	r.bashPPTools.goBinary = ""
	err := r.Run(context.Background(), parseBashPPInternal(t, "import \"os\"\nos.Exit(7)\n"))
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("error/status = %T %v", err, err)
	}
}
