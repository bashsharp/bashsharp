package interp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A runner fence (`~~~<type> as <alias> !<runner>`) hands its materialized
// body to a runner the script names. The runner resolves, in this order, to
// a Bash++ function of the unit, a shell function, a builtin, or a command
// the embedder registered — and never to a program on PATH: a fence means
// the same thing on every host, so a PATH tool is wrapped in a function.
// Every shape is invoked as `runner <verb> <file> [args…]`; its stdout (a
// Bash++ function: its string result) is the call's value and a failing
// status is the call's error.

// FenceRunnerRegistered, when set, reports whether the embedder dispatches
// a command of that name itself (a registered command of the host shell).
// nil — a standalone engine — registers nothing, so only functions and
// builtins may serve as runners.
var FenceRunnerRegistered func(name string) bool

// ForeignEffectGate, when set, is asked before a foreign export that
// declares effects runs — a text verb, a runner method — with the call's
// context and the declared atoms. A non-nil error denies the call before it
// runs: the message is the diagnostic and the status is 126, the boundary
// denial `@effects` yields under a `@guard` it exceeds. nil — a standalone
// engine — enforces no cap.
var ForeignEffectGate func(ctx context.Context, qualified string, effects []string) error

// ForeignEffectDenied is the status of a foreign call the gate refused.
const ForeignEffectDenied = 126

// bashPPForeignEffectsAllowed asks the gate; false means the call was
// denied and the diagnostic and status are already recorded.
func (r *Runner) bashPPForeignEffectsAllowed(ctx context.Context, fn *bashPPForeignFunc) bool {
	if fn.export.Agentic && !r.bashPPAgentic {
		r.bashPPAgenticCallError(r.curStmtPos, fn.qualified)
		return false
	}
	if len(fn.export.Effects) == 0 || ForeignEffectGate == nil {
		return true
	}
	if err := ForeignEffectGate(ctx, fn.qualified, fn.export.Effects); err != nil {
		r.errf("%v\n", err)
		r.exit.code = ForeignEffectDenied
		return false
	}
	return true
}

// foreignZeroResults is what a denied call binds: the zero value of each
// declared result, so a `:=` site stays well-formed and reads the status.
func foreignZeroResults(fn *bashPPForeignFunc) []string {
	if fn.export.Signature.Dynamic {
		return []string{"", ""}
	}
	out := make([]string, len(fn.export.Signature.Results))
	for i, typ := range fn.export.Signature.Results {
		out[i] = foreignZero(typ)
	}
	return out
}

// bashPPRunnerFence builds the runtime of a runner fence, binding the
// runner to this Runner's dispatch. Fences are prepared before the unit's
// first statement runs, so the runner's own top-level declaration, when the
// unit holds one, is evaluated now: a fence may name a function declared
// anywhere in its unit, and the declaration runs again, harmlessly, in
// order.
func (r *Runner) bashPPRunnerFence(ctx context.Context, file *syntax.File, block *syntax.SourceBlock, language string) polyglot.RunnerFence {
	runner := block.Runner.Value
	for _, stmt := range file.Stmts {
		switch decl := stmt.Cmd.(type) {
		case *syntax.FuncDecl:
			if decl.Name.Value == runner {
				r.cmd(ctx, decl)
			}
		case *syntax.BashPPFuncDecl:
			if decl.Receiver == nil && decl.Name.Value == runner {
				r.cmd(ctx, decl)
				if r.bashPPHoistedDecls == nil {
					r.bashPPHoistedDecls = map[*syntax.BashPPFuncDecl]bool{}
				}
				r.bashPPHoistedDecls[decl] = true
			}
		}
	}
	return polyglot.RunnerFence{
		Type:   language,
		Runner: runner,
		Invoke: func(ctx context.Context, argv []string) (string, error) {
			return r.bashPPInvokeRunner(ctx, block, runner, argv)
		},
	}
}

func (r *Runner) bashPPInvokeRunner(ctx context.Context, block *syntax.SourceBlock, runner string, argv []string) (string, error) {
	if fn, ok := r.bashPPFuncs[runner]; ok {
		args := make([]any, len(argv))
		for i, arg := range argv {
			args[i] = arg
		}
		// A Bash++ function runner answers either way: a returned string is
		// the value; a function that prints instead (the shell habit) has
		// its stdout, minus trailing newlines, as the value. What it prints
		// when it also returns a value reaches the shell's stdout as usual.
		var out bytes.Buffer
		savedStdout := r.stdout
		r.stdout = &out
		result, err := r.bashPPForeignCallback(ctx, runner, fn, args)
		r.stdout = savedStdout
		if err != nil {
			return "", err
		}
		if value := foreignResult(result); value != "" {
			savedStdout.Write(out.Bytes())
			return value, nil
		}
		return strings.TrimRight(out.String(), "\n"), nil
	}
	switch {
	case r.Funcs[runner] != nil, IsBuiltin(runner) && !r.disabledBuiltins[runner],
		FenceRunnerRegistered != nil && FenceRunnerRegistered(runner):
	default:
		return "", fmt.Errorf("runner %s is not a function, a builtin or a registered command (PATH is never consulted)", runner)
	}
	// A command runner runs in this shell with its stdout captured, the way
	// a funsub `${ cmd; }` does: same scope, no subshell, exit state
	// restored afterwards.
	var out bytes.Buffer
	savedStdout, savedExit := r.stdout, r.exit
	r.stdout = &out
	defer func() {
		r.stdout = savedStdout
		r.exit = savedExit
	}()
	r.exit = exitStatus{}
	if len(argv) >= 2 {
		r.setVarString("BASHPP_FENCE_TYPE", block.Language.Value)
		r.setVarString("BASHPP_FENCE_FILE", argv[1])
	}
	r.call(ctx, block.Pos(), append([]string{runner}, argv...))
	// The command's stdout is the value with its trailing newlines removed,
	// as a command substitution reads a command.
	value := strings.TrimRight(out.String(), "\n")
	if err := ctx.Err(); err != nil {
		return value, err
	}
	if r.exit.err != nil {
		return value, r.exit.err
	}
	if r.exit.code != 0 || r.exit.exiting || r.exit.fatalExit {
		return value, fmt.Errorf("%s exited %d", runner, r.exit.code)
	}
	return value, nil
}

// FenceRunnerInvoke runs one runner call of a fence at compile time, the way
// the running shell would: a fresh Runner rooted at dir evaluates only the
// runner's own top-level declaration (the same hoist a prepare does, so the
// runner sees no program state — a unit's first statement has not run when
// its fences are prepared) and then dispatches argv to it. A lowered program
// binds a RunnerFence to it so `runner methods <file>` is answered by the
// declaration the interpreter would ask, through the same materialized file;
// the Plan then carries the exports exactly as the interpreter's does.
// stderr receives the runner's diagnostics; nil discards them.
func FenceRunnerInvoke(ctx context.Context, file *syntax.File, block *syntax.SourceBlock, dir string, stderr io.Writer, argv []string) (string, error) {
	if block == nil || block.Runner == nil {
		return "", fmt.Errorf("fence runner: the block names no runner")
	}
	if stderr == nil {
		stderr = io.Discard
	}
	r, err := New(Lang(syntax.LangBashPP), Dir(dir), StdIO(nil, io.Discard, stderr))
	if err != nil {
		return "", err
	}
	r.Reset()
	r.fillExpandConfig(ctx)
	r.filename = file.Name
	fence := r.bashPPRunnerFence(ctx, file, block, polyglot.CanonicalLanguage(block.Language.Value))
	return fence.Invoke(ctx, argv)
}
