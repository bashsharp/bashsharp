// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Bash++ P2 evaluator capability policy.
//
// The design of record makes this a PRECONDITION of choosing an evaluator, not
// a consequence of it: "decide the fallback for cgo, compiled-only and
// unsupported-feature packages before an evaluator is selected, or the choice
// is made by whatever the prototype happened to run."  This file is that
// decision, expressed as code so it is executable rather than prose.  The
// narrative record, including the measured evidence that retired the Yaegi
// time-box, is docs/bashpp-p2-evaluator-decision.md.
//
// TWO RULES GOVERN EVERYTHING BELOW.
//
//  1. FAIL CLOSED.  An unclassified package is refused, never executed.  The
//     zero value of [bashPPCapability] is capUnknown and its policy is
//     policyRefuse, so a class someone forgets to wire up declines to run
//     instead of quietly reaching the toolchain.
//
//  2. ONE REVIEWED ENGINE. The production evaluator is the pinned Go
//     toolchain adapter. "Interpreted" in the Bash++ plan means that the
//     Bash++ shell interpreter dispatches the typed node; it does not promise
//     an unsafe in-process Go interpreter.

// bashPPCapability classifies an import path by what an evaluator can actually
// do with it. Classification is derived from `go list -json` facts, never from
// the import path's spelling.
type bashPPCapability int

const (
	// capUnknown is the zero value and is deliberately first: an
	// unclassified package must refuse, not execute.
	capUnknown bashPPCapability = iota
	// capReviewedStdlib is in the reviewed pure-Go standard-library
	// inventory and may run through the reviewed toolchain adapter.
	capReviewedStdlib
	// capExternalPureGo is pure Go and buildable outside the standard
	// library: local-module, workspace, vendor and GOPATH packages land here.
	capExternalPureGo
	// capCgo requires cgo.
	capCgo
	// capCompiledOnly has no Go source available to build.
	capCompiledOnly
	// capNotBuildable is `package main`, a package with no buildable Go
	// files, or one excluded by build constraints on this platform.
	capNotBuildable
	// capUnreviewedStdlib is a standard-library package outside the
	// reviewed Go 1.27 inventory. It is refused rather than run: the
	// reviewed inventory is an allow-list, and quietly widening it to
	// "anything go list calls standard" would defeat the review.
	capUnreviewedStdlib
	// capMissing could not be resolved at all.
	capMissing
)

// bashPPPolicy is what the runner does with a classified package.
type bashPPPolicy int

const (
	// policyRefuse is the zero value, so the default for anything
	// unclassified is to decline. See rule 1 above.
	policyRefuse bashPPPolicy = iota
	// policyToolchain permits the reviewed Go toolchain adapter to resolve
	// and execute the package.
	policyToolchain
)

// bashPPPolicyFor is the single decision table. It is a pure function of the
// capability class so the policy can be tested without a toolchain, a module,
// or a network.
func bashPPPolicyFor(c bashPPCapability) bashPPPolicy {
	switch c {
	case capReviewedStdlib, capExternalPureGo:
		return policyToolchain
	case capCgo, capCompiledOnly, capNotBuildable, capUnreviewedStdlib, capMissing, capUnknown:
		return policyRefuse
	}
	return policyRefuse
}

// refusal explains a refusal in the shell's own voice. Every string here is
// user-facing, so each says what was refused and why, never merely that
// something failed.
func (c bashPPCapability) refusal() string {
	switch c {
	case capCgo:
		// bashy is a pure-Go program (see CLAUDE.md, "Pure Go only").
		// Building a cgo package would require a C toolchain the shell
		// does not promise and cannot review, so this refuses rather
		// than inheriting a dependency the project has ruled out.
		return "package requires cgo, which this pure-Go shell does not provide"
	case capCompiledOnly:
		return "package has no Go source available to build"
	case capNotBuildable:
		return "package is not importable: it is package main, has no buildable Go files, or is excluded on this platform"
	case capUnreviewedStdlib:
		return "package is not in the reviewed Go standard library"
	case capMissing:
		return "package could not be resolved"
	case capUnknown:
		return "package could not be classified"
	}
	return "package is not supported by any available evaluator"
}

// bashPPPackageFacts is the subset of `go list -json` this policy reads.
type bashPPPackageFacts struct {
	Name           string
	Standard       bool
	Dir            string
	GoFiles        []string
	CgoFiles       []string
	IgnoredGoFiles []string
	Incomplete     bool
	Error          *struct{ Err string }
}

type bashPPFactsLoader func(context.Context, bashPPEvalRequest, string) (bashPPPackageFacts, error)

// classifyBashPPPackage maps go list facts onto a capability class.
//
// Order matters and is not arbitrary. Unresolvable and unbuildable are checked
// before cgo, because a package that cannot be imported at all should say so
// rather than complain about cgo it was never going to reach.
func classifyBashPPPackage(f bashPPPackageFacts, path string) bashPPCapability {
	// A package whose every file is excluded by build constraints reports a
	// go list Error ("build constraints exclude all Go files"), but calling
	// that "could not be resolved" would be wrong and unhelpful: the package
	// exists and simply does not build on this host. Check the platform shape
	// before falling through to the generic resolution failure.
	if len(f.GoFiles) == 0 && len(f.CgoFiles) == 0 && len(f.IgnoredGoFiles) > 0 {
		return capNotBuildable
	}
	if f.Error != nil || f.Incomplete {
		return capMissing
	}
	// Go itself refuses to import package main. Matching that exactly is
	// mandatory: the measured Yaegi prototype ACCEPTED such an import, and
	// silently diverging from Go here is the kind of defect this policy
	// exists to keep out.
	if f.Name == "main" {
		return capNotBuildable
	}
	if len(f.GoFiles) == 0 && len(f.CgoFiles) == 0 {
		if len(f.IgnoredGoFiles) > 0 {
			// Source exists but every file is excluded by build
			// constraints: a platform mismatch, not a missing package.
			return capNotBuildable
		}
		if f.Dir == "" {
			return capCompiledOnly
		}
		return capNotBuildable
	}
	if len(f.CgoFiles) > 0 {
		return capCgo
	}
	if f.Standard {
		if syntax.BashPPStdlibImportAllowed(path) {
			return capReviewedStdlib
		}
		return capUnreviewedStdlib
	}
	return capExternalPureGo
}

// bashPPGoListFacts runs `go list -json` for one import path.
func bashPPGoListFacts(ctx context.Context, req bashPPEvalRequest, path string) (bashPPPackageFacts, error) {
	target, err := bashPPImportListTarget(ctx, req, path)
	if err != nil {
		return bashPPPackageFacts{}, err
	}
	cmd := exec.CommandContext(ctx, req.internalBuildGo(), "list", "-e", "-json", target)
	cmd.Dir, cmd.Env = req.Dir, req.internalBuildEnv()
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return bashPPPackageFacts{}, ctxErr
		}
		return bashPPPackageFacts{}, fmt.Errorf("bash++ import %q: go list: %w", path, err)
	}
	var facts bashPPPackageFacts
	if err := json.Unmarshal(out.Bytes(), &facts); err != nil {
		return bashPPPackageFacts{}, fmt.Errorf("bash++ import %q: go list: %w", path, err)
	}
	return facts, nil
}

// policyBashPPEvaluator is the evaluator the runner uses by default. It owns
// the capability decision and delegates permitted execution to one replaceable
// package-private adapter. Production installs [nativeBashPPEvaluator], the
// reviewed Go toolchain adapter.
//
// It holds NO per-session mutable state. That is deliberate and load-bearing:
// the capability decision is taken at import time and the import registry is
// already per-runner and cloned into subshells, so a subshell can never
// observe or corrupt a sibling's classification. Caching classifications on
// the evaluator would have reintroduced exactly the shared state the session
// isolation tests forbid, because bashPPToolchain.eval is an interface value
// shared by every subshell.
type policyBashPPEvaluator struct {
	toolchain bashPPEvaluator
	facts     bashPPFactsLoader
}

func newPolicyBashPPEvaluator() *policyBashPPEvaluator {
	return &policyBashPPEvaluator{toolchain: nativeBashPPEvaluator{}, facts: bashPPGoListFacts}
}

// Resolve classifies the package and applies the policy.
//
// DIAGNOSTIC TIMING. Every class that can be decided from package facts is
// decided HERE, at `import`, not deferred to the first call. A refused package
// must fail on the line that named it: an import that appears to succeed and
// then fails at an unrelated call site later reports the error in the wrong
// place, and under `set -e` it aborts at the wrong statement. The registry is
// only ever populated with packages that passed policy.
func (e *policyBashPPEvaluator) Resolve(ctx context.Context, req bashPPEvalRequest, path string) (string, error) {
	req = bashPPModuleRequest(req)
	load := e.facts
	if load == nil {
		load = bashPPGoListFacts
	}
	facts, err := load(ctx, req, path)
	if err != nil {
		return "", err
	}
	capability := classifyBashPPPackage(facts, path)
	// Sprint 165 D8: an INTERNAL standard-library package outside the
	// reviewed inventory is admitted for a declared identity that cmd/go's
	// rule admits it for (bashPPIdentityAdmitsInternal); nothing else moves.
	if capability == capUnreviewedStdlib && (bashPPIdentityAdmitsInternal(req, path) || bashPPReviewedCompilerImport(req, path)) {
		capability = capReviewedStdlib
	}
	if bashPPPolicyFor(capability) != policyToolchain {
		return "", fmt.Errorf("bash++ import %q: %s", path, capability.refusal())
	}
	if !syntax.BashPPValidIdent(facts.Name) {
		return "", fmt.Errorf("bash++ import %q: invalid package name %q", path, facts.Name)
	}
	if err := validateBashPPImportVisibilityFor(req, facts.Dir, path); err != nil {
		return "", err
	}
	return facts.Name, nil
}

// Call executes a selector call with the reviewed, replaceable adapter.
func (e *policyBashPPEvaluator) Call(ctx context.Context, req bashPPEvalRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return e.toolchain.Call(ctx, req)
}

func (e *policyBashPPEvaluator) Values(ctx context.Context, req bashPPEvalRequest) ([]any, error) {
	values, ok := e.toolchain.(bashPPValuesEvaluator)
	if !ok {
		return nil, errors.New("bash++: selected evaluator cannot return object values")
	}
	return values.Values(ctx, req)
}

// bashPPFuncNest is $FUNCNEST as bash reads it: the nesting limit when set
// to a positive integer, and 0 otherwise. Unset is the common case, and the
// call path must not pay for a parse failure on every call.
func (r *Runner) bashPPFuncNest() int {
	raw := r.envGet("FUNCNEST")
	if raw == "" {
		return 0
	}
	limit, _ := strconv.Atoi(raw)
	return limit
}

// bashPPHasFIFOs reports whether the task group holds any FIFO registration.
// A registration is only ever a fresh descriptor, so a statement that sees
// none has no reference scan to run and nothing to retain for one.
func (r *Runner) bashPPHasFIFOs() bool {
	c := r.bashPPConcurrent
	if c == nil {
		return false
	}
	return c.fifoCount.Load() > 0
}
