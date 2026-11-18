// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The INBOUND half of the Bash++ typed process boundary, per the ratified
// design (docs/bashpp-posix-superset-syntax.md, "P5 ratified: the typed
// process boundary needs no new grammar").
//
// Three decisions of record govern this file:
//
//  1. THERE IS NO NEW SYNTAX. `r, err := run("ls")` and `out, err :=
//     capture(ls)` are call-shaped and Class R — stock bash already rejects
//     them — so the inbound surface is purely additive. `$(...)` and
//     backticks are Class E and deliberately untouched: "capture stdout as a
//     string" is the correct UNTYPED behaviour and stays exactly as it is.
//
//  2. CAPTURE SEPARATES ITS THREE CHANNELS. A process hands back stdout,
//     stderr and an exit status, and they are three distinct results here:
//     `run` returns all three as one structured value, `capture` returns
//     stdout alone and routes failure through its err result while stderr
//     passes through to the shell's own stderr. Collapsing them is how a
//     failed command becomes a successful empty decode, which is the defect
//     class this boundary exists to remove.
//
//  3. NEVER INFER. Captured bytes are a plain string no matter what they
//     happen to parse as; they become a typed value only through the explicit
//     `json.Decode` call below. A decode that fails says so with a diagnostic
//     naming the offset — it never returns a zero value indistinguishable
//     from a successful empty decode.
//
// `run` and `capture` are predeclared like `panic` and `recover`: they answer
// only after the session's own typed functions have had their chance, so a
// script's `func run(...)` shadows them exactly as a Go declaration shadows a
// predeclared identifier. `json.Decode` is an ordinary imported selector —
// it requires `import "encoding/json"` (under any alias) and is recognised by
// the import PATH, never by the spelling of the local name.

// bashPPCaptureCall reports which predeclared capture function a call names,
// or "" for any other callee. Like [bashPPPredeclaredCall], it is consulted
// only after the session's own functions.
func bashPPCaptureCall(c *syntax.BashPPCall) string {
	if c == nil || c.FuncLit != nil || len(c.Fun) != 1 {
		return ""
	}
	switch name := c.Fun[0].Value; name {
	case "run", "capture":
		return name
	}
	return ""
}

// bashPPShortDeclCapture binds `r, err := run(...)` and `out, err :=
// capture(...)`. It reports false for any other callee so the caller falls
// through to the remaining `:=` forms.
//
// Arguments form the argv directly, evaluated by the established P3 call
// convention (a bare identifier naming a live binding is that binding's
// value, any other bare word is its own literal, `$x` expands, `xs...`
// spreads). There is no word splitting, no globbing and no re-parsing of an
// argument as shell source: the typed boundary passes exactly the argv it
// was written, and the command resolves through the shell's normal dispatch
// — functions, builtins, then PATH.
//
// The `:=` statement itself succeeds (status 0) even when the captured
// command failed: the failure is IN the bound results, which is the point.
// Leaking the child's status into $? would make `set -e` abort on the very
// failure the script just asked to receive as a value.
func (r *Runner) bashPPShortDeclCapture(ctx context.Context, d *syntax.BashPPShortDecl) bool {
	name := bashPPCaptureCall(d.Call)
	if name == "" {
		return false
	}
	if len(d.Lhs) != 2 {
		r.errf("assignment mismatch: %d variable(s) but 2 value(s)\n", len(d.Lhs))
		r.exit = exitStatus{code: 2}
		return true
	}
	argv := r.bashPPCallArgValues(d.Call)
	if len(argv) == 0 {
		r.errf("%s: requires a command\n", name)
		r.exit = exitStatus{code: 2}
		return true
	}
	// capture routes the child's stderr to the shell's stderr: still a
	// separate channel from the captured stdout, just not a bound result.
	// run captures it as data.
	childStderr := r.stderr
	if name == "run" {
		childStderr = nil
	}
	stdout, stderr, status, errMsg := r.bashPPCaptureExec(ctx, name, argv, childStderr)
	var value expand.Variable
	object := false
	switch {
	case errMsg != "":
		// A capture that did NOT run to completion — cancellation, or an
		// internal fatal error — binds an EMPTY value next to a non-empty
		// err: never a truncated value that looks complete.
		value = expand.Variable{Set: true, Kind: expand.String}
	case name == "run":
		value = expand.NewObject(map[string]any{
			"Stdout": stdout, "Stderr": stderr, "Status": status,
		})
		object = true
	default:
		// The captured stdout, byte for byte. Unlike `$(...)`, no trailing
		// newlines are stripped: the untyped form keeps its convenience, the
		// typed boundary is exact. A command that COMPLETED with a non-zero
		// status keeps the bytes it wrote — they are real data — and the
		// failure is reported alongside them through err, so it can never
		// pass as a successful empty capture.
		value = expand.Variable{Set: true, Kind: expand.String, Str: stdout}
		if status != 0 {
			errMsg = fmt.Sprintf("capture: %s: exit status %d", argv[0], status)
		}
	}
	r.bashPPBindResultPair(d.Lhs, value, object, errMsg)
	return true
}

// bashPPCaptureExec runs argv in a subshell with stdout captured, and stderr
// captured too unless childStderr supplies a passthrough writer. It mirrors
// the `$(...)` machinery in runner.go — same subshell, same errexit
// exemption — with the one difference that stdout and stderr never share a
// buffer.
//
// A non-empty errMsg means the capture did NOT run to completion and the
// other results must not be trusted; in particular a cancelled context
// reports cancellation rather than returning whatever bytes arrived before
// it, because a truncated capture that looks complete is precisely the
// "short read reported as success" defect.
func (r *Runner) bashPPCaptureExec(ctx context.Context, name string, argv []string, childStderr io.Writer) (stdout, stderr string, status int, errMsg string) {
	var outBuf, errBuf bytes.Buffer
	r2 := r.subshell(false)
	defer r2.closeDirFile()
	r2.bgProcs = r.bgProcs
	r2.jobsReadOnly = true
	r2.stdout = &outBuf
	if childStderr != nil {
		r2.stderr = childStderr
	} else {
		r2.stderr = &errBuf
	}
	if !r.functraceEnabled() {
		delete(r2.trapCallbacks, "DEBUG")
	}
	// Like `$(...)`, the captured command does not inherit `set -e`: its
	// failure is a result, not an abort.
	r2.opts[optErrExit] = false
	words := make([]*syntax.Word, len(argv))
	for i, arg := range argv {
		words[i] = &syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{Value: arg}}}
	}
	r2.stmts(ctx, []*syntax.Stmt{{Cmd: &syntax.CallExpr{Args: words}}})
	r2.exit.exiting = false
	r2.exit.discarding = false
	r2.exit.returning = false
	if err := ctx.Err(); err != nil {
		return "", "", 0, fmt.Sprintf("%s: %v", name, err)
	}
	if r2.exit.fatalExit && r2.exit.err != nil {
		return "", "", 0, fmt.Sprintf("%s: %v", name, r2.exit.err)
	}
	return outBuf.String(), errBuf.String(), int(r2.exit.code), ""
}

// bashPPBindResultPair binds a (value, err) tuple to the two names of a `:=`,
// skipping `_` like every other tuple form, and leaves the statement itself
// successful.
func (r *Runner) bashPPBindResultPair(lhs []*syntax.Lit, value expand.Variable, object bool, errMsg string) {
	if name := lhs[0].Value; name != "_" {
		r.bashPPDeclareName(name, value)
		if r.exit.code != 0 {
			return
		}
		if object {
			if cell := r.bashPPScope.lookup(name); cell != nil {
				cell.object = &bashPPObjectIdentity{owner: name}
			}
		} else if cell := r.bashPPScope.lookup(name); cell != nil {
			cell.declType = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}
		}
	}
	if name := lhs[1].Value; name != "_" {
		r.bashPPDeclareName(name, expand.Variable{Set: true, Kind: expand.String, Str: errMsg})
		if r.exit.code != 0 {
			return
		}
		r.bashPPScope.lookup(name).declType = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "error"}}
	}
	r.exit = exitStatus{}
}

// bashPPCaptureCommandPosition diagnoses `run(...)` / `capture(...)` in
// command position. The forms exist to separate a process's three result
// channels; discarding all three would repeat what a plain command line
// already does, so the honest answer is to say how the form is used. Class R
// makes the diagnostic free: stock bash rejects the spelling outright.
func (r *Runner) bashPPCaptureCommandPosition(c *syntax.BashPPCall) bool {
	name := bashPPCaptureCall(c)
	if name == "" {
		return false
	}
	r.errf("bash++: %s(...) separates stdout, stderr and exit status into results; bind them with :=\n", name)
	r.exit = exitStatus{code: 2}
	return true
}

// bashPPShortDeclDecode binds `v, err := json.Decode(input)`, the one
// explicit path from captured bytes to a typed value.
//
// It is an imported call: the selector is recognised by its import PATH
// being "encoding/json" — under whatever local name the script imported it —
// never by the local name's spelling, so a local package that happens to be
// called json is not shadowed and not confused. It must run before the
// generic imported-selector delegation, which would otherwise hand the
// selector to the Go toolchain.
func (r *Runner) bashPPShortDeclDecode(ctx context.Context, d *syntax.BashPPShortDecl) bool {
	c := d.Call
	if c == nil || len(c.Fun) != 2 || c.Fun[1].Value != "Decode" {
		return false
	}
	if r.bashPPImports[c.Fun[0].Value] != "encoding/json" {
		return false
	}
	if len(d.Lhs) != 2 {
		r.errf("assignment mismatch: %d variable(s) but 2 value(s)\n", len(d.Lhs))
		r.exit = exitStatus{code: 2}
		return true
	}
	if len(c.Args) != 1 || c.Ellipsis.IsValid() {
		r.errf("json.Decode: takes exactly one argument\n")
		r.exit = exitStatus{code: 2}
		return true
	}
	input := r.bashPPDecodeArg(c.Args[0])
	decoded, err := bashPPDecodeJSON(input)
	if err != nil {
		// The failure is a bound diagnostic next to an EMPTY value — a
		// script can always tell it from a successful decode of anything.
		r.bashPPBindResultPair(d.Lhs, expand.Variable{Set: true, Kind: expand.String}, false, err.Error())
		return true
	}
	value, object := bashPPDecodedVariable(decoded)
	r.bashPPBindResultPair(d.Lhs, value, object, "")
	return true
}

// bashPPDecodeArg evaluates json.Decode's single argument. It follows
// [Runner.bashPPExprValue]'s convention, but reads a bound name through
// [expand.Variable.String] so an Object argument arrives as its JSON
// coercion — the same string every other consumer of the variable sees.
//
// `run` also exposes its byte-exact stdout field as `result.Stdout`. That is
// the documented structured-output boundary: select the already-separated
// stdout channel, then explicitly decode it. It is deliberately narrow rather
// than a general dotted-word evaluator; this call-shaped boundary must not
// become implicit conversion for arbitrary object fields or pipes.
func (r *Runner) bashPPDecodeArg(w *syntax.Word) string {
	if len(w.Parts) == 1 {
		if lit, ok := w.Parts[0].(*syntax.Lit); ok {
			if vr := r.lookupVar(lit.Value); syntax.BashPPValidIdent(lit.Value) && vr.IsSet() {
				return vr.String()
			}
			if base, field, found := strings.Cut(lit.Value, "."); found &&
				syntax.BashPPValidIdent(base) && field == "Stdout" {
				if obj, ok := r.lookupVar(base).Obj.(map[string]any); ok {
					if stdout, ok := obj["Stdout"].(string); ok {
						return stdout
					}
				}
			}
		}
	}
	return r.literal(w)
}

// bashPPDecodeJSON decodes exactly one JSON value from input.
//
// The contract the property tests pin:
//   - success means the WHOLE input was one JSON value (plus whitespace);
//     trailing bytes are an error, never silently ignored, so a truncated or
//     concatenated input can never pass as a complete decode;
//   - failure always carries a diagnostic naming the offset or reason;
//   - numbers keep their source spelling ([json.Number]), so a decoded value
//     re-encodes to the bytes it came from.
func bashPPDecodeJSON(input string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(input))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		switch err := err.(type) {
		case *json.SyntaxError:
			return nil, fmt.Errorf("json.Decode: %s at offset %d", err.Error(), err.Offset)
		default:
			if errors.Is(err, io.EOF) {
				return nil, errors.New("json.Decode: empty input")
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, fmt.Errorf("json.Decode: unexpected end of input at offset %d", len(input))
			}
			return nil, fmt.Errorf("json.Decode: %v", err)
		}
	}
	end := dec.InputOffset()
	for i := end; i < int64(len(input)); i++ {
		switch input[i] {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return nil, fmt.Errorf("json.Decode: trailing data after the JSON value at offset %d", i)
	}
	return v, nil
}

// bashPPDecodedVariable maps a decoded JSON value onto the dialect's one
// value model: a structured value (object, array, null) becomes an Object
// variable, and a scalar stays a plain STRING — the same rule `:=` follows
// everywhere else, so `v, err := json.Decode('"hi"')` interpolates as hi,
// unquoted, exactly like `v := "hi"`.
func bashPPDecodedVariable(v any) (expand.Variable, bool) {
	switch v := v.(type) {
	case nil:
		return expand.NewObject(nil), true
	case string:
		return expand.Variable{Set: true, Kind: expand.String, Str: v}, false
	case json.Number:
		return expand.Variable{Set: true, Kind: expand.String, Str: v.String()}, false
	case bool:
		return expand.Variable{Set: true, Kind: expand.String, Str: strconv.FormatBool(v)}, false
	default:
		return expand.NewObject(v), true
	}
}
