// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestBashPPPolicyFailsClosed pins rule 1. The zero value of the capability
// type must refuse, so a class someone adds later and forgets to wire into
// bashPPPolicyFor declines to run rather than reaching the toolchain.
func TestBashPPPolicyFailsClosed(t *testing.T) {
	if got := bashPPPolicyFor(capUnknown); got != policyRefuse {
		t.Fatalf("capUnknown policy = %v, want policyRefuse", got)
	}
	if capUnknown != 0 {
		t.Fatalf("capUnknown = %d, want 0: the zero value must be the refusing class", capUnknown)
	}
	if policyRefuse != 0 {
		t.Fatalf("policyRefuse = %d, want 0: the zero value must be the refusing policy", policyRefuse)
	}
	// An out-of-range class, standing in for one added without a table row.
	if got := bashPPPolicyFor(bashPPCapability(1 << 20)); got != policyRefuse {
		t.Fatalf("unlisted capability policy = %v, want policyRefuse", got)
	}
}

// TestBashPPCapabilityPolicyTable is the executable form of the P2 capability
// decision. Each row is a class the design required a decision for; changing a
// row here is changing the recorded policy.
func TestBashPPCapabilityPolicyTable(t *testing.T) {
	for _, test := range []struct {
		capability bashPPCapability
		want       bashPPPolicy
		name       string
	}{
		{capReviewedStdlib, policyToolchain, "reviewed pure-Go stdlib"},
		{capExternalPureGo, policyToolchain, "pure-Go local module/workspace/vendor/GOPATH"},
		{capCgo, policyRefuse, "cgo-required"},
		{capCompiledOnly, policyRefuse, "compiled/export-only"},
		{capNotBuildable, policyRefuse, "package main/no buildable files/platform mismatch"},
		{capUnreviewedStdlib, policyRefuse, "stdlib outside the reviewed inventory"},
		{capMissing, policyRefuse, "unresolvable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := bashPPPolicyFor(test.capability); got != test.want {
				t.Fatalf("policy = %v, want %v", got, test.want)
			}
			if test.want == policyRefuse && test.capability != capUnknown {
				if r := test.capability.refusal(); r == "" || strings.HasPrefix(r, "package is not supported by any") {
					t.Fatalf("refused class has no specific explanation: %q", r)
				}
			}
		})
	}
}

// TestBashPPClassification pins the go-list-facts to capability mapping. These
// are pure-function cases: no toolchain, module, or network is involved, so
// the recorded policy stays testable on any host.
func TestBashPPClassification(t *testing.T) {
	for _, test := range []struct {
		name  string
		path  string
		facts bashPPPackageFacts
		want  bashPPCapability
	}{
		{"reviewed stdlib", "fmt",
			bashPPPackageFacts{Name: "fmt", Standard: true, Dir: "/goroot/src/fmt", GoFiles: []string{"print.go"}},
			capReviewedStdlib},
		{"local module", "example.com/m/greet",
			bashPPPackageFacts{Name: "greet", Dir: "/w/greet", GoFiles: []string{"greet.go"}},
			capExternalPureGo},
		{"cgo required", "example.com/m/cgopkg",
			bashPPPackageFacts{Name: "cgopkg", Dir: "/w/cgopkg", GoFiles: []string{"a.go"}, CgoFiles: []string{"c.go"}},
			capCgo},
		// Go itself refuses to import package main. The measured Yaegi
		// prototype accepted it; this row is why that mattered.
		{"package main", "example.com/m/cmd",
			bashPPPackageFacts{Name: "main", Dir: "/w/cmd", GoFiles: []string{"main.go"}},
			capNotBuildable},
		{"platform mismatch", "example.com/m/plan9only",
			bashPPPackageFacts{Name: "plan9only", Dir: "/w/p9", IgnoredGoFiles: []string{"p_plan9.go"}},
			capNotBuildable},
		{"compiled only", "example.com/m/binonly",
			bashPPPackageFacts{Name: "binonly"},
			capCompiledOnly},
		{"unreviewed stdlib", "internal/trace/v2",
			bashPPPackageFacts{Name: "trace", Standard: true, Dir: "/goroot/src/x", GoFiles: []string{"t.go"}},
			capUnreviewedStdlib},
		{"go list error", "example.com/nope",
			bashPPPackageFacts{Error: &struct{ Err string }{Err: "no such package"}},
			capMissing},
		{"incomplete", "example.com/broken",
			bashPPPackageFacts{Name: "broken", Incomplete: true, GoFiles: []string{"b.go"}},
			capMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyBashPPPackage(test.facts, test.path); got != test.want {
				t.Fatalf("classify = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBashPPAllRefusedClassesFailAtImportThroughRunner(t *testing.T) {
	for _, test := range []struct {
		name  string
		facts bashPPPackageFacts
		want  string
	}{
		{"cgo", bashPPPackageFacts{Name: "p", Dir: "/p", CgoFiles: []string{"p.go"}}, "requires cgo"},
		{"compiled-only", bashPPPackageFacts{Name: "p"}, "no Go source"},
		{"package-main", bashPPPackageFacts{Name: "main", Dir: "/p", GoFiles: []string{"main.go"}}, "not importable"},
		{"no-buildable", bashPPPackageFacts{Name: "p", Dir: "/p", IgnoredGoFiles: []string{"p_plan9.go"}}, "not importable"},
		{"missing", bashPPPackageFacts{Error: &struct{ Err string }{Err: "missing"}}, "could not be resolved"},
		{"unreviewed-stdlib", bashPPPackageFacts{Name: "abi", Standard: true, Dir: "/goroot/src/internal/abi", GoFiles: []string{"abi.go"}}, "reviewed Go standard library"},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts := test.facts
			path := "example.com/p"
			if test.name == "unreviewed-stdlib" {
				path = "internal/abi"
			}
			native := &recordingNative{}
			policy := &policyBashPPEvaluator{toolchain: native, facts: func(context.Context, bashPPEvalRequest, string) (bashPPPackageFacts, error) {
				return facts, nil
			}}
			r := newInjectedBashPPRunner(t, policy)
			err := r.Run(context.Background(), parseBashPPInternal(t, "import \""+path+"\"\necho reached\n"))
			if err == nil || (test.want != "" && !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if len(r.bashPPImports) != 0 || native.calls != 0 {
				t.Fatalf("refusal mutated state or called adapter: imports=%#v calls=%d", r.bashPPImports, native.calls)
			}
		})
	}
}

type recordingNative struct {
	calls int
	err   error
}

func (n *recordingNative) Resolve(context.Context, bashPPEvalRequest, string) (string, error) {
	return "", errors.New("unused")
}
func (n *recordingNative) Call(context.Context, bashPPEvalRequest) error {
	n.calls++
	return n.err
}

// TestBashPPPolicyUsesOneReplaceableToolchain pins the honest evaluator
// contract: Bash++ nodes are interpreted by the shell, while Go calls are
// executed exactly once by the package-private adapter selected by policy.
func TestBashPPPolicyUsesOneReplaceableToolchain(t *testing.T) {
	native := &recordingNative{}
	e := &policyBashPPEvaluator{toolchain: native}
	if err := e.Call(context.Background(), bashPPEvalRequest{Selector: []string{"fmt", "Println"}}); err != nil {
		t.Fatal(err)
	}
	if native.calls != 1 {
		t.Fatalf("native calls = %d, want 1", native.calls)
	}
}

// TestBashPPDefaultEvaluatorIsThePolicy pins the wiring. Selecting the native
// adapter directly would bypass import-time capability classification.
func TestBashPPDefaultEvaluatorIsThePolicy(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	if _, ok := r.bashPPTools.eval.(*policyBashPPEvaluator); !ok {
		t.Fatalf("default evaluator is %T, want *policyBashPPEvaluator", r.bashPPTools.eval)
	}
	// Classic Bash must not acquire an evaluator at all: no Bash++ node can
	// reach a runner in that dialect, so wiring one would be a seam where
	// Classic behavior could later drift.
	classic, err := New(Lang(syntax.LangBash))
	if err != nil {
		t.Fatal(err)
	}
	classic.Reset()
	if classic.bashPPTools.eval != nil {
		t.Fatalf("Classic Bash acquired evaluator %T", classic.bashPPTools.eval)
	}
}

// TestBashPPCallHonoursCancellation keeps cancellation ahead of dispatch, so a
// cancelled context never starts a toolchain build.
func TestBashPPCallHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	native := &recordingNative{}
	e := &policyBashPPEvaluator{toolchain: native}
	if err := e.Call(ctx, bashPPEvalRequest{Selector: []string{"fmt", "Println"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if native.calls != 0 {
		t.Fatalf("native ran under a cancelled context (%d calls)", native.calls)
	}
}
