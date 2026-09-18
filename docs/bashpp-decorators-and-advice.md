# Bash++ decorators and policy advice

Status: **DELIVERED, Sprint #197 (epic `bashpp`), 2026-09-16 UTC.**
Explicit decorators, typed-callable lowering and native policy/advice integration
are implemented and verified. Published revisions, installed-binary results,
and coverage limits are in the
[`delivery evidence`](sprint-197-resume-evidence.md); execution order is in the
[`master execution plan`](sprint-197-master-execution-plan.md). Subordinate
to [`bashpp-posix-superset-syntax.md`](bashpp-posix-superset-syntax.md)
(grammar, activation, start-site table) and
[`bashsharp-ergonomics-tier.md`](bashsharp-ergonomics-tier.md) (the
four-part admissibility test), and consistent with the shipped `agentic`
contract ([`bashpp-agentic-mvp-plan.md`](bashpp-agentic-mvp-plan.md),
`sh/docs/bashpp-agentic.md`, `sh/docs/lowering-agentic.md`).

## The ask

Decorator (Python `@f`) and aspect-oriented programming (AspectJ: join point ·
pointcut · advice · weaving) support in Bash++, for cross-cutting concerns —
logging/trace, auth, guardrail/security/governance. Prior art supplied for
information: Python decorators, the AOP Wikipedia entry,
[dengsgo/go-decorator](https://github.com/dengsgo/go-decorator) and
[MicroWiller/AOP-golang](https://github.com/MicroWiller/AOP-golang).

## Recommendation, in one paragraph

Ship **decorators** as the Bash# feature that is *already admitted and only
deferred*: stacked `@name(args)` lines above a declaration, where a decorator
is an ordinary Bash++ function whose first parameter is `c *Call` (go-decorator's
context shape, not Python's higher-order shape), lowering to a generated Go
wrapper. Deliver **aspects** not as a pointcut language but as **policy
advice**: the *same* decorators applied at function registration by an opt-in,
add-only, deny-only rules file, visible through `bashy inspect advice`. The
standard cross-cutting set (`trace`, `guard`, `retry`, later `require`,
`audit`, `timeout`) rides on bashy's existing telemetry span attributes, the
hash-chained audit ledger, the atlas effect vocabulary and `autoretry` — one
new interception seam (the function join point), zero new planes. The word is
**advice**, never "weave": `weave` is the orchestration tool.

## Findings

### F1 — decorators are pre-admitted; this is the "concrete ask"

`bashsharp-ergonomics-tier.md:102`: "Decorators `@f` — E bare / R called —
Admissible — `@decorator(x)` is Class R and it lowers to a wrapper function.
Deferred past P3 on cost, not principle." And `:147`: "Decorators are revisited
only if something concrete asks for them." P3 (`func`/`defer`) shipped in
Sprints 113/114, so the deferral condition has passed. The four tests
(`:20-40`) still bind every shape below: committed start shape · lowers to
ordinary Go · inert under `--posix` · no re-spelling of a Go construct.

### F2 — only the called form is claimable, and one empty-parens shape is Class E

`bashpp-tests/tools/startsites/baseline.tsv:83-84` already records bare
`@decorator` as **Class E** (bash runs a command named `@decorator`; recorded as
*never claimed* at `bashpp-posix-superset-syntax.md:810`) and `@decorator(x)`
as **Class R**. Measured this session with the classifier's own oracle
(GNU bash 5.3.15, `bash -n`), on the commit line alone as the README requires:

| shape | bash 5.3 | class | consequence |
|---|---|---|---|
| `@t` | accept | E | never claimed (as recorded) |
| `@t(x)` · `@t(3)` · `@t("a")` · `@t(n: 3)` · `@ns.t(x)` · `@t(x); …` | reject | R | free — no row, no escape |
| `@t()` alone | reject | R | — |
| `@t()` followed by a **compound command** (`{`, `(`, `((`, `[[`, `if`, `for`, `while`, `until`, `case`, `select`) | **accept** | **E** | it is bash's `name ()` function definition of a function named `@t`; bash accepts *any* compound body, not only `{` — the engine encodes exactly this at `sh/syntax/parser.go:4413-4427 startsCompoundCommand`. One bounded-lookahead row: commit unless the token after `)` (past newlines) starts a compound command |
| `@t(x) f() { :; }` same line | reject | R | allowed but not the taught form |

### F3 — `agentic` is already the first decorator, and its implementation is the template

`agentic` is a prefix modifier on typed functions, receiver methods and shell
functions that changes invocation behaviour with a check *before the body*.
Its seams are exactly where a general decorator slots in:

| layer | `agentic` today | decorator generalisation |
|---|---|---|
| parser | transactional recogniser `sh/syntax/bashpp_agentic.go:19`; `*Lit` on `BashPPFuncDecl.Agentic` (`bashpp_nodes.go:1224`) and `FuncDecl.Agentic` (`nodes.go:536`); dispatch `parser.go:3076` | `Decorators []*BashPPDecorator` on the same two nodes; one transaction for the whole stack |
| interp | side-table `bashPPAgenticFuncs` (`api.go:103`); gate at `bashpp_func.go:1928` (`bashPPInvoke`) and `runner.go:10702` (`Runner.call`); `HandlerContext.Agentic` (`handler.go:97`); reset for traps/`source`/`mapfile`; cloned into subshells | chain around the body at the same two sites, after the gate |
| lowering | private entry + public wrapper with the declared signature (`sh/lower/callables_runtime.go:95-122,204`; `sh/docs/lowering-agentic.md` §Preserving public callable signatures); `shellrt.Frame` + `Adapter` hook var (`shellrt/agentic.go:132`) | chain emitted inside the private entry; `shellrt.Call` beside `Frame` |

The MVP plan's non-goals still bind (`bashpp-agentic-mvp-plan.md` L208-215):
no generic action framework, no universal interception, no call-graph
analysis. A pointcut DSL would violate all three.

### F4 — bashy already weaves, but only at the *command* join point

`bashy/internal/agentos/agentos.go:1857 wireExec` composes the ExecHandler
chain (outermost first): `telemetry.ExecMiddleware` → audit → exechist →
advisor → learn → weaveGuard → output reducer → autofix → dry-run → coreutils
applets → registered commands. Every rung is
`func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc` — around-advice by
another name. **It never fires for shell functions or Bash++ funcs**; bash's
only function-level hooks are `trap DEBUG` (with the `extdebug` return-2 veto,
`sh/interp/runner.go:10478`), `trap RETURN/ERR` and `FUNCNAME`. The function
join point is the gap decorators fill, and the advice to put there already
exists:

- span attributes and principal: `coreutils/pkg/telemetry/exec.go:37`
  (`cmd.name/argv/cwd/duration_ms/exit_code`, `agent.principal`),
  `coreutils/pkg/policy/audit/actor.go:17 ActorFromEnv`;
- the audit ledger's `Record.Decision allow|ask|deny`
  (`coreutils/pkg/policy/audit/audit.go:73`) — present in the schema, **allow-only
  today**; a guardrail decorator is what fills it;
- the 11-atom effect vocabulary `coreutils/pkg/atlas/atlas.go:128-140`,
  already stamped on every audit record;
- `coreutils/pkg/autoretry` (`Eligible/Classify/Backoff`).

## Prior-art verdicts

| source | what it does | take | refuse |
|---|---|---|---|
| **Python `@f`** | `f = d(f)` at definition; stacked bottom-up; `functools.wraps` | the spelling, one-per-line stacking, the order (nearest the declaration is innermost) | the higher-order contract — `func(func) func` over arbitrary signatures needs per-arity generics or reflection to lower (test 2), and shell functions have no first-class values at all |
| **go-decorator** (`//go:decor f`, `go build -toolexec decorator`) | compile-time AST rewrite; **one fixed decorator signature** `func(ctx *decor.Context)` with `TargetDo()/TargetIn/TargetOut`; params `//go:decor f#{k: v}`; type-level decoration; no reflection, zero runtime cost | the context-shaped contract (this is what makes the lowering a signature-independent generated wrapper); "not calling `TargetDo` skips the body"; compile-time application as the model for a lowering option | comment-annotation syntax (`#` is a bash comment; Bash++ has a real start-site grammar); `#{}` params (Bash# kwargs exist); type-level decoration (post-MVP at most) |
| **AOP-golang** | generic proxy `AOP[T]` with hand-registered `Before/After` per type | nothing | it demonstrates that AOP without syntax degrades to proxy boilerplate |
| **AspectJ / AOP** | pointcut language + advice + weaving (compile/load/run time); inter-type declarations; criticisms: **obliviousness**, **fragile pointcuts**, whole-program reasoning, "action at a distance" (the COME FROM critique) | the vocabulary; the split between an explicit decorator and policy-applied advice | a pointcut DSL in source (no Go spelling → test 4; no lowering → test 2; universal interception → the `agentic` non-goals). The obliviousness critique is answered structurally, not argued away: advice is add-only, deny-only, and printed |

## Design of record

### 1. Syntax — Bash++ only; inert under `--posix`; invisible with the flag off

```bash
@trace()
@retry(n: 3, backoff: "1s")
func deploy(env string, dry bool = false) (string, error) { … }

@guard(effects: "read,net")
func (c *Client) Fetch(url string) ([]byte, error) { … }

@audit()
agentic func summarize(text string) (string, error) { … }

@trace()
function backup() { … }           # classic shell function; `backup() { … }` too
@trace(); func one_liner() { … }   # `;` separator — the single-line / `set -x` form
```

- One decorator per line above the declaration; blank lines and comments
  allowed between; `;` accepted as separator. **Order is Python's.**
- Start site `@IDENT(`: Class R, no table row, no escape — except `@IDENT()`
  (empty parens): commit unless the next token starts a compound command, in
  which case it is the bash function definition of `@IDENT` (one Class E row;
  near-miss fallback to shell; escape `command`/quoting). Bare `@IDENT` stays
  never-claimed. `@pkg.IDENT(` parses (R) but is *reserved* — rejected at
  registration in the MVP.
- Decoratable in the MVP: `func` declarations, receiver methods,
  `agentic func`, shell functions. **Not** function literals, `type`, or
  `agentic {` blocks. A stack followed by anything else rolls back the whole
  stack so the diagnostic is bash's own (`TestBashPPMatchesBash`); a decorated
  `func` nested inside a `func` gets the existing nested-func diagnostic.
- `agentic` stays a keyword modifier, never `@agentic`: it is shipped, and
  the design permits no alternate spelling.

### 2. Contract — a decorator is an ordinary function whose first parameter is `c *Call`

```bash
func trace(c *Call) {
    name := c.Name
    echo "→ $name" >&2
    c.Next()                     # the next decorator, then the body; skip it to deny
    status := c.Status
    echo "← $name status=$status" >&2
}

func retry(c *Call, n int, backoff string = "0") {
    for i := 0; i < n; i++ {
        c.Next()
        if c.Status == 0 { return }
        sleep $backoff
    }
}
```

Fields use existing typed expression access (`name := c.Name`). Shell
`${c.Name}` interpolation is not supported and is not added by this feature.

- `@retry(n: 3)` means: at each invocation of the target, call
  `retry(c, n: 3)`. Arguments are evaluated **per invocation** — for typed
  functions in the declaration's captured scope (`fn.scope`,
  `bashpp_func.go:311-318`), for shell functions in the dynamic scope (the
  divergence shell functions already have). Keyword and default arguments are
  the shipped Bash# binding; nothing new.
- `Call` is **predeclared in `LangBashPP` only and shadowable** (like `any`,
  `bashpp_func.go:1129`): `Name`, `Site`, `Caller`, `Args []any` (shell
  functions: the `"$@"` strings; typed: boxed cells, with channel/interface
  positions preserved by index), `Results []any`, `Status int`, `Agentic bool`
  (from the *declaration*, not the caller frame), `Advised` (rule id or empty),
  `Next()`. A function whose first parameter is not the predeclared `*Call` is
  simply not a decorator (`EDECO-SIG`).
- **Decorators wrap the body, never the gates.** The chain sits after the
  agentic scope check, argument checks and `FUNCNEST`
  (`bashpp_func.go:1928-1947`) and inside the DEBUG/RETURN trap bracket
  (`runner.go:10789/10812`), so a decorator cannot smuggle a marked action
  out of scope and RETURN fires once. Each decorator is invoked **through
  `bashPPInvoke` as an ordinary frame**, so `defer`, panic unwinding
  (`:1953`), `recover` and `FUNCNEST` apply without new machinery — the chain
  must *not* be a plain Go loop around `r.stmts`. Results travel via
  `c.Results` (settled by `bashPPSettleResults`, `:2084`) and are type-checked
  back into the result cells (`EDECO-RESULT`); a skipped body yields zero
  results and whatever `Status` the decorator set.
- Resolution is at **call time** (shell is late-bound; the native registry
  and advice rules may load after the declaration): user function → native
  registry → `EDECO-UNDEF`, status 1, for explicit source decorators.
  **Policy-applied rungs resolve only through the trusted native registry**:
  a source-defined `guard` must not replace a policy guard with arbitrary
  code, otherwise the observe/deny allowlist and deny-only contract are
  bypassable. Missing native policy implementations fail closed.
  Self-decoration is `EDECO-SELF` at
  registration; cycles are `EDECO-CYCLE` at call time, before `FUNCNEST`
  produces a confusing message. Lowering resolves statically, as it does for
  every other callee.
- **Stated cost, not hidden:** decorator frames are visible — `${FUNCNAME[1]}`
  and `caller` inside a decorated function name the innermost decorator.
  There is no bash precedent for hiding a frame, so none is hidden;
  `Call.Caller` is the fix and the `FUNCNAME` shape is pinned by fixture.
  `FUNCNEST` is consumed by decorator frames too; recursion re-enters the
  chain on every level (Python semantics — `@retry` on a recursive function
  multiplies).
- Side-table hygiene mirrors `agentic`: cloned into subshells at the three
  sites the agentic table is (`api.go:2988,3691,3731`); dropped on
  redefinition and `unset -f` beside `vars.go:3012`; **`export -f` refuses** a
  decorated function, as it refuses a marked one — `BASH_FUNC_*` cannot carry
  the contract; `declare -f` prints **source** decorators only.
- Against the four tests: (1) every shape is measured, one E row; (2) the
  lowering is an ordinary Go wrapper calling `retry(call, 3)` — one Go type
  for every decorator, no generics, no reflection; (3) nothing is reachable
  under `--posix`; (4) Go has no decorators, so nothing is re-spelled.

**Why the context shape and not Python's.** A Python decorator takes and
returns a function of the *target's* type; in Go that is per-signature
generics or reflection, and it cannot preserve the public symbol's declared
signature that `lowering-agentic.md` mandates. `func(c *Call)` is one Go type,
one generated wrapper per decorated function, zero reflection — and it is the
shape the substrate already uses (`ExecHandlers` `next` middleware;
go-decorator's `TargetDo/TargetIn/TargetOut`). The cost — a decorator cannot
change a target's signature — is a feature at a governance seam.

### 3. Native decorator registry — the standard cross-cutting set

The engine exposes only the slot: `interp.Decorators(map[string]DecoratorFunc)`
(a mirror of `CommandResolver`, `api.go:2450`) and a process-level
`shellrt.Decorators` (like `Adapter`). Every implementation lives in
bashy/coreutils and is registered on both engines by one adapter; `sh` stays
close to upstream. A user-defined function shadows a native one for explicit
source decorators, never for a policy-applied rung.

| decorator | reuses | concern |
|---|---|---|
| `@trace()` | `telemetry.ExecMiddleware` attribute shape; span `call <name>`; `agent.principal` via `ActorFromEnv` | logging / trace |
| `@guard(effects: "read,net")` | atlas effects; the cap rides on the `ctx` handed to `Next()` (`context.WithValue`; `handlerCtx` derives from it at `runner.go:1716`, so **no new `HandlerContext` field**); the audit rung refuses a command whose atlas effects exceed the cap and writes `Decision: deny` — the unfilled slot. **Deny-only: it can never widen authority** | guardrail / governance |
| `@retry(n:, backoff:)` (source-only, never advised) | `coreutils/pkg/autoretry` | resilience |
| next wave: `@timeout(d)`, `@require(principal:/role:)` (`ActorFromEnv`, `pkg/principal`), `@audit()`, `@memo()`, `@log(level:)` | | auth, audit |

Guard checks use the full existing atlas effect vocabulary, not its lossy
skill-lattice projection. `pure` has no governed effect; missing or unknown
command effects are denied. The cap is a check on commands dispatched through
the host's effect-aware handler, not an OS sandbox for arbitrary process code.

### 4. Advice — policy-applied decorators, no pointcut DSL

- An opt-in rules file (`BASHY_ADVICE=<path>`; off by default; **zero rules
  under `VSC_PROFILE=cert`**, precedent `agentos.go:1899`) maps selectors to
  decorator lines. Selectors are names bashy already has — function-name
  glob, file glob (`funcSources`, `vars.go:3018`), `agentic: true` — never
  call-site pointcuts. Default `exclude: preamble`, so bashy's own preamble
  functions (registered before user code) do not match `*`.
- Applied at **registration** (`runner.go:6830 setFunc`,
  `bashpp_func.go:318`, `bashPPMethodDecl`) through one engine option,
  `interp.Advice(func(name, file string, agentic bool) []DecoratorSpec)`.
  Because `eval`, `source` and `declare -f` re-definition all register, they
  are covered by construction; application is idempotent by rule id, so a
  re-`eval` never stacks duplicates.
- **Add-only · outermost · deny-only.** Advice never removes or reorders a
  source decorator and never interposes between an author's decorator and the
  body. The advisable set is observe/deny-class only (`trace`, `guard`, `log`):
  a policy-applied `retry`, `memo` or `timeout` re-executes or replaces side
  effects behind the author's back, which *is* the obliviousness hazard.
- Visible via `Call.Advised` and a read-only **`bashy inspect advice`**
  aspect (a new self-view is an `inspect` aspect, never a verb —
  `bashy/docs/command-atlas.md:102`). `declare -f` stays source-true.
- Compiled programs: `lower.Options.Advice` applies the same rules at emit
  time (go-decorator's `-toolexec` analogue), so the output is still ordinary
  Go and the interpreted-vs-compiled parity gate compares like with like.
  Post-MVP.

### 5. Lowering

- Undecorated callables emit **byte-for-byte as today** — Sprint 152 D1
  fidelity untouched (`sh/lower/fidelity_test.go`); a Go-only input never
  contains `@`.
- A decorated typed callable emits the chain **inside the private entry**
  (option 2 of `lowering-agentic.md` §Methods, interfaces and function handles)
  so every indirect route — method value, interface dispatch, function handle
  — is decorated. The generated chain builds `shellrt.Call` (packed args; the
  source `Site` string), calls decorators outermost-first with `Next` bound to
  the next rung, and unpacks `Results`; the public symbol keeps its declared
  signature. Source decorators run in standalone generated programs. Native
  decorators resolve through `shellrt.Decorators`; the embedding host registers
  the native set and an effect-aware shell backend. Bashy's adapter shares the
  trace/guard/retry implementation with the interpreter and preserves each
  engine's continuation. `Next(ctx)` carries derived context through the next
  source rung and body without changing the parent Program.
- Decorated *shell* functions are not lowered in the MVP (shell functions are
  not lowered today).

### 6. Non-goals

Pointcut expressions or call-site join points · `@agentic` · decorating
function literals, types, `agentic {` blocks, builtins or external commands
(that is `wireExec` and `bashy commands add`, both shipped) · a `--decorators`
flag or a third dialect · call-graph inference · hiding decorator frames from
`FUNCNAME` · `export -f` transport · once-at-registration argument caching ·
new `HandlerContext` fields · a new ledger, telemetry plane or CLI verb.

## Delivery — Sprint #197 stories

PLANNED-first: fixtures and manifest rows are filed before any engine commit,
and flip to `supported` only at acceptance. The shared-repo rule applies —
confirm no live sprint owns the same seam before a submodule commit. One owner
delivers the serialized `sh` vertical slice; the split parser/interpreter/lower
cards originally filed here were consolidated at manager review to avoid
handoff overhead.

| # | story | owner · scope | gate |
|---|---|---|---|
| S197.1 | **Measure + spec.** Rows for every `@` shape, regenerated baseline, planned fixtures, tier row 102 scheduled | bashpp-tests · umbrella | classifier check + POSIX gate; harness reports PLANNED |
| S197.2 | **Engine vertical slice.** Parser nodes/transactions, interpreted ordinary-frame chain and `Call`, diagnostics/side-table lifecycle, typed-callable lowering and `shellrt.Call` | one serialized sh owner | race-enabled syntax/interp/lower suites; compatibility matrix; D1 fidelity; focused parity |
| S197.3 | **Native policy integration.** Native trace/guard/retry plus opt-in registration-time advice and `inspect advice`, reusing existing telemetry/audit/effects/retry machinery | coreutils → bashy → minimal sh option | positive trace/deny/retry evidence; add-only/idempotent/cert-off advice evidence; Bash 86/86 |
| S197.4 | **Acceptance + close.** Full mode/input acceptance battery, manifest ratchet, installed-binary proof, published submodule heads, umbrella pins, handoff and index status | bashpp-tests · umbrella | every Sprint 197 gate green on built and installed binaries |

Sequencing: 1 → 2 → 3 → 4. The first usable increment inside S197.2 is the
interpreted, user-defined decorator path; do not split it back into management
cards.

## Decisions the executing conductor should not reopen

1. Context-shaped contract (`c *Call`), not Python higher-order — see §2.
2. `Call` predeclared and shadowable, not imported or namespaced; `@ns.name(`
   is reserved so a namespaced spelling stays available at no cost.
3. Arguments evaluated per invocation, never cached at registration.
4. Decorators are ordinary frames; `FUNCNAME` shows them.
5. Advice is registration-time, add-only, outermost, deny-only, off under
   cert, and named *advice*.
6. `retry`/`memo`/`timeout` are never advisable.

## References

- Python decorators — geeksforgeeks.org/python/decorators-in-python
- Aspect-oriented programming — en.wikipedia.org/wiki/Aspect-oriented_programming
- go-decorator — github.com/dengsgo/go-decorator (`//go:decor`, `-toolexec`, `decor.Context`)
- AOP-golang — github.com/MicroWiller/AOP-golang (generic proxy)
- In-tree: `bashsharp-ergonomics-tier.md` · `bashpp-posix-superset-syntax.md`
  §Committed start sites · `bashpp-agentic-mvp-plan.md` · `sh/docs/lowering-agentic.md` ·
  `bashy/docs/audit-log.md` · `bashy/docs/space-time-advisor.md`
