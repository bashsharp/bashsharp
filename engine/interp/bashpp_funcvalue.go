// Copyright (c) 2025, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"maps"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A FUNCTION USED AS A VALUE.
//
// Arguments reach a typed call already expanded to strings, so the callee sees
// the four letters `f2` and not the declaration they name. That is fine for a
// closure, whose value IS a string — the registry handle [bashPPFuncHandlePrefix]
// hands out — but it leaves a plain function name with nothing behind it, and a
// parameter declared `func() int` rejected it as not being a function at all.
//
// The repair is a binding step that runs before the call's type check: at a
// parameter whose declared type is a func type, an argument that names a
// declared function is converted into a value of that function, so the callee
// receives the same kind of handle a closure argument carries and the rest of
// the call — the type check, the frame, `fn()` in the body — needs no special
// case for where the function came from.
//
// CONTEXTUAL (REVERSE) INFERENCE. When the named function is generic, the
// parameter's own signature is what determines its type arguments: passing `f2`
// (`func f2[P any]() P`) at `func() int` infers `P = int` from the RESULT the
// parameter declares, not from any argument value, since a function used as a
// value is not called here. This mirrors Go's reverse inference for assigning a
// generic function (go/types infer.go; the worked examples in
// src/internal/types/testdata/examples/inference2.go), restricted to the case
// this dialect can state: a CONCRETE func-typed parameter. A generic consumer
// whose own parameter type is still open — Go's `g4(f6)` — infers nothing here
// and is reported as an ordinary signature mismatch.

// bashPPBindStatus is the outcome of trying to make a function value out of an
// argument. The three cases are distinct because they belong to different
// reporters: a match binds, a mismatch is left to the call's type check so the
// argument is described the way every other bad argument is, and a failure has
// already said something more specific than "wrong type" and must not be
// spoken over.
type bashPPBindStatus int

const (
	bashPPBindOK bashPPBindStatus = iota
	bashPPBindMismatch
	bashPPBindFailed
)

// bashPPBindFuncValueArgs converts arguments naming a declared function into
// function values, for the parameters that declare a func type. It returns the
// arguments to bind — the original slice when nothing was converted — and
// false when a diagnostic was already reported and the call must not proceed.
func (r *Runner) bashPPBindFuncValueArgs(params []bashPPParam, args []string) ([]string, bool) {
	if len(params) == 0 {
		return args, true
	}
	out := args
	for i, arg := range args {
		param := params[min(i, len(params)-1)]
		signature, ok := param.typ.(*syntax.BashPPFuncType)
		if !ok {
			continue
		}
		// A closure handle is already a function value; the type check
		// compares its signature as it always has.
		if _, handle := r.bashPPClosure(arg); handle {
			continue
		}
		candidate, ok := r.bashPPFuncs[arg]
		if !ok {
			continue
		}
		bound, status := r.bashPPContextualFuncValue(candidate, signature)
		switch status {
		case bashPPBindFailed:
			return nil, false
		case bashPPBindMismatch:
			continue
		}
		// Copy on first write: the caller's slice is also what the frame
		// records, and an argument list that binds nothing must stay the very
		// same slice so no other path observes a copy.
		if &out[0] == &args[0] {
			out = append([]string(nil), args...)
		}
		out[i] = r.bashPPStoreFunc(bound).Str
	}
	return out, true
}

// bashPPContextualFuncValue resolves a declared function against the signature
// a func-typed parameter expects, instantiating it when it is generic.
func (r *Runner) bashPPContextualFuncValue(candidate *bashPPFunc, signature *syntax.BashPPFuncType) (*bashPPFunc, bashPPBindStatus) {
	typeParams := candidate.typeParams()
	if len(typeParams) == 0 {
		if !bashPPSignatureMatches(candidate.params(), candidate.results(), signature) {
			return nil, bashPPBindMismatch
		}
		return candidate, bashPPBindOK
	}
	declParams, declResults := candidate.params(), candidate.results()
	// Inference walks slot by slot, so a shape that cannot line up is a plain
	// mismatch rather than a failure to infer: there is no pairing to draw a
	// binding from in the first place.
	if len(bashppResultTypeExprs(declParams)) != len(bashppResultTypeExprs(signature.Params)) ||
		len(bashppResultTypeExprs(declResults)) != len(bashppResultTypeExprs(signature.Results)) {
		return nil, bashPPBindMismatch
	}
	bindings := make(map[string]syntax.BashPPTypeExpr, bashPPTypeParamCount(typeParams))
	if !r.bashPPInferFromFields(declParams, signature.Params, bindings) ||
		!r.bashPPInferFromFields(declResults, signature.Results, bindings) {
		return nil, bashPPBindMismatch
	}
	if len(bindings) != bashPPTypeParamCount(typeParams) {
		// Go's "cannot infer P": the expected signature mentions the type
		// parameter nowhere, so no context can determine it. Naming the
		// signature we tried is what tells a reader which context was used.
		r.errf("BASHPP-EGENERIC-INFER: cannot infer type arguments for %s from %s\n",
			candidate.name(), bashPPTypeText(signature))
		r.exit = exitStatus{code: 2}
		return nil, bashPPBindFailed
	}
	bound := *candidate
	// A generic method arrives carrying its receiver's bindings; they merge
	// with the ones inferred here for the same reason as in an ordinary
	// instantiation, see [Runner.bashPPInstantiateFunc].
	if len(candidate.typeArgs) > 0 {
		merged := make(map[string]syntax.BashPPTypeExpr, len(candidate.typeArgs)+len(bindings))
		maps.Copy(merged, candidate.typeArgs)
		maps.Copy(merged, bindings)
		bound.typeArgs = merged
	} else {
		bound.typeArgs = bindings
	}
	// Inference is only a proposal: the INSTANTIATED signature is what has to
	// be the parameter's. Go reports the same check as "inferred type … does
	// not match type … of v", and it is what stops a lenient per-slot walk
	// from binding `func(P) []P` to `func(string) []int`.
	if !bashPPSignatureMatches(bound.params(), bound.results(), signature) {
		return nil, bashPPBindMismatch
	}
	if !r.bashPPCheckTypeConstraints(candidate, typeParams, bindings) {
		return nil, bashPPBindFailed
	}
	return &bound, bashPPBindOK
}

// bashPPInferFromFields draws type parameter bindings from one side of a
// signature, pairing each declared slot with the slot the context expects.
func (r *Runner) bashPPInferFromFields(declared, expected []*syntax.BashPPField, bindings map[string]syntax.BashPPTypeExpr) bool {
	declaredTypes := bashppResultTypeExprs(declared)
	expectedTypes := bashppResultTypeExprs(expected)
	for i, declType := range declaredTypes {
		if declType == nil || expectedTypes[i] == nil {
			continue
		}
		if !r.bashPPInferTypeFromParam(declType, expectedTypes[i], bindings) {
			return false
		}
	}
	return true
}

// bashPPSignatureMatches reports whether a function's parameters and results
// are exactly the ones a func type declares.
func bashPPSignatureMatches(params, results []*syntax.BashPPField, signature *syntax.BashPPFuncType) bool {
	return bashPPFieldsSignature(params) == bashPPFieldsSignature(signature.Params) &&
		bashPPFieldsSignature(results) == bashPPFieldsSignature(signature.Results)
}

// bashPPFullyInstantiated reports whether every type parameter of fn already
// has a binding, which is what makes it an ordinary function value rather than
// a generic one still waiting for its type arguments.
func bashPPFullyInstantiated(fn *bashPPFunc, params []*syntax.BashPPTypeParam) bool {
	if len(params) == 0 || len(fn.typeArgs) == 0 {
		return false
	}
	for _, group := range params {
		for _, name := range group.Names {
			if fn.typeArgs[name.Value] == nil {
				return false
			}
		}
	}
	return true
}
