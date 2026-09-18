# Bash++ / Bash# agentic actions — MVP execution plan

Status: user-approved Sprint 134 interpreted MVP delivered 2026-09-07;
see [the handoff](sprint-134-handoff.md) for exact commits, gates and Sprint 117 bootstrap.
User decision: one bare `agentic` contract; no `agentic(N)` or language-level
determinism ladder. Sprint 134 implements the interpreted MVP; Sprint 117
preserves it in ordinary-Go lowering. This is the current language decision
over the earlier proposals in `agentic-tool-duality-design.md` §14.

> **2026-09-13 amendment (Sprint 166):** the interpreted source modifier remains
> unchanged. `bashy agentic ACTION` and the bashy-shell `agentic` shim are now
> the CLI activation spelling for commands and scripts. On an unfixable native
> command/script failure they return one non-executed, non-persisted
> `input_required` skill proposal; they do not retry or call a model. This is
> the bounded exception to Sprint 134's “no new CLI verb / no automatic repair”
> wording. See `bashy-agentic-action-design.md`.

The user approved this plan and appointed the current assistant project manager
for execution and timely delivery. The bare keyword, inclusive meaning of action,
and syntax/scope rules below are the implementation contract. The sprint manager
is `codex-gpt5.6-sol`; the initial two-hour delivery checkpoint is 17:50 UTC.
Bounded internal helpers own corpus, serial engine work and the product adapter;
the manager owns integration and independently executes the final gates.

## The contract

An **action** accepts input, processes it, and produces output or an error.
Methods, functions, closures, shell scripts, commands, utilities and tools are
all actions. Their packaging does not create a different contract.

`agentic` explicitly permits an action or source region to use LLM-assisted
behavior. An implementation may use ordinary code, heuristics, an LLM, or a
mixture. The modifier does not require an LLM call. How the action achieves
its result is its implementation detail; callers retain the normal arguments,
return values, streams and error conventions.

Unmarked code retains its existing behavior. This is an opt-in to new language
assistance, not a promise of mathematical determinism, purity, or truth. Existing
explicit calls to AI tools remain ordinary valid commands. A postcondition
check establishes that postcondition, not identical outputs on repeated runs.

Model choice, local/cloud placement, sampling, budgets, retries and permissions
remain implementation/runtime configuration. `agentic` grants none of these
permissions and implies no automatic repair, retry or rewrite of a command.

## Minimal source surface

Use the same modifier on existing typed function/method and shell-function
definitions; use one scoped block for script actions and command/tool calls:

```bash
agentic func summarize(text string) (string, error) {
    # Ordinary implementation, including an explicitly chosen LLM call.
}

agentic func (r Report) Summarize() (string, error) {
    return summarize(r.Text)
}

agentic function summarize_file() {
    # Existing shell arguments, stdin/stdout/stderr and exit status.
}

agentic {
    summarize_file "$1"
}
```

These illustrate the planned grammar; they are not executable implementations.
A whole script marks its action body with the same outer block. A command or
utility can be implemented by that script, a marked function plus an entry
block, or its compiled form. A tool adapter uses the same scope information at
the existing host handler boundary. No separate command/utility/tool keyword,
per-utility option, script-header directive or new action registry is needed.

The block groups statements in the current shell; it does not introduce a
subshell or change variable lifetime, working directory, redirection or status
rules. Function literals need no new modifier spelling in the MVP: an explicit
block in the literal body expresses an agentic region. Nested definitions do
not become marked merely because they were defined inside a marked region.

Execution evidence: story `642584d1c9e9` now records both oracles for 17 selected
forms and compatibility neighbors. Definition prefixes are Class R; the prefix
`agentic {` is Class E. The 205-row corpus passes both `--check` and
`--posix-gate` independently. Supply the Class-E table entry, near-miss fallback
and `command`/quote escapes. Do not reserve the bare word in ordinary command
arguments or claim every possible `agentic …` prefix. Numeric forms are not admitted.

## Scope and calls — one boolean, no call-graph analysis

1. An explicit block enters agentic scope. A marked function/method is callable
   from agentic scope and executes its body in that scope. Calling a marked
   callable from ordinary scope fails before entering its body, with a source
   diagnostic directing the caller to an explicit block. Ordinary argument
   evaluation rules remain unchanged; this is not transactional rollback.
2. Ordinary helpers remain callable inside agentic scope, but their unmarked
   bodies execute with assistance off. They may explicitly enter their own
   block. Thus absence means no *implicit* assistance, not a transitive purity
   assertion about every action reachable from the function.
3. Save and restore scope at the existing block/call frames, including failure,
   return, panic and cancellation. Preserve a callable's declaration when it
   is passed as a value or resolved through a method/interface. Check the
   resolved callable at invocation; no new effect types or whole-program pass.
4. Pipeline/subshell/task copies carry an independent scope value. A deferred
   call retains its scheduling scope. Existing cancellation and join rules
   still apply. An ordinary helper or callback cannot acquire scope merely
   because an agentic caller is on its stack.
5. `eval` uses its current execution scope. A separately entered or sourced
   script starts with assistance off and opts in through its own source block;
   returning from `source` restores the caller's scope. This concerns only the
   new assistance bit, not shell variables or the existing sourcing behavior.
6. Commands in a marked region retain normal dispatch and arguments. Expose
   the boolean through the existing handler context for cooperating in-process
   tools. A tool that implements no assistance continues its ordinary path.
   External tools receive ordinary argv, streams and exit handling; no automatic
   prompt conversion, universal interception, or new environment/wire protocol.
   A Bash++ script tool carries its own source contract, including when compiled.

These are authoring rules within Bash++/#, not a sandbox for arbitrary external
programs. A Classic script that explicitly invokes `bashy chat` still works.
`BASHY_AGENTIC`, harness detection and `.bpp`/dialect selection never supply the
new source opt-in. Existing formatting/hints and gate environment policy remain
as implemented. Bash# uses the same Bash++ dialect; no third mode is introduced.

## Code inspection and implementation seams

Inspected 2026-09-07; these are current code locations, not delivery claims.

| Existing seam | MVP use |
|---|---|
| `sh/syntax/bashpp_startsites.go`, `parser.go`, `bashpp_func.go` | Extend the existing bounded commitment and function parser; measure the block separately. |
| `sh/syntax/bashpp_nodes.go:BashPPFuncDecl` | Functions and methods already share a node with an optional receiver. Preserve one modifier and its position. |
| `sh/syntax/nodes.go:FuncDecl`, `printer.go`, `walk.go`, `typedjson/` | Shell definitions and the script block must round-trip too. Do not implement typed functions alone. |
| `sh/interp/bashpp_func.go:bashPPInvoke`, `bashPPEnterFrame`, `bashPPFrame.leave` | Shared invocation/frame restoration for typed functions, method values and closures; add boolean checks here. |
| `sh/interp/runner.go:call`, `handlerCtx`; `handler.go:HandlerContext` | Shell-function dispatch and the host command/tool boundary; expose scope without a second dispatcher. |
| `sh/interp/api.go:subshell`, `bashpp_concurrency.go:bashPPGo`; `builtin.go` eval/source | Existing copy, task and dynamic-source paths need scope preservation/restoration tests. |
| `bashy/internal/cli/main.go`, `bashpp.go`, live dialect tests | Exercise actual script, stdin and `-c` entry paths. The resolver file still has historical preparation comments; inspect its live callers. |
| `bashy/internal/agentos/agentos.go:wireExec` | Existing shell command middleware. The direct front-door `Dispatch` path is separate; an environment assignment cannot stand in for a source scope. |
| `coreutils/pkg/chat/chat.go:Invoke(ctx, Options, Runner)` | Existing governed one-shot agent invocation, with injectable execution. Reuse it or its existing CLI for the example; no inference engine is needed. |
| `bashy/internal/agentos/run.go:runCommandEnv` | Currently sets `BASHY_AGENTIC=1`; preserve this existing behavior without treating it as language authority. |

At the pre-implementation baseline, no agentic declaration/rung implementation
was found in `sh/syntax` or `sh/interp`. The inspected P7 story is still todo; compilation docs describe a
planned path, so this review does not claim that ordinary-Go lowering exists.

Adapter inspection confirmed that coreutils already preserves handler context
in `tool.RunContext.Ctx`. The MVP therefore demonstrates a cooperating native
tool in a runnable embedding example, reusing governed `chat.Invoke`; it does
not intercept existing `bashy chat` dispatch or change shell-local environment
semantics. Existing explicit chat commands serve the script wrappers.

## Stories and order

| Order | Sprint / story | Deliverable | Depends on |
|---|---|---|---|
| 1a | 134 / `642584d1c9e9` (bashpp-tests) | Commit selected definition/block measurements and compatibility fixtures; own the final MVP gate. | This contract; final gate waits for 4. |
| 1b | 134 / `be84e6c6dedf` (sh) | Parse and round-trip bare modifier on typed functions/methods, shell functions and script blocks. | 1a commitment evidence. |
| 2 | 134 / `1a1dc881f966` (sh) | Enforce and restore boolean scope across actual action invocation paths. | 1b. |
| 3 | 134 / `872f9f15885f` (umbrella, implementation in bashy as needed) | Expose scope to existing command/tool handlers and demonstrate one LLM-assisted action as function/script/tool. | 2. |
| 4 | 134 / `642584d1c9e9` final gate | Run product fixtures and compatibility checks; record the Sprint 117 handoff. | 1b, 2, 3. |
| 5 | 117 / `72cd8bec4ac6` (umbrella) | Lower the same contract to ordinary Go and prove interpreted/compiled parity. | 134 handoff and P7 lowering seam `59bc1d4ea772`. |

This is five stories, with the existing corpus story owning both the prerequisite
measurements and final verification. Start with serial implementation across the
shared parser/runtime paths; do not create an agent fleet or further decomposition
for this small feature unless concrete implementation evidence requires it.

## Acceptance and verification

Sprint 134 closes only when all five action presentations below work through the
actual Bash++ product path: typed function, receiver method, shell function,
standalone script, and command/utility/tool wrapper. The examples use normal
input/output/error conventions. Passing a marked callable through a variable or
interface must not remove its marker. Compiled packaging is Sprint 117's gate.

Use the existing injectable chat runner/local fixture to prove one success,
one provider error, and cancellation without paid inference. Exercise the
production adapter with that fixture; a mock bypassing dispatch is insufficient.
Ship a runnable example using the existing configured chat provider as well.
A live model smoke is supplementary and reports which provider ran; its changing
prose is not a byte-equality oracle. Preserve the provider's existing policy.

Required negative evidence: ordinary code requests no new implicit assistance;
unmarked helper bodies do not inherit it; marked calls outside a block fail;
scope does not leak after failure or concurrent work; an unavailable requested
provider reports its normal error, not a fabricated result. Numeric declarations
are rejected after commitment without stealing valid classic command syntax.

Retain positioned AST, printer/Walk/typed-JSON, buffered/one-byte parsing and
Class-E escape/fallback checks. Run the existing Bash++/Classic/POSIX mode
matrix, including ambient `BASHY_AGENTIC` set and unset, and the focused shell
compatibility gates. Run the concurrency cases under the race detector. No
foreign certification suite execution or new certification claim is part of
this planning task; existing repository gate policies govern implementation.

Sprint 117 lowers scope entry/restoration and dynamic checks into ordinary Go
values/control flow/runtime calls. Runtime enforcement is allowed: ordinary Go
can perform a boolean check. Preserve existing signatures, using private
compiler plumbing where necessary; do not erase the only enforcement mechanism.
Compare interpreted/compiled status, streams, values, adapter requests, errors
and cancellation using the same deterministic provider fixture. Generated code
must be stable; independently sampled live-model output need not be identical.

The handoff records exact commits/pins, commands/results, shipped examples and
the lowering seam. Sprint 117 may begin after the interpreted contract is fixed
and verified; Sprint 134 does not wait for a compiler implementation.

## Outside the MVP

No numeric levels, inferred effect lattice, generated-value taint types,
postcondition-based removal of the marker, context-scope language, automatic
agent loops, routing ladder, model registry, new provider, new CLI verb,
per-command `--agentic` flag, ambient-authority redesign or generic action
framework. Existing tools can adopt the contract incrementally. The modifier
does not promise that every installed utility suddenly has an LLM implementation.

## Quick current-source comparison — 2026-09-07

This is a bounded primary-source check, not an exhaustive survey or a benchmark
ranking. Documentation was inspected today; no third-party runtime was installed
or tested. Publication dates below belong to papers, not feature release dates.

| Project | Documented support | Relevance to this MVP |
|---|---|---|
| Jac / byLLM | `by llm()` on typed functions and methods; project/module model configuration; structured result validation. [Official reference](https://docs.jaseci.org/reference/plugins/byllm/) | Closest declaration precedent. Jac delegates the implementation to an LLM; our modifier permits a mixed ordinary/assisted body. |
| BAML | Typed input/output functions with a prompt and client. Clients can be overridden at runtime. [Functions](https://docs.boundaryml.com/ref/baml/function), [client override](https://docs.boundaryml.com/ref/baml_client/client-registry) | Keep the callable contract stable while model configuration changes. A numeric determinism level is not needed for that separation. |
| LMQL | `@lmql.query` exposes query code as a normal Python callable; model/decoder settings are invocation configuration. [Python integration](https://lmql.ai/docs/lib/python.html) | Explicit declaration can bridge ordinary code and LLM execution without changing normal calling conventions. It is a query language integration, not a shell contract. |
| Microsoft GenAIScript | JavaScript/TypeScript/Markdown scripts with model configuration and overridable model aliases. [Scripts](https://microsoft.github.io/genaiscript/reference/scripts/), [model aliases](https://microsoft.github.io/genaiscript/reference/scripts/model-aliases/) | Script-level AI composition already has practical precedents; model identity need not be encoded in an action modifier. |
| IBM PDL | Declarative blocks compose models, ordinary code and tools; includes functions and typed model input/output. [Official language overview](https://ibm.github.io/prompt-declaration-language/) | Supports the broad input/process/output action model, but introduces a separate YAML language. We can reuse the existing Bash++ language instead. |

Two 2026 research directions are also relevant: **Turn**, submitted March 7,
proposes a compiled actor-based language for LLM-driven computation
([paper](https://arxiv.org/abs/2603.08755)); **Language-Based Agent Control**,
submitted May 13, studies static typing and runtime enforcement for agent policy
([paper](https://arxiv.org/abs/2605.12863)). These are research proposals, not
evidence that Bash++ needs actors or a policy type system for its MVP.

Assessment: explicit AI-backed functions already exist. The useful Bash++ goal
is a small contract spanning existing methods/functions and the shell's scripts,
commands and tools. The sources support separating implementation configuration
from the callable interface; they do not validate our particular scope rules.
This search found no reason to restore numeric rungs and does not establish a
novelty claim for the bare keyword or an absence of similar work elsewhere.
