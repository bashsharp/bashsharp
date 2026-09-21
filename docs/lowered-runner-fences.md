# Lowered runner fences — which runner shapes lower, and how

**Status:** DELIVERED 2026-09-21 (Sprint 237, S.0–S.3). The decision record
for the `lowering` row of the gap table in `fenced-text-blocks-plan.md`
§Rod vs fish. Code: `sh/lower/polyglot.go` (`runnerLowers`, `runnerAdapter`),
`sh/interp/bashpp_text_fence.go` (`FenceRunnerInvoke`), `sh/polyglot/text.go`
(`WithHost`/`HostFrom`); tests `sh/lower/text_fence_test.go`.

## The rule

**A transpiled binary never depends on a shell on the target.** (Operator,
2026-09-21: "there is no point of transpiling if it requires the bash, which
defeats the purpose of transpilation.") Everything below follows from it.

## D1 — which runner shapes lower

| runner shape | interpreted | lowered |
|---|---|---|
| Bash# `func` of the unit, runner signature | yes | **yes** — the lowered Go function is called directly |
| Bash# `func` with another signature | yes (the bridge adapts) | `LOWER-ETYPE`: "does not have the runner signature `func NAME(verb string, file string, args ...string) string`" |
| shell function (`name() { … }`) | yes | `LOWER-EUNSUPPORTED`: "is a shell function, which runs in the interpreter session a lowered program does not carry; declare the runner as a Bash# function (…) to lower it" |
| builtin, registered command (`bashy commands register`) | yes | `LOWER-EUNSUPPORTED`: "is not a Bash# function of this unit (a builtin or a registered command runs in the shell that runs the program, which a lowered program does not carry); wrap it in a Bash# function (…) to lower it" |
| `dag`, `skill` rows (`InterpretedOnly`) | yes | `LOWER-EUNSUPPORTED`: "its processor is the running shell, which a lowered program does not carry; run it interpreted, or process the body with a Bash# function runner (`~~~dag as NAME !func`), which lowers" |

The runner signature is the contract verbatim: two `string` parameters, a
variadic `string` tail, one `string` result, no defaults, no type parameters.
The signature is what the emitter can call without reflection
(`polyglot.FuncCallback` refuses variadic functions; the emitted call is
direct). A fence with a runner needs an alias, as in the interpreter.

Not taken: a shell-function runner through the embedded session. Under the
execution runtime the session holds the `FuncDecl`, and a call with captured
stdout would be possible, but it is a second calling convention for a shape
whose Bash# spelling is one line away. Refused with that line.

## D2 — calling convention per emitter mode

`foreignDeclarations` emits, per runner plan, the runtime literal
`polyglot.RunnerFence{Type, Runner, Invoke: <prefix>foreignRunnerN}` and the
adapter `foreignRunnerN(ctx, argv)`, which calls the lowered runner with
`argv[0], argv[1], argv[2:]...`:

- **plain mode** (a runner of pure Bash# expressions, `echo`/`printf` only):
  `runner(verb, file, args...)`; stdout captured by swapping `rt.Stdout`.
- **execution mode** (any shell statement in the unit — a `case`, `cat`,
  a `FuncDecl` — sets `needsShell`, which is every real runner):
  `<prefix>call_runner(program, rt.Site{Name: "runner"}, verb, file,
  args...)`; stdout captured by `program.Session.SetStdio(nil, &buf, nil)`
  around the call, which catches both `Program.Echo` and the shell regions
  of the body.

The value is what the interpreter's `bashPPInvokeRunner` computes
(`sh` `44397753`): a returned non-empty string is the value and the captured
bytes are forwarded to stdout; otherwise the captured stdout, trailing
newlines trimmed, is the value. A public plain-signature wrapper of the
runner also exists under execution mode, but it starts a fresh `Program`
with no shell backend (`ErrNoShell` on the first region) and none of the
caller's state — the private entry on the calling Program is the one to
call.

## D3 — how `Invoke` reaches the calling Program

`Invoke` is fixed at `polyglot.Start` (package init), before any Program
exists; callbacks solve the same problem by capturing the Program per call.
Under execution mode the wrapper of a runner plan calls
`module.Call(polyglot.WithHost(foreignProgram.Context, foreignProgram), …)`
and the adapter reads it back with `polyglot.HostFrom(ctx).(*rt.Program)`.
No package-level Program: concurrent Programs stay independent.

## D4 — host-command runners and the `dag`/`skill` rows

**Refused, sharpened** (the operator's decision; the table above). No exec
path, no `BASHY_EXE`/PATH lookup, no vendored shell. `examples/quickstart/
pipeline.bsh` (`~~~dag as ci`) stays interpreted; `scripts/quickstart-smoke.sh`
leg (b) says so. A `func` runner that calls a bashy self-verb
(`"$BASH" zig …`, `examples/runners/zig.bsh`) lowers, but its binary then
needs that shell on the target: the honest table lists it as interpreted-only
and the fences doc says to keep such a runner interpreted.

## D5 — effects of a lowered runner fence

**The gap is recorded, not closed.** No lowered program carries a cap plane
today: `shellrt.Call.Advised` is reserved ("compiled programs do not carry
yet"), `shellrt.Decorators` is nil in a standalone binary (a `@guard` rung is
`BASHPP-EDECO-UNDEF` at call time unless bashy's native decorators are
linked), and `interp.ForeignEffectGate` is asked only by the interpreter — the
built-in text rows' `Effects` (`dockerfile.build`, `tf.apply`) are already
ungated when lowered. The runner's declared effects travel in the Plan
(`Effects: []string{"write"}` in the emitted exports) so a future gate has
them; the 126 boundary denial is the interpreter's and is tested there. When
a lowered program gets a cap plane, the gate belongs in
`polyglot.Module.CallKeywords` behind one seam that interp and lowered
programs share — it fixes the rows too. A todo, not this sprint.

## D6 — where the parity proof lives

`sh/lower/text_fence_test.go` `TestRunnerFenceInterpretedNativeParity`: the
same fence script interpreted (`interp.Run`) and lowered (`lower.Compile` →
`go build` → run), byte-identical stdout, stderr and status, one fixture per
emitter mode (`go test -tags full ./lower`). Beside it:
`TestLowerRunnerFenceExportsMatchInterpreter` (the Plan's exports equal the
interpreter's, runner declared after its fence, effects and signatures
carried), `TestLowerRunnerFenceRefusals` (every refused shape, the route in
the message) and `TestLowerRunnerFenceMethodsFailure` (a failing `methods`
is a compile-time refusal carrying the runner's own stderr). The
BASHSHARP33 differential keeps its pin at 33: this proof is a `lower` test,
not a matrix row.

## Compile-time methods (S.1)

`interp.FenceRunnerInvoke(ctx, file, block, dir, stderr, argv)`: a fresh
`Runner` rooted at `dir`, `Reset` + expand config, then the same hoist the
interpreter's prepare does (`bashPPRunnerFence`: only the runner's own
top-level declaration is evaluated) and `Invoke(argv)`. `prepareForeign`
binds a `RunnerFence` analyzer to it per runner block — before and instead
of the type's row, as the interpreter does — so `Prepare` materializes the
body under the same key (`type, runner, source`) and asks `methods` through
the same path; `ParseMethods` gives the exports the Plan carries. The
runner's stderr during `methods` is appended to the refusal when it fails.
