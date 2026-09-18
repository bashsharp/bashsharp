# Bash++ Language Design

**Status:** Sprint198 delivered 2026-09-16; exact candidate and bounded regression evidence
are recorded in `sprint-198-delivery-evidence.json`.
Older phase discussion is historical where superseded by the current contract.

Bash++ is an opt-in GNU Bash 5.3 dialect that adds Go-shaped structured
programming while retaining shell commands, pipelines, expansion, and exit
status. Its extended constructs should use Go syntax directly so Go developers
do not have to learn a second spelling.

## Sprint 198 contract (normative; delivered)

This section supersedes the older fallback and universal-superset wording
below. It is the single current rule table; historical phase notes describe
how the dialect evolved, not exceptions to these rules. The Sprint198 evidence distinguishes enforced behavior from explicitly
recorded semantic gaps.

| Start site in Bash++ mode | Meaning / boundary | Shell escape or control |
|---|---|---|
| Unquoted `var`, `const`, `import` | Reserved declaration forms; malformed continuation is a diagnostic, never command fallback | `command name`, quoted command name, explicit path |
| Unquoted `func` | Typed function, method and literal forms retain their existing Go syntax and diagnose malformed continuations after their commit point. Classic definitions, including `func () { ...; }` and `function func { ...; }`, and ordinary shell invocations with arguments or prefix assignments retain shell syntax | `command func ...`, quoted command name, explicit path |
| `package` at the first non-comment token | Select unchanged Go compilation unit through existing Go frontend before shell parsing; malformed unit stays a Go error | `command package`, quoted name, explicit path; `--source=go` remains |
| `package` elsewhere in mixed input | Reserved; diagnose misplaced unit marker | Same shell escapes |
| `goto LABEL`, `LABEL:` | Existing Go branch/label nodes and their scope validation | `command goto …`, quoted command/label name |
| Identifier/identifier-list followed by `:=` at statement start | Go binding; malformed RHS is diagnosed, never command fallback | `command name …`, quoted command name; `${x:=y}` is unchanged shell expansion |
| Spaced assignment, compound assignment, inc/dec, dereference assignment, multi-assignment | Existing Go statement nodes, including at mixed top level | `command name …`, quote/explicit path; unspaced `x=5`, `x+=1` remain shell assignments; mixed-top-level inc/dec is adjacent (`x++`/`x--`), while `name --` remains shell |
| Channel send `ch <- v` and receive `<-ch` | Existing Go channel statements | Explicit shell command context/quoted word for shell forms |
| Shell-only keyword that is a valid Go identifier in an enumerated binding/update shape (`done := …`, `then = …`, etc.) | Go identifier in that shape; otherwise ordinary shell keyword grammar | Ordinary shell forms unchanged |
| `type NAME TYPE` with complete Go type shape | Go type declaration, including a bare named type. No PATH/type-resolution-dependent parse. `type foo bar` therefore has changed meaning and may report unknown type `bar` | `command type foo bar` retains the builtin; `type foo`, option forms and redirection-only builtin use stay shell |
| Bare generic instantiation such as `f[int]` | Positioned diagnostic: instantiation must be used in a call or binding (a bare value is not a Go expression statement); existing generic calls/bindings stay supported | `command f[int]` or quote the command word |
| Unquoted `//` at the beginning of a word | Comment through end of line, also after a shell command | Quote `"//path"`; `http://x` and `"$a"//b` remain literal words |
| Backticks or `/*` at shell command position | Existing shell substitution/glob syntax | No change; selected Go units use Go lexical rules instead |
| `go build`, `defer cleanup`, shell `return 1` | Existing shell dispatch/context rules | Parenthesized Go calls/functions retain their existing Go behavior |

The parser determines syntax from tokens, never PATH or registry contents.
Reserved names other than `func` cannot name shell function definitions,
including `function name` spelling; quoting an invocation is an escape, not
a way to create another reserved-name function. Sprint 119's approved raw
Bash-fixture acceptance supersedes Sprint 198's blanket `func` reservation
for classic definitions and ordinary calls, including `func nope` and
`var=30 func`. Recognized typed prefixes such as `func f(` still commit to
typed parsing and report malformed syntax instead of command fallback.
Historical Sprint 198 raw measurements and their rejection attribution remain
unchanged. No global keyword ban on `go` or `type`: their existing
shipped-command registry protection is separate from language grammar.
Registry add/update must reject the six reserved words as names or aliases;
verification must report collisions in pre-existing records.

Go-unit selection covers Bashy `-c`, stdin and files, with leading whitespace
and Go comments/directives. It preserves the existing frontend eligibility:
Go units require the Bashy front door and non-POSIX mode. A marker encountered
by pure `bash --bashpp` or a POSIX invocation receives a clear unsupported
Go-unit diagnostic, never shell fallback; no Go frontend is added to the pure
Bash binary. It does not add extension-based dispatch. No shell
parsing or fallback follows a selected Go parse error. Directives are passed
to the existing Go frontend unchanged, including its existing build/embed
policy. A shell shebang or shell statement is not a Go comment prefix.

Only the existing Bash++ grammar selector enables these rules. Classic
`bash`, Bash++ OFF, ordinary `--posix` and certification remain unchanged.
Startup POSIX disables Bash++ grammar and runtime extensions even with an
affirmative selector. Bashy's front door retains POSIX semantics; the pure
`bash` front door preserves Sprint 114's established affirmative-selector
compatibility profile by disabling POSIX differences as well. Explicit
Bash++ grammar selection in the parser library is separate from this startup
resolver policy. Live POSIX mode likewise suppresses Bash++ in subsequent
interactive input, `eval`, and sourced input; `set +o posix` restores an
enabled latent Bash++ dialect. An AST already parsed before an option change
is not retroactively reparsed.

Go-expression string literals keep literal `$` text. Shell argument strings
use shell expansion. A Go binding and its shell projection share a name;
that does not automatically export it as an environment variable. Mixed-program goroutines currently snapshot ordinary Go bindings: a
synchronized child write does not update the parent, and a later parent write
does not update the child. Channels remain the synchronization mechanism.
This is a recorded limitation relative to Go variable-sharing semantics, not
a shared-namespace guarantee. The Go-source route has separate capture rules;
its behavior must not be inferred from the mixed route. Shell environment/cwd/status
are task-local. The existing Go-source shared-counter fixture separately exercises shared
lexical capture in native, interpreted and lowered modes. Shell-function
`return` is an exit-status operation; Go-function
return is a typed result. Go block declarations follow existing Go scope.
The mixed-program fixtures cover these boundaries, including synchronized
parent/child writes. Whole-file parsing validates label scope and forward
jumps; the streaming `Stmts` library API cannot validate future whole-file
labels before receiving the remaining input. No new task or value model is
introduced.

Compatibility claim: an opt-in dialect with explicitly enumerated changes,
not universal acceptance of every Bash program under activation. Class E/R
comes from the pinned Bash oracle. Go support remains the mode-specific,
evidence-backed profile in `bashpp-go-implementation-claim.md`; a new grammar
rule is not a full implementation-conformance result.

## Compatibility Contract

The initial language follows the two-binary product sequence
`bash` → Bash++ → `bashy`, subject to the startup POSIX policy:

| Invocation | Initial language and surface |
|---|---|
| `bash script.sh`, `script.bash`, or extensionless | GNU Bash 5.3; Bash++ off; no agentic surface |
| `bash script.bpp` | GNU Bash 5.3; the suffix alone does not enable Bash++ |
| `bashy <any file>` | Bash++ and agentic surface on by default; independent selectors and startup POSIX policy apply |
| `bash --posix` | Bash's POSIX behavior; certification baseline |
| `bash --bashpp` or `bash --bash++` | Bash 5.3 plus Bash++ syntax |
| `bash --posix --bashpp` or `bash --posix --bash++` | Sprint 114 inert compatibility profile: Bash++ off and POSIX differences off |
| `bashy --posix --bashpp` or `bashy --posix --bash++` | Bash grammar and POSIX semantics; Bash++ grammar and runtime extensions off |

`--bashpp` is the canonical automation-friendly spelling; `--bash++` is its
exact human-friendly alias. Both request Bash++ grammar. Startup POSIX
suppresses that request in either flag order; there is no combined extended
POSIX language mode. Ordinary `--posix` and an explicit `--no-bashpp` retain
POSIX semantics on both front doors. Only pure `bash` with a winning
affirmative selector preserves the earlier Sprint 114 inert profile.

Controls are `--bashpp` / `--bash++`, `--no-bashpp`, `BASHY_BASHPP=1|0`, and
the `bashpp` shell option (`set -o bashpp` / `set +o bashpp`). Bashy's agentic
surface has independent `--agentic` / `--no-agentic` and
`BASHY_AGENTIC=1|0` controls. Initial-mode precedence is:

```text
explicit CLI > environment > .bpp extension > binary default
```

Bash++ must be selected before the file is parsed; an in-file `set -o bashpp`
is too late to change grammar already parsed as one unit. It applies to
interactive and subsequently parsed/sourced input. CLI flags and environment
values request the initial grammar subject to the startup policy. A `.bpp`
suffix is a selector tier only on the Bash++-default `bashy` front
door; pure `bash` requires an affirmative CLI or environment selector.
`#!/usr/bin/env -S bash --bashpp` requests the initial extended grammar.
Live POSIX mode suppresses extensions in subsequently parsed input, including
`eval` and source, until POSIX is turned off. It does not reparse an existing
AST.
Certification uses the standalone `bash` binary with `--posix` and without a
Bash++ selector.

Stock Bash is not required to parse files containing extended syntax. A
polyfill may implement runtime operations, but it cannot add grammar such as
`:=`, `<-`, or Go blocks.

## Go Vocabulary

The Go specification defines exactly 25 keywords:

```text
break        default      func         interface    select
case         defer        go           map          struct
chan         else         goto         package      switch
const        fallthrough  if           range        type
continue     for          import       return       var
```

`close` and `make` are predeclared functions. `error` is a predeclared
interface; `nil`, `true`, and `false` are predeclared identifiers. Bash++
should retain these distinctions and must not introduce alternate control-flow
vocabulary.

## Syntax Surface

Within `--bashpp`, supported Go constructs use their original spelling:

```go
config, err := readConfig("config.json")
if err != nil {
    printf "%s\n" "$err"
    return 1
}

results := make(chan string, 16)
go worker(config, results)
defer cleanup(config)
results <- config
value, ok := <-results

select {
case value = <-results:
    consume "$value"
default:
    printf "%s\n" "no result"
}
```

The initial surface should include declarations (`var`, `const`, `type`,
`struct`, `:=`), imports, functions and multiple results, Go-style calls,
explicit error values, `if`/`for`/`range` blocks, `go`, `defer`, channels, and
`select`. Unsupported Go constructs must produce a clear Bash++ diagnostic
rather than silently changing meaning.

Ordinary shell statements remain available, including commands, assignments,
expansions, redirections, pipelines, functions, and exit-status tests. Bash++
does not add exception handling: errors are returned explicitly, as in Go, and
shell commands continue to report status conventionally.

## Contextual Parsing and Conflicts

Several Go spellings already have shell meanings:

| Go form | Shell conflict |
|---|---|
| `if`, `for`, `case`, `select` | Bash reserved words |
| `break`, `continue`, `return`, `type` | Bash builtins |
| `go` | Common Go toolchain executable |
| `var`, `const`, `package`, `import` | Valid command names |
| `x, err := call()` | A shell simple command and arguments |
| `value <- channel` | Input redirection from a file named `-` |
| `{}`, `()`, `[]` | Grouping, subshell, and pattern syntax |
| `&&`, `||`, `!`, `<`, `>` | Shell operators or redirections |
| `//` and backticks | Paths/globs and command substitution |

These conflicts make exact Go syntax impossible to implement as shell
functions, but not impossible in Bash++ itself. `LangBashPP` needs a contextual
parser selected before tokenization. In extended mode, a token sequence that
matches a supported Go production is parsed as Go-shaped syntax. The Sprint198
reserved forms diagnose invalid continuation; other near misses fall back to
the selected Bash or POSIX shell grammar.

In stock Bash, both `var x=1` and `var x = 1` invoke a command named `var`;
only their argument boundaries differ. In Go, both declare and initialize
`x`. Bash++ intentionally conflates them as the same declaration after
explicit activation. Use an explicit shell-command context, such as
`command var x=1`, when an executable named `var` is intended.

## Go Imports and Evaluator Reuse

`import` is supported with exact Go spelling, including ordinary, aliased, and
grouped forms:

```go
import "dhnt/json"
import fleet "dhnt/fleet"
import (
    "fmt"
    neturl "net/url"
)
```

Imports are idempotent and session-scoped in the interpreter, making them
useful interactively as well as in scripts. `command import ...` forces shell
interpretation. Blank and dot imports may follow ordinary and aliased imports
and retain their Go spelling.

They do **not** receive a diagnostic, and an earlier wording here said they
would. Both measured **Class E** — `import _ "fmt"` and `import . "fmt"` are
valid shell command lines — so under the class-dependent rule in
§Committed Start Sites they fall back to shell until their phase lands.
Diagnosing them would break a script that runs a command named `import`.

Resolution delegates to the Go toolchain rather than inventing a Bash++
package layout. It supports standard packages, the current module/workspace,
vendor directories, and classic `GOROOT`/`GOPATH` lookup. A manifest or
lockfile may pin dependencies, but version syntax is not added to an import
declaration.

Bash++ should reuse an existing evaluator where that avoids reimplementing Go
semantics. Yaegi is the leading candidate for committed Go regions and
interactive imports. It remains replaceable behind an internal adapter: the
Bash++ parser owns contextual classification and the shared typed AST, while
the native Go toolchain owns compiled output. Start with Yaegi as a dependency
or vendored engine and fork only for a demonstrated integration gap. GOPATH
support is required, not a reason to reject the evaluator.

## Polyglot Embedding

Go is the only language fused directly into Bash++ grammar and runtime. Other
languages plug into the Go-native value and call layer through one
foreign-runtime interface. The canonical source form is a naked tilde fence:

```bash
~~~python as py
def add(a, b):
    return a + b
~~~

result, err := py.add(40, 2)
```

The opener is recognized only at column one in Bash++ mode. Three or more
tildes delimit the raw body; the closer must use the exact same run. An
optional `as py` exposes qualified calls, while an unaliased block promotes
public functions for direct calls. In stock Bash the opener is an ordinary
command, so this is a measured Class-E site; `command ~~~python`, quoting, and
indentation preserve the Classic interpretation. Backtick and quote fences are
not Bash++ syntax. See `sh/docs/bashpp-polyglot-fences.md` for the executable
Python subset and runtime contract. The older heredoc-backed `embed` spelling
remains a possible compatibility adapter, not the canonical language syntax.

The common boundary initially carries `nil`, booleans, integers, floats,
strings, bytes, lists, string-keyed maps, and explicit `(value, err)` results.
Stdout and stderr remain byte streams and are never inferred as return values.
Foreign objects use opaque session-scoped handles. Every adapter propagates
cancellation and effect permissions.

| Language | Initial execution strategy |
|---|---|
| Go | Native runtime; Yaegi may execute interpreted regions |
| Python | Persistent managed `python3` worker over framed RPC |
| JavaScript | In-process Goja; optional Node adapter for Node compatibility |
| TypeScript | Transpile with `microsoft/typescript-go` (native Go, no Node), then execute through the JavaScript adapter |
| Rust | Compile and cache as WebAssembly |
| Other languages | Prefer WebAssembly or a persistent process adapter |

### Why TypeScript is embedded rather than fused

TypeScript is not a candidate for grammar fusion, and the reason is structural
rather than a matter of effort. The Go constructs Bash++ adopts are
**statement-level and keyword-led** — `go`, `defer`, `type`, `:=`, `<-` — so
each one is a token sequence a contextual parser can recognise before
tokenization and route to a Go production. TypeScript's value is almost entirely
in its **expression grammar and type annotations**, and that is precisely where
the shell's metacharacters live: backticks, `$`, `<`, `>`, `{}`, `()`, `?`, and
`:` are all load-bearing in both languages simultaneously. A fused TypeScript
would collide with the Contextual Parsing table above at nearly every
expression, not at a handful of recognisable heads.

So TypeScript takes the `embed` path, which is what that builtin is for:

```bash
embed typescript as ts <<'TS'
export function add(a: number, b: number): number { return a + b }
TS

result, err := ts.add(40, 2)
```

**What `microsoft/typescript-go` changes is the transpile step, not the
grammar.** It is Apache-2.0 and pure Go, so the TypeScript path becomes
**Node-free end to end**: a native Go transpiler feeding the in-process Goja
runtime the JavaScript row already specifies. Previously "transpile" implied
`tsc`, which implied the whole Node runtime — the one foreign dependency this
section works hardest to avoid.

Two constraints travel with that, and neither is optional:

- **Emit target must match what Goja executes.** Goja is ES5.1 with partial later coverage; a default modern emit can produce JavaScript it will not run. The adapter pins a conservative target and falls back to the Node adapter when a program needs more, rather than failing at runtime in a way that looks like a program bug.
- **typescript-go cannot be linked today.** Every package is under `internal/` and upstream declares the API "not ready", so the transpiler is invoked as a binary, not imported. That is an upstream decision, not an effort question — see `typescript-go-toolchain.md` for the assessment and its re-check triggers.

Direct CPython linkage would introduce C/cgo and platform coupling, so it is
not the default. Wazero is the preferred portable WebAssembly runtime because
it is written in Go without cgo. This preserves bashy's pure-Go executable;
managed external runtimes remain explicit dependencies rather than bundled
language implementations.

## Implementation Requirements

- Keep the ordinary Bash parser and behavior unchanged when neither Bash++
  flag is present.
- Register `--bash++` as an exact alias of canonical `--bashpp`; neither
  spelling creates a separate language mode.
- Evolve `LangBashPP` into the contextual extended grammar; do not install Go
  keyword functions globally.
- Apply the Sprint 119 startup and live POSIX isolation policy above;
  already parsed ASTs are not retroactively reparsed. Explicit library
  grammar selection remains separate from the front-door resolver policy.
- Lower structured Bash++ constructs to the existing native Go value/runtime
  layer where possible, retaining interpreter fallback for dynamic shell code.
- Resolve Go imports through standard module/workspace/vendor/GOPATH behavior;
  do not create a Bash++-specific package namespace.
- Keep every foreign language behind the same typed adapter. Adding an adapter
  must not add another language grammar to the contextual shell parser.
- Test all four mode combinations, including unchanged Bash/POSIX fixtures,
  conflict precedence, parser diagnostics, and Go-shaped concurrency/error
  behavior.

## References

- [The Go Programming Language Specification](https://go.dev/ref/spec) —
  normative grammar, keywords, declarations, statements, channels, and
  predeclared identifiers.
- [Effective Go](https://go.dev/doc/effective_go) — idiomatic error,
  goroutine, channel, and `defer` usage.
- [Go `build` package](https://pkg.go.dev/go/build) — package discovery,
  build constraints, `GOROOT`, and `GOPATH`.
- [Yaegi](https://github.com/traefik/yaegi) — embeddable Go interpreter and
  candidate evaluator.
- [Goja](https://github.com/dop251/goja) — pure-Go JavaScript runtime.
- [Wazero](https://github.com/wazero/wazero) — zero-dependency, no-cgo
  WebAssembly runtime for Go.
- [Embedding Python](https://docs.python.org/3/extending/embedding.html) —
  CPython initialization and native-linking requirements.
- [Rust linkage](https://doc.rust-lang.org/reference/linkage.html) — native
  static and dynamic foreign-library boundaries.
- [GNU Bash 5.3 Reference Manual](https://www.gnu.org/software/bash/manual/bash.html)
- [POSIX.1-2024 Shell Command Language](https://pubs.opengroup.org/onlinepubs/9799919799/utilities/V3_chap02.html)

## L4 Design Review

Meeting room **7** (`2026-07-24-finalize-bash-exact-go-contextual-syntax-and-imp-543f`)
reviewed this document together with the original
[`bash-plus-plus-design.md`](../bashy/docs/bash-plus-plus-design.md) and
[`bash-plus-plus-compilation.md`](../bashy/docs/bash-plus-plus-compilation.md).
The meeting brief is
[`bashpp-l4-design-review-brief.md`](bashpp-l4-design-review-brief.md).

The following decisions govern implementation where the earlier documents
conflict:

- Exact Go spelling requires explicit `--bashpp`/`--bash++` activation. This
  intentionally supersedes the original default-on, stock-Bash-parseable
  proposal.
- **Historical combined-mode proposal, superseded by Sprint 119:** `--posix`
  was proposed as orthogonal to Bash++ grammar. The current startup policy
  suppresses extensions, and live POSIX suppresses Bash++ in subsequently
  parsed interactive, eval, and sourced input. Existing ASTs remain intact.
- Go parsing begins only at a published, finite set of committed start shapes.
  A near miss falls back to shell parsing; a recognized but unsupported Go
  construct receives a Bash++ diagnostic.
- `command WORD …` or quoting the leading token forces shell interpretation.
  Thus `go build`, `command var x=1`, and `"var" x=1` retain command behavior.
- Contextual classification is stored once in a typed AST. The interpreter and
  compiler never independently reclassify a node.
- External stdout is bytes unless an explicit typed capture/decode operation is
  requested. Goroutines receive task-local shell state and share only
  concurrency-safe typed values and channels. `defer` evaluates arguments when
  the defer statement executes.

### Participant Comments

- **Sable (`claude-fable5`) — AGREE.** Exact-Go syntax forces the opt-in
  reversal. Separate purely additive forms that stock Bash rejects from
  enumerated meaning-changing forms, and test every latter form with both
  Bash++ precedence and a shell escape. Preserve the full compatibility corpus
  under `--bashpp`, not merely the trivial flag-off gate.
- **Arlo (`codex-gpt-5.5`) — AGREE.** Commit to explicit Go start sites and
  delimiter confirmation before leaving shell grammar. Keep unsupported or
  dynamic nodes on a clear diagnostic/interpreter path, and do not ship
  concurrency until its task-local state model is tested.
- **Omar (`codex-gpt5.6-sol`) — AGREE.** Keep flag order and aliases equivalent,
  apply Go semicolon insertion only inside committed Go regions, never
  auto-decode arbitrary stdout, and prove goroutines cannot mutate parent shell
  state.
- **Roan (`ycode-fable5`) — no opinion recorded.** The seat was attempted five
  times, including a targeted retry; its Ycode harness could not obtain an
  Anthropic provider.
- **Asa (`ycode-gpt5.6-sol`) — AGREE.** After the credential-routing repair,
  Asa ratified the plan in room 7. Before implementation, publish the finite Go
  start-site/delimiter table; before concurrency, specify defer scope and
  task-local descriptor ownership; before P5, ratify explicit inbound-decode
  and outbound-encoding syntax.

Four agents explicitly agreed. Roan remains unavailable because this host has
no Anthropic API credential and must not be represented as agreement; it may be
asked to ratify this record when that provider is operable.

## Finalized Implementation Plan

### P0 — Mode and Compatibility Seam

**Historical P0 proposal; its combined-mode requirement is superseded by
Sprint 119's startup POSIX isolation policy.** Register canonical `--bashpp`
and exact alias `--bash++`, select
`LangBashPP` before parsing, and compose either spelling with `--posix` in
either flag order. A certification-profile invocation containing a Bash++ flag
must fail clearly. Document `env -S bashy --bashpp` and a portable dedicated
launcher for shebang use.

**Gate:** alias/order equivalence; all four semantic modes; unchanged Bash and
POSIX results with Bash++ off; the plain-shell corpus under Bash++ has the same
output, status, AST classification, and known fail set except for enumerated
meaning-changing forms.

### P1 — Typed AST and Day-1 Syntax

Add shared typed nodes and a fixture-backed start-site table. Day 1 includes
`var`/`const`, `:=` with multiple results, Go-form calls, and brace-form `if`.
Reserve recognized future constructs with explicit diagnostics. Shell remains
the fallback everywhere else; Go semicolon insertion never crosses into shell
regions.

**Gate:** one positive, near-miss, forced-shell, flag-off diagnostic, and
unsupported-form fixture per start site. Parser and evaluator tests must cover
both Bash and POSIX semantic profiles.

### P2 — Imports and Go Evaluator

Implement exact-Go ordinary, aliased, and grouped imports. Resolve packages
with standard Go module/workspace/vendor/GOPATH behavior and register imported
symbols in a session-scoped namespace. Put evaluator execution behind an
internal interface and time-box a Yaegi integration before writing a new Go
evaluator.

**Gate:** standard-library, module, workspace, vendor, and GOPATH fixtures;
interactive idempotence; alias collisions; forced-shell `command import`;
interpreted/native equivalence for the committed subset; no evaluator-specific
AST or public ABI.

### P3 — Functions and `defer`

Add `func`, `return`, typed function results, and `defer f(args)` with Go
argument timing, function-scoped LIFO execution, and script-exit behavior.
Deferred work never silently crosses a fork.

**Gate:** multiple-result/error behavior agrees with shell status; defer order,
argument capture, return, failure, subshell, and exit cases are deterministic.

### P4 — Structured Concurrency

Add exact-Go call-shape `go`, `make(chan T, n)`, send/receive `<-`, and
`select`. Each owning committed `File.Run` has exactly one structured task
group; every `go` started in that run belongs to it. There is no separate
`await` syntax: completion, return, failure, or exit from the owning run is the
join boundary, and the owner cancels and joins every child before running its
final `EXIT` work or returning control to its caller.

Each task receives a task-local shell-state snapshot: variables, working
directory, options, functions, types, imports, deferred calls, panic/exit
state, traps, signal subscriptions, jobs, and descriptor ownership. Mutating
that state never mutates the owner or a sibling. Tasks may share only channels
and values whose immutable or typed representation is explicitly
concurrency-safe; a goroutine handle is neither a shell value nor exportable.
Inherited and duplicated descriptors still refer to the same underlying open
file description, so their file offsets are shared. Every descriptor reference
nevertheless has explicit ownership: closing a task's reference does not
implicitly close a parent or sibling reference, and the underlying resource is
closed only after its final owner releases it.

Send, receive, and blocked `select` operations are cancellation points. The
first child error or panic recorded by the group cancels its siblings; launch
ordinals make the reported primary failure deterministic when failures race.
A child `exit` is task-local and follows the same cancellation path rather than
terminating the process behind the owner's join.

A channel's positive scope is the committed region that created it, including
nested Go blocks and functions executed in that same region. It stops at every
shell-copy construct, including `( ... )`, command substitution, and a
pipeline stage; attempting the crossing must diagnose, for example,
`channel ch cannot cross a subshell boundary`, rather than hang. Crossing
`execve` is impossible because a channel is an in-process runtime object, and
an attempted channel or task-handle expansion into an external command is
refused; no channel or task handle can be exported. Reset and all terminal
paths cancel and join the group, stop its signal subscriptions, release its
jobs and descriptor references, restore terminal ownership, and run owned
cleanup exactly once before discarding runtime state.

**Gate:** race-enabled tests; no parent-state mutation; channel close,
buffering, cancellation, failure, panic, reset, terminal cleanup, and `select`
behavior; descriptor-offset and close-ownership fixtures; subshell and
`execve` refusal diagnostics; exhaustive `<-` versus shell-redirection
fixtures.

### P5 — Typed Process Boundary

Define explicit capture/decode for inbound bytes and the outbound encoding
contract for typed values. Arbitrary command output is never inferred to be
JSON merely because it is valid JSON.

**Gate:** property tests over arbitrary byte output and round-trip tests for
each supported typed value.

*The outbound-syntax ratification this phase once owed is **closed** — see
§P5 ratified above. There is no outbound syntax: outbound is ordinary
expansion under L0's coercion rule. Inbound capture and decode are call forms,
both Class R and therefore free. What remains is the rule that arbitrary output
is never inferred to be JSON.*

### P6 — Polyglot Runtime

Define one `ForeignRuntime` contract over the typed value boundary. Ship a
persistent Python worker and an in-process JavaScript adapter first, then add a
Wazero adapter and compile Rust embeds to cached Wasm modules. Foreign runtimes
are explicit managed dependencies; they do not weaken bashy's pure-Go build.

**Gate:** typed round trips, explicit foreign errors, stdout/stderr separation,
worker reuse and teardown, cancellation, effect enforcement, handle lifetime,
missing-runtime diagnostics, and parity between script and interactive use.

### P7 — Compiler

Lower certified typed AST nodes to Go incrementally. Dynamic shell code and
unsupported nodes retain an explicit interpreter fallback; no phase reparses
source to decide its language. Foreign blocks are packaged as resources and
retain their declared runtime dependency; Go regions continue to lower
natively.

**Gate:** interpreted and compiled programs have identical stdout, stderr,
status, effects, and concurrency behavior across the committed corpus.

All conformance gates must use the documented hardened runners. The native
uutils suite and quarantined OOM/root-walk cases are never part of a host-side
gate.

## Committed Start Sites

This is the finite table both L4 reviewers named as the blocking artifact:
the exact shapes at which the parser leaves shell grammar and commits to a Go
production. Until it existed, neither the compatibility claim above nor the
conformance gates below were testable.

**It is derived, not authored.** Run a candidate shape through real GNU Bash
5.3 with `bash -n`; the verdict assigns its class.

| Class | Stock bash 5.3 | Consequence |
|---|---|---|
| **R** | **rejects** the shape | Purely additive. No existing script can contain it, so Bash++ may claim it with **no table row, no escape, and no compatibility risk**. |
| **E** | **accepts** the shape | Meaning-changing. Claiming it changes what an existing script does, so it needs an explicit row and shell escape; reserved forms diagnose invalid continuations, while non-reserved near misses may fall back. |

The generator, corpus, and ratcheted baseline live in
[`bashpp-tests/tools/startsites/`](../bashpp-tests/tools/startsites/). That
directory is the authority; the tables here are its readable projection. The
oracles are pinned: GNU bash **5.3 patch 9** (source at
`~/projects/poc/bash/bash`, carrying `parse.y`) and bashy's own engine. Both
are required — a classification from one of them would quietly become an
assertion again. Nothing is executed; `bash -n` parses and exits.

### The law: the parenthesised call is Bash++'s free disambiguator

Bash's only `word (` production is `name ()`. So **nearly every Go construct
whose committed shape contains a `NAME(` call form is already a bash syntax
error**, and everything that is keyword-plus-bare-words is not. Two
consequences that were not obvious before the corpus was run:

- **`go build ./...` parses; `go worker(a, b)` does not.** The collision the
  original design feared — `go` shadowing the Go toolchain, which bashy itself
  ships as a verb — is resolved by the parens, not by renaming. This is
  measured evidence for the L4 decision to abolish `go routine { … }`, which
  until now rested on argument alone.
- **The `:=` start site splits.** `x := 42` is Class E while `x := f()` and
  `x, y := f()` are Class R: one start site, two rows, opposite risk. That
  distinction is invisible without running the oracle, and it is exactly the
  kind of thing hand-authored prose gets wrong.

### Day-1 sites (P1)

Class R rows are listed for completeness; they need no escape.

| Shape | Class | What stock bash 5.3 does with it | Escape |
|---|:-:|---|---|
| `var x = 1` · `var x int = 1` · `var x int` | **E** | `simple_command`: command `var` with arguments | `command var …` · `"var" …` |
| `const K = 2` · `const K int = 2` | **E** | command `const` with arguments | `command const …` |
| `type T struct { A int }` *(one line)* | **E** | command `type` (a bash builtin) with arguments; `{` and `}` are ordinary words in argument position | `command type …` |
| `type T struct {` *(commit point)* | **E** | a complete `simple_command`: builtin `type` with arguments, `{` an ordinary word | `command type …` |
| `type ID = string` | **E** | command `type` with arguments | `command type …` |
| `x := 42` · `x := "hello"` | **E** | command `x` with arguments `:=` and `42` | `command x …` · `"x" …` |
| `x := f()` · `x := f(1, 2)` | **R** | `(` after a word is only legal as `name ()` → parse error | — |
| `x, y := f()` · `config, err := readConfig(…)` | **R** | same — the `(` rejects it | — |
| `x, y := 1, 2` · `x, y := a, b` · `x, y, z := 1, 2, 3` | **E** | a literal tuple has no parens, so this is a `simple_command`: command `x,` with arguments | `command …` · quote |
| `x = 1` · `x.y = 1` | **E** | with spaces this is a `simple_command`, command `x` with arguments `=` and `1` — bash assignment is `x=1`, unspaced | `command …` · quote |
| `x = f()` | **R** | the `(` rejects it | — |
| `m := map[string]int{"a": 1}` · `s := []int{1, 2, 3}` · `g := Gopher{Name: "x"}` | **E** | words plus brace expansion | `command …` |
| `f := Max[int]` | **E** | `[int]` is a glob character class | `command …` · quote |
| `Gopher{Name: "x", Burrow: "y"}` | **E** | brace expansion: `GopherName: "x"` etc. | `command …` · quote |
| `f(1, 2)` · `x.y.z()` · `clear(m)` · `n := len(s)` | **R** | the `(` rejects it | — |
| `if err != nil {` *(commit point)* | **E** | a shell `if` may legally have `{` as the last word of its condition and continue with `then`, so the opening line alone is accepted | require **no `then`** after the matching `}` |
| `if err != nil { … }` *(complete, one line)* | **R** | rejected — no `then`/`fi` | — |
| `for i := range 10 { … }` · `for i < 10 { … }` | **R** | `for` without `do` → parse error | — |
| `switch x { case 1: … }` | **E** | command `switch` with arguments; `case` is only reserved at command position | `command switch …` |

**Classification is of the COMMIT POINT, not the complete construct.** A
parser decides at the opening line; it does not get to see the closing brace
first. So a multi-line construct must be measured at its prefix, and the two do
not always agree — the complete multi-line `type T struct { … }` is a parse
error (Class R), while the line the parser actually commits on,
`type T struct {`, is an ordinary bash command (Class E). Measuring only the
complete form would have recorded that site as free when it is not.

**A prefix that opens a bash compound needs a completing context to be measured
at all.** `bash -n` on a bare `if err != nil {` reports the missing `then` —
that is *incompleteness*, not unavailability, and reading it as a rejection is
a false Class R. Measured inside a minimal completing context, this is what
comes back:

```text
if err != nil {
then echo b
fi
```

**That parses in bash 5.3.** The `{` is simply the last word of the condition.
So `if <expr> {` is genuinely reachable in a valid script, the site is Class E,
and committing to a Go `if` on the brace alone would break that script. It is
the one Day-1 site needing lookahead to a **matching brace**: the confirming
delimiter is the matching `}` with **no `then` after it**.

Calling that lookahead *bounded* was imprecise, and a review round pushed back
on it as circular — the parser cannot find the matching `}` without first
knowing it is parsing Go. It is not circular, and the distinction matters:
brace matching here is **lexical**, not semantic. A scanner pairs `{` with `}`
while respecting quoting, command substitution and here-documents, and needs no
decision about the construct's language to do it. What is true is that the
lookahead is **unbounded in length** — the matching brace may be arbitrarily far
away — so the cost is a scan, not an ambiguity. P1's gate must include a
deeply-nested and a here-document-containing case so that scanner is exercised
rather than assumed. `for` is not affected — no
completing context makes `for i := range 10 {` parse — and the corpus carries
both results rather than reasoning from the `if` case to the `for` one.

This correction came from an L4 review of the first draft of this section,
which held that classifying complete snippets ignored shared prefixes. The
review was right, and the measured `if` case is the counterexample that proves
it.

**A start site that shares a keyword with a shell builtin must commit on a
SIGNAL, never on the keyword.** `return` is the clearest case and it was nearly
got wrong. Every shell form of it is Class E — `return`, `return 1`,
`return $?`, `return x` all parse — so a parser that commits to Go on seeing
`return` would hijack every `return` in every existing script. The Go form is
distinguished by a token the shell form cannot contain:

| Shape | Class | Commits? |
|---|:-:|---|
| `return` · `return 1` · `return $?` · `return x` | **E** | **no** — stays shell |
| `return x, nil` | **E** | yes — the **comma** is the signal |
| `return f()` | **R** | yes — the parens, already a bash syntax error |

The same shape of reasoning covers the other builtin-colliding sites: the signal
for a call is the parens, for multi-assignment the comma, for `if`/`for` the
brace plus its delimiter confirmation. Reading the Class E column as "Bash++
claims this shape" is the error to avoid — Class E means *bash accepts it, so
claiming it needs a signal and an escape*, not that it has been claimed.

**Composite literals are never a start site of their own.** `Gopher{…}` at
command position is brace expansion, so a literal is only recognised inside an
already-committed Go region — the right of `:=`, a call argument, a `return`.
This is also what makes Go 1.27's field-selector keys free: `Burrow.Depth:`
adds no new risk to a construct that already carries its row.

### Shell projection of `:=` values

A review round raised a blocker worth recording because the hazard is real even
though its premise was not: if `x := "hello"` bound a structured value, then
`echo $x` would hand an external command `"hello"` **with literal JSON quotes**,
breaking every ordinary shell idiom that passes a string to a program.

That does not happen, and the reason should be stated rather than left to be
inferred from the implementation. `expand.Variable.String()` returns `Str`
verbatim for `Kind: String` and JSON-encodes only `Kind: Object`. So the rule
is:

- `:=` with a **scalar** right-hand side — string, integer, float, boolean —
  exposes an ordinary shell **string projection**, alongside existing typed
  metadata where required by Go evaluation. `x := "hello"` then `echo $x` prints
  `hello`, exactly as `x=hello` would.
- `:=` with a **structured** right-hand side — map, slice, struct, channel, or
  a Go value returned from an imported call — binds an **Object**, and the JSON
  coercion applies at the OS boundary as L0 specified.

The shell projection is not the complete interpreter value model; see
`sprint-174-runtime-findings.md` for the typed cell and identity substrate.

The JSON encoding is what makes a *structured* value safe to hand to a program
that predates it. Applying it to a scalar would be a regression with no
benefit, so the binding is decided by the value's shape, not by the spelling of
the assignment.

**A related classification that looks contradictory and is not.** The same
review held that `type R interface { Read() int }` (Class R) and
`type T struct { A int }` (Class E) are structurally identical and so cannot
differ. Measured, they differ, and for the reason the whole table turns on:
`Read()` contains a **parenthesised call form**, and bash's only `word (`
production is `name ()`, so the interface declaration is already a syntax
error. Remove the parens and the classification flips — `type T struct { A }`
is Class E, like the struct. This is the disambiguator law operating exactly as
documented, not an inconsistency.

### Scoping of `var` and `:=` — the Day-1 semantic gap

Two independent review rounds flagged that the Day-1 declarations arrive with
no scoping rule, and that Go's lexical block scope and the shell's
function/global scope are not the same thing. They are right, and P1 cannot
ship without an answer, so it is stated here rather than discovered during
implementation.

**The rule.** A `var`, `const`, or `:=` declaration binds in the **innermost
enclosing committed Go region**, lexically, as in Go. It is not visible before
its declaration, and it does not survive the region.

Mapped onto the shell that means:

- At **script top level**, outside any Go region, a declaration behaves as an
  ordinary shell global — which is what a reader of either language expects.
- Inside a **Go function body**, a declaration is `local` to that function. It
  does not leak to the caller, so Go's block scope and bash's `local` agree.
- Inside a **nested Go block** (`if { … }`, `for { … }`), the binding is
  scoped to that block. The shell has no nested lexical scope, so this is the
  one place Bash++ is strictly narrower than shell habit: a variable first
  declared inside an `if` block is **not** visible after it. That is Go's rule,
  and a Bash++ program is read as Go here.
- A **shell command inside a Go region** resolves `$name` against the enclosing
  Go bindings first, then the ordinary shell scope. Interpolation of a
  structured value is the L0 JSON coercion.

**Redeclaration follows Go, not shell.** `x := 1` twice in one block is an
error; the shell's silent overwrite would make the Go spelling mean something
Go does not. Assignment to an existing binding uses `=`.

**Why narrower rather than looser.** The alternative — making Go declarations
dynamically scoped so they behave like shell variables — would let a `:=`
inside a loop body silently outlive it, which no Go reader would predict and
which the compiler phase (P7) could not reproduce, since the lowered Go simply
would not compile. A construct whose interpreted and compiled forms disagree
about scope cannot satisfy P7's byte-identical gate, so scope is not a free
choice.

**P1's gate therefore adds:** shadowing, block exit, redeclaration, visibility
of a Go binding to an embedded shell command, and non-visibility after the
block — under both the Bash and POSIX semantic profiles.

### P5 ratified: the typed process boundary needs no new grammar

The plan carried "the exact outbound syntax remains a specification item to
ratify before P5 implementation", and a review round raised the deferral as a
blocker on P1. Measuring the candidate spellings settles it, and the answer is
better than a chosen syntax: **there is no new syntax to choose.**

| Candidate | Class | Disposition |
|---|:-:|---|
| `r, err := run("ls")` · `out, err := capture(ls)` | **R** | **inbound capture — adopt.** Free: already a bash syntax error |
| `v, err := json.Decode(out)` | **R** | **decode — adopt.** An ordinary imported call; no grammar at all |
| `out := run(ls).Stdout` | **R** | free, if a struct result is preferred to a tuple |
| `ls \| consume(v)` | **R** | free |
| `out := $(ls)` · ``out := `ls` `` | **E** | **do NOT claim.** Keeps its shell meaning |
| `grep $obj` · `export OBJ=$obj` | **E** | **outbound — no syntax.** Ordinary expansion |

Three consequences, all of which remove work rather than add it.

**Inbound typed capture is a call, and calls are free.** Every call-shaped
candidate is Class R, so the whole inbound surface is purely additive.

**`$(…)` and backticks are deliberately left alone.** They are Class E, and
what they already mean — *capture stdout as a string* — is exactly the correct
**untyped** behaviour. Redefining them would not merely be meaning-changing, it
would replace a right answer with a different one. The typed form is
visually distinct precisely because it is a call.

**There is no outbound syntax, only a coercion rule, and L0 already fixed it.**
`grep $obj` is Class E because it is ordinary parameter expansion — and what an
`Object` renders as there was settled at L0 by `expand.ObjectString`: JSON, with
a scalar staying a plain string (see §What `:=` binds). So the item recorded as
"unratified outbound syntax" was never a syntax question. It is closed.

What P5 still owes is a **rule**, not a spelling: arbitrary command output is
never inferred to be JSON merely because it parses as JSON. Decoding is
requested explicitly through the call above, and that requirement stands.

### Class E rows for the later phases

Two rules fell out of writing these, and both are more useful than any
individual row.

**Not every Class E shape is claimed.** A row records a *decision*, and for many
shapes the decision is **stay shell, never claim**. Those rows matter most —
they are the ones that would otherwise be claimed by accident later, by someone
reading Class E as an invitation.

**A construct whose disambiguator would have to be SEMANTIC is never a start
site of its own.** If telling the Go form from the shell form requires knowing
what a name *is* — that `ch` holds a channel, that `py` is a live embed handle —
then the parser cannot decide it, because parsing precedes that knowledge. Such
constructs require a syntactic delimiter or an already-committed Go region.
Sprint198 gives channel send/receive exact syntactic start sites without
looking up the channel type; type errors belong to evaluation. Composite
literals and embed adapters retain their existing bounded shape rules.

#### P2 — imports

| Shape | Signal | Near miss | Escape |
|---|---|---|---|
| `import "fmt"` | `import` + a **string literal**, nothing following | `import foo` (bare word) → reserved-word diagnostic | `command import …` |
| `import neturl "net/url"` | `import` + ident + **string literal** | ident not followed by a string → reserved-word diagnostic | `command import …` |
| `import _ "fmt"` · `import . "fmt"` | Go import form | Invalid continuation → diagnostic | `command import …` |

#### P3 — functions, defer, return

| Shape | Decision |
|---|---|
| `return x, nil` | claim on the **comma** |
| `return` · `return 1` · `return $?` · `return x` | **never claimed** — every shell return must keep working |
| `defer f(x)` | Class R, free |
| `defer cleanup` *(no parens)* | **never claimed.** Go's `defer` requires a *call*, so this is not a Go form at all — claiming it would invent syntax Go does not have |
| `f := obj.Method` | rides the `:=` row; escape `command f …` / `"f" …` |

`defer cleanup` is worth pausing on: it is Class E, so it *could* have been
claimed, and claiming it would have been wrong for a reason unrelated to
compatibility — Go has no such form, and test 4 forbids inventing one.

#### P4 — concurrency

| Shape | Decision |
|---|---|
| `go f(x)` · `go func(){…}()` · `make(chan T, n)` · `select {…}` | Class R, free |
| `go build ./...` · `go test ./...` | **never claimed.** The Go toolchain, which bashy itself ships as a verb. The parens are what separate them |
| `ch <- v` · `value <- ch` · `<-ch` | Sprint198 exact Go channel start sites; syntax is independent of whether the identifier resolves to a channel. Forced-shell escapes and mode-off preserve shell redirection. |
| `value, ok := <-results` | rides the `:=` multi-assign row; the comma is the signal |

#### P6 — polyglot embed

| Shape | Decision |
|---|---|
| `~~~python … ~~~` · `~~~python as py … ~~~` | **Class E** at the opening line; claim only at column one in Bash++ mode; escape with `command`, quoting, indentation, or flag-off |
| `embed python as py <<'PY' … PY` | deferred compatibility adapter; not the canonical source-block syntax |
| `result, err := py.add(40, 2)` | Class R — the typed call is free |
| `py.add 40 2` | **not a start site.** The stdout/status adapter is resolved only when `py` is a live embed handle; otherwise shell. A script with a real command named `py.add` uses `command py.add` |

The fence opener and command-adapter spellings are meaning-changing ordinary
command lines. The typed parenthesized call form is Class R and free.

#### Go 1.27

| Shape | Decision |
|---|---|
| `func (r *Rand) N[Int intType](n Int) Int {…}` · `r.N[int](5)` | Class R, free |
| `f := Max[int]` and its composite-type variants | rides the `:=` row. Subject to the arithmetic-subscript bound — see §The one real bound |
| `Gopher{Burrow.Depth: 3}` | composite literal → **not a start site**; free inside a committed region |
| `var f = Max[[]int]` | rides the `var` row |

#### Bash# candidates

| Shape | Decision |
|---|---|
| `readonly obj` | **no grammar change.** An existing builtin whose semantics extend to `Object` values; nothing to claim or escape |
| `type Color enum { … }` | rides the `type` row; signal is the keyword `enum` after the type name |
| `f(name: "x")` · `func f(a int, b int = 3) {}` | Class R, free |
| `[x*2 for x in xs]` · `a ? b : c` · `x?.y` · `x ?? y` · `match x {…}` · `x -> y` · `x => y` · bare `@decorator` | **never claimed** — rejected in `bashsharp-ergonomics-tier.md`. Recorded here so a later reader does not mistake Class E for an opening |

### Certification safety is an invariant, and it is now gated

**The certification profile invokes the shell with `--posix` and no Bash++
selector, and extended grammar must be inert there.** That was asserted. It is
now measured, per shape, by `classify.sh --posix-gate`: the engine's parse
verdict under `--posix` must equal real bash 5.3's, for every shape in the
corpus *including the ones Bash++ intends to claim*. A divergence means extended
grammar reached the certification profile.

The gate is committed **before any grammar lands**, deliberately. A leak found
by a gate that already exists costs a fix; a leak found by a certification arm
costs the arm, and is traced back through a language feature nobody suspected.

Its first run found five divergences, and **none of them was Bash++** —
nothing is implemented yet. All five were the pre-existing command-position
`NAME[…]` fidelity defect, which turned out to **reproduce under `--posix`** and
was therefore certification-relevant rather than a Bash++ curiosity. They were
recorded in `posix-known-divergent.tsv` with their owner and printed on every
run rather than hidden.

**They are now fixed** (`sh f091034a`), and the closing move is the part worth
keeping. The gate fails if a listed id *stops* diverging: on the first run after
the fix it reported five `STALE` lines, one per row, and refused to pass until
each was removed. So a fix **tightens** the allowlist rather than leaving a
stale excuse behind — because an allowlist that only ever loosens is a hole, a
place where a real Bash++ leak can sit indefinitely under an entry that stopped
describing anything. The file remains, empty, as the mechanism: it is where a
future cert-owned divergence gets recorded with an owner, not a list to be
deleted once it happens to be short.

The corpus now reports **zero engine disagreements across all 188 shapes** —
every shape it knows parses identically under `--posix` in the engine and in
real bash 5.3.

### The unit-test matrix Phase B implements against

The corpus in `bashpp-tests/tools/startsites` proves **classification** — what
bash does with a shape. It does not prove the parser **honours** that
classification. Those are different claims, and only the second one is what
ships. So the Go-level coverage is specified here rather than invented during
implementation.

**Per committed start site, five cases, in `sh/syntax`:**

| # | Case | Asserts |
|---|---|---|
| 1 | **positive** — the exact committed shape | it parses as the Go production, with the expected typed node |
| 2 | **near miss** — the shape with its signal removed (`return x` for `return x, nil`; `f x` for `f(x)`) | non-reserved forms fall back to **shell** with the same AST as `LangBash`; reserved words/operators produce positioned diagnostics |
| 3 | **forced shell** — `command <word> …` and `"<word>" …` | shell wins; the escape in the table actually works |
| 4 | **flag off** — the same input under `LangBash` | byte-identical to today; the dialect gate holds |
| 5 | **unsupported form** — a recognized Go shape the phase has not reached | Follow the published contract: reserved syntax diagnoses invalid/unsupported continuation; non-reserved shapes retain their documented fallback |

Every case runs under **both** semantic profiles (`--posix` on and off), because
the grammar is required to be identical across them and only the embedded shell
semantics differ.

**Three suite-level assertions, in addition:**

- **`TestBashPPMatchesBash`, re-expressed.** It currently demands byte-identical
  ASTs against `LangBash` across the whole corpus, which P1 deliberately breaks.
  The repair is not to weaken it: it must permit divergence **only** at
  published Class E rows, with each divergence traceable to a table row. **A
  divergence not in the table is a failure.** That turns the test into the
  executable form of the compatibility contract.
- **The `--posix` inertness gate**, from `classify.sh --posix-gate`, mirrored as
  a Go test so it fails in `go test ./...` and not only in the shell suite.
- **Lexical brace matching**, exercised by a deeply nested case and a case whose
  body contains a here-document — because the `if` site's confirming delimiter
  is an unbounded scan, and a scanner that has not met a here-doc has not been
  tested.

**Counting.** Day-1 has six committed sites (`var`, `const`, `type`, `:=`,
Go-form call, brace-`if`), so P1 alone owes 6 × 5 × 2 = **60 focused parser
tests** before it can claim the phase, plus the three suite-level assertions.
That number is the point: it is large enough that discovering it during
implementation would reshape the estimate, and small enough to be unremarkable
if planned for.

**What the tests may not do.** They may not assert a shape's class — that is the
corpus's job, and duplicating it in Go would create a second source of truth
that can drift. A parser test asserts *behaviour given* the class; `--check`
asserts the class itself.

### Where the grammar risk actually lives

Settling syntax is the expensive-to-reverse decision. A shape costs one
`bash -n` to classify now; claiming it wrongly after scripts exist is a
compatibility break, and changing `sh/syntax` during a certification campaign
risks the baseline. So the whole surface is enumerated **before** implementation,
across every phase, and implementation of the later phases may then be deferred
freely — the grammar is settled either way.

Enumerating it revealed the risk is **back-loaded**, which a Day-1-only table
would have concealed:

| Phase | Shapes | Class E | Meaning-changing |
|---|---:|---:|---:|
| P1 Day-1 | 64 | 37 | 57% |
| P2 imports | 5 | 3 | 60% |
| P3 func/defer | 20 | 7 | 35% |
| P4 concurrency | 22 | 6 | 27% |
| **P5 process boundary** | 6 | 3 | **50%** |
| **P6 polyglot** | 5 | 4 | **80%** |
| Go 1.27 | 21 | 11 | 52% |
| Bash# candidates | 18 | 12 | 66% |

P6 had **one** row before this pass and is 80% meaning-changing. P5 had
**none** — and both obvious spellings for typed capture, `out := $(ls)` and the
backtick form, are Class E, so the process boundary sits on ground that already
has shell meaning. Other results worth carrying: a bare channel receive `<-ch`
is Class E (bash reads a redirect from a file named `-ch`); `break LOOP` and
`continue LOOP` are Class E; and field access, map/slice indexing and slice
expressions are all Class E.

None of that blocks P0/P1. It does mean the later phases must have their rows
and escapes decided **now**, while changing them is free.

### Later phases

Full rows live in the baseline; the shape of the risk is what matters here.
Imports (`import "fmt"`, aliased, grouped) are **Class E** — `import` is a
valid command name — and take `command import …`. Functions and `defer` are
**mixed**: `func f(x int) int { … }` and `defer cleanup(x)` are Class R, while
bare `defer cleanup` and `return x, nil` are Class E. Concurrency is mixed the
same way: `go worker(a, b)`, `make(chan string, 16)` and `select { … }` are
Class R, while `ch <- v`, `value <- ch` and `value, ok := <-results` are
Class E — `<-` is read as a redirection from a file named `-`.

**Go 1.27 costs almost nothing.** Generic methods are Class R at both the
declaration (`func (r *Rand) N[Int intType](n Int) Int { … }`) and the call
(`r.N[int](5)`); field-selector keys ride an existing row; generalized function
type inference has no syntax at all. One bound is worth recording: a **bare**
generic instantiation used as a value is limited by the subscript path, so
`f[int]` and `f[int, string]` survive while `f[[]int]`, `f[map[string]int]` and
`f[*T]` do not. Every generic **call** form is Class R and therefore
unaffected.

### Recognized-but-unsupported forms — the rule is class-dependent

The design previously said an unsupported Go form must receive a diagnostic.
Measurement shows that rule cannot be unconditional, and the class split says
exactly where it applies:

- **Class R → diagnose.** No valid script can contain the shape, so a Bash++
  diagnostic is strictly better than bash's own `syntax error`. This covers
  `type R interface { … }`, `func (r *R) Read() int { … }`, and `v := x.(T)`.
- **Class E → fall back to shell, never diagnose.** Diagnosing would break a
  working script. This covers `package main`, `goto Done`, `Done:`, `x := *p`,
  `p := &x`, and the blank and dot import forms.

`package main` is Asa's counterexample, and it lands in the second group — so
the contradiction is resolved at exactly the point it was raised.

## Go 1.27 Parity

Go 1.27 shipped **2026-08-19** with exactly three language changes
([release notes](https://go.dev/doc/go1.27#language)). All three are cheap for
Bash++, and the reason is the disambiguator law: the two that have syntax are
either already rejected by stock bash or already inside a construct that
carries a table row.

| Go 1.27 change | Committed shape | Class | Cost |
|---|---|:-:|---|
| **Generic methods** — a method declaration may declare its own type parameters | `func (r *Rand) N[Int intType](n Int) Int { … }` | **R** | free — stock bash rejects it |
| | `r.N[int](5)` (the call) | **R** | free — the `(` rejects it |
| **Struct literal keys may be any valid field selector**, not just a top-level field name | `Gopher{Burrow.Depth: 3}` | **E** | none additional — composite literals already carry a row, and `.` is an ordinary word character inside it |
| **Generalized function type inference** — applies wherever a generic function is assigned to or converted to a matching function type | *no syntax* | — | none — a type-checker property, not a grammar change |

Two restrictions travel with generic methods and are normative:
**interface methods may not declare type parameters, nor may an interface
method be implemented by a generic method.** Bash++ inherits both; they are
Go's rules, not ours to relax.

### The one real bound: bare generic instantiation

`[…]` is already claimed by the engine. `sh/syntax/lexer.go:1368` splits a word
at `[` so that `a[i]=x` can parse, and the subscript is then read as
**arithmetic**. That is fine for a type argument that happens to look like an
arithmetic expression, and not fine otherwise. Measured:

| Shape | Result |
|---|---|
| `f[int]` · `f[T]` · `f[int, string]` | parses — bare names and `,` are valid arithmetic |
| `f[[]int]` · `f[map[string]int]` · `f[*T]` | **does not parse** |

**This bites only a bare instantiation used as a value** — `f := Max[[]int]`.
Every generic **call** form is Class R and therefore unaffected:
`f[[]int](x)`, `f[map[string]int](x)` and `f[*T](x)` are all already bash
syntax errors, so the committed Go region begins before the subscript is ever
read as arithmetic.

**Workarounds, measured rather than reasoned.** Within a committed Go region
the arithmetic reader does not apply, so the fix is to make the instantiation
part of a larger committed form. All four are in the corpus and ratcheted:

| Form | Class | Note |
|---|:-:|---|
| `Max[[]int](xs)` | **R** | the call — free, already a bash syntax error |
| `f := Max[[]int](xs)` | **R** | short declaration from a call — free |
| `var f func([]int) int = Max[[]int]` | **R** | explicitly typed declaration — free |
| `var f = Max[[]int]` | **E** | works, but needs the `var` row's escape |

Note the last row: `var f = Max[[]int]` parses in **both** bash 5.3 and the
engine, so it does not hit the divergence that a bare `f[[]int]` does. Where a
bare instantiation is genuinely wanted, P1's start-site machinery must commit on
the identifier before the `[` is lexed — a parser-ordering fix, not a new
spelling, tracked as a P-phase item rather than resolved here. What must not
happen is a Bash++-specific alternate syntax for type arguments; the
no-alternate-spelling rule applies to Go 1.27 exactly as it applies to
everything else.

### Toolchain floor — deliberately not raised here

`sh`, `bashy` and `coreutils` all pin `go 1.26.5`, and the installed toolchain
is `go1.26.0`. **Raising the floor to 1.27 is a separate change and must not
ride this work.** It recompiles the engine that a live certification arm is
measuring, which is exactly the class of change the shared-gate rule exists to
sequence. The language contract above can be stated, tested against the
collision corpus, and reviewed at the current floor; only the *implementation*
of generic methods needs the newer toolchain, and that is a later phase.

The profile coordinate moves to `go1.27-profile-v1` in
`bashy/docs/release-roadmap-and-versioning.md`. That file lives in a
cert-gated repo, so the edit is **pending an ack** rather than applied here;
this document is the contract, and the release doc records it.

## Compatibility and Go fidelity

The current Sprint198 contract above replaces the former universal-acceptance
claim and permanent fallback for package/goto/labels/comments. Classic modes
retain their separately tested shell behavior. Activated Bash++ has explicit
reserved words and meaning changes with shell escapes. Go compilation units
use the existing Go route; mixed syntax does not redefine their lexical rules.
Go implementation completeness is bounded by measured evidence and recorded
profile decisions in `bashpp-go-implementation-claim.md`. Historical reviews
below are preserved for provenance, not current compatibility guarantees.

## Post-Extension L4 Bashy Judge

Two independent L4 reviewers judged this complete file after the import,
evaluator-reuse, and polyglot additions. These were separate `bashy judge`
runs on distinct tools; a file judgment has no meeting room or weave run ID,
so its UTC `judged_at` value is the run identifier. Both returned `revise`.
The findings below are recorded verbatim and remain open.

### Arlo (`codex-gpt-5.5`) — REVISE

Command:

```bash
/private/tmp/bashy-ycode-fix judge \
  --file docs/bashpp-posix-superset-syntax.md \
  --stage plan --agent codex-gpt-5.5 --json
```

Schema `bashy-judge-v1`; judged at
`2026-07-24T18:44:03.990926Z`; duration `19.839s`; single-reviewer result
`unanimous: true`.

> The direction is plausible and test-oriented, but it cannot be treated as
> build-ready until the ambiguity boundary is made concrete: publish the
> committed parse-entry table and the typed process boundary before
> implementing features that rely on them.

- **Major:** The design depends on a finite Go start-site and delimiter table,
  but does not include it, so the core compatibility claim and parser
  precedence cannot be validated before implementation.
- **Major:** The plan defers exact inbound capture/decode and outbound
  typed-value syntax until P5, but earlier phases introduce typed calls,
  imports, functions, errors, and foreign boundaries that need that
  process-value contract to be coherent.
- **Major:** The concurrency model names task-local shell state but does not
  specify descriptor, trap, job-control, signal, and cancellation ownership
  precisely enough to know what real shell programs will do under goroutines.

### Asa (`ycode-gpt5.6-sol`) — REVISE

Command:

```bash
/private/tmp/bashy-ycode-fix judge \
  --file docs/bashpp-posix-superset-syntax.md \
  --stage plan --agent ycode-gpt5.6-sol --json
```

Schema `bashy-judge-v1`; judged at
`2026-07-24T18:44:56.178825Z`; duration `1m5.371s`; single-reviewer result
`unanimous: true`.

> The most important missing artifact is the actual committed-start grammar
> table: until it enumerates precedence, delimiter confirmation,
> unsupported-form handling, and forced-shell recovery, neither the claimed
> shell compatibility nor the proposed conformance gates are testable.

- **Blocker:** The Contextual Parsing and P1 sections defer the finite
  start-site and delimiter table that defines the language, so the central
  grammar, ambiguity behavior, and compatibility claim cannot yet be
  implemented or reviewed.
- **Blocker:** The Normative Superset claim that every baseline shell program
  remains accepted contradicts the rule that recognized unsupported Go forms
  produce diagnostics, because forms such as `package foo` are valid shell
  command lines that Bash++ would reject.
- **Major:** P4's task-local descriptor snapshot does not define behavior for
  inherited pipes or duplicated file descriptors that share offsets and
  external side effects, leaving ordinary concurrent reads and writes without
  enforceable isolation semantics.
- **Major:** P2 assumes arbitrary module, vendor, and GOPATH packages can be
  executed through a replaceable evaluator without specifying what happens
  for packages Yaegi cannot interpret, such as packages requiring cgo,
  unsupported Go features, or compiled-only integration.

The combined advisory verdict is **REVISE / AGREE WITH CHANGES**. No reviewer
approved the plan as build-ready.

### Commit-point class is not complete-form class

Found while implementing Phase B against this table, by a test failing rather
than by inspection. It corrects the `if` row above, and the correction
generalises.

The corpus measures **complete strings**: "what does stock bash 5.3 do with
this?" The parser needs a different answer — "what does bash do **at this
commit point**?" — because it decides at the opening line. For a single-line
shape the two coincide. For a multi-line one they diverge, and in the direction
that matters:

```
if err != nil { echo a }      complete     → REJECT → Class R
if err != nil {               commit point → ACCEPT → Class E   (with then…fi)
```

Both measurements are correct; they describe different strings. **The
commit-point class governs**, and it is the riskier of the two, because Class E
is what obliges a table row, a fallback and an escape. Reading a complete-form
Class R as the commit-point class would understate the compatibility risk on
exactly the constructs where care is most needed.

The `if` row previously carried the complete-form spelling with the
commit-point class, which conflated them; it is now split into two rows. Any
future multi-line row must state which of the two it measures — the corpus's
`prefix-*` ids exist for precisely this, and they are the rows a parser
implementer should read.

### The three residual findings, dispositioned

Sprint 97 story A1b. Each of the three surviving review findings now has a
written disposition a successor can act on without re-deriving it.

#### 1. Channels across subshells — the prohibition is right, the wording conflated two boundaries

The objection: forbidding channels and goroutine handles from crossing
`execve` or a subshell breaks ordinary shell idioms, because a Go-style
function communicating on a channel fails the moment it is used in a pipeline
or a command substitution.

**The objection is half right, and the half that lands is that one sentence
covered two boundaries that are not alike.**

- **Across `execve`: not a policy, a fact.** A channel is a runtime object in
  one process image. Nothing survives `execve`. There is no design freedom here
  and nothing to justify.
- **Across a subshell: a genuine choice, and it stays** — but the reason is
  *shell semantics*, not implementation convenience. `sh`'s subshells are
  goroutines rather than forks, so a channel *could* technically be shared
  across one. It must not be, because a bash subshell is defined to be a
  **copy**: assignments inside `( … )`, a pipeline stage, or `$( … )` do not
  escape it. A channel that did escape would let a construct the shell defines
  as isolated mutate its parent, which is a worse surprise than the
  restriction.

So the prohibition holds, and its justification changes: **a channel obeys the
same boundary a variable already obeys.** A shell programmer finds that
unremarkable; a Go programmer finds it surprising, which is why it must be
stated in the language contract rather than discovered.

Two requirements follow, and P4's gate owns them:

- The failure must be a **clear diagnostic at the boundary**, never a silent
  hang or a value that quietly goes nowhere. "Channel `ch` cannot cross a
  subshell boundary" is the minimum.
- The **scope rule is stated positively**: a channel is usable throughout the
  committed Go region that created it, including nested Go blocks and functions
  executed in that same region; it stops at any construct shell defines as a
  copy.

#### 2. Phase dispositions, re-examined adversarially

The objection: reclassifying a blocker as a phase-specific gate is a way of
dismissing it. A1 already conceded one instance, so the remaining two were
re-checked against that objection rather than defended because A1 wrote them.

**Yaegi-cannot-interpret → P2. Disposition holds.** It is a *precondition* of
choosing the evaluator, not something the phase discovers: decide the fallback
for cgo, compiled-only and unsupported-feature packages before an evaluator is
selected, or the choice is made by whatever the prototype happened to run.
Nothing earlier than P2 resolves an import, so it cannot bind sooner.

**Descriptor / trap / signal ownership → P4. Disposition NARROWED.** The
concurrency half is right: nothing before P4 creates a second thread of
control. But the phase plan also says *"deferred work never silently crosses a
fork"* — and `defer` is **P3**. Fork-crossing ownership is therefore a P3
question wearing a P4 label, and the original disposition was too coarse.
Split:

| Question | Binds at |
|---|---|
| Does a deferred call run when its function exits inside a subshell? | **P3** |
| Descriptor, trap, job-control, signal and cancellation ownership across goroutines | **P4** |
| Inherited pipes and duplicated descriptors sharing offsets | **P4** |

That is one disposition confirmed, one corrected. The objection was worth
taking seriously precisely because it found something the second time.

#### 3. What "the gate is met" can mean for a non-deterministic judge

Four `bashy judge` runs over substantially the same document produced four
different finding sets. Two rounds found real defects — the `return` hijack and
the commit-point classification error, both of which would have shipped as
compatibility breaks. Two produced at least one claim that **measurement
refuted** (`type R interface` and `type T struct` are not "structurally
identical"; the JSON-quoting blocker rested on a premise the code contradicts).

That is a consistent profile: **high recall, low precision.** Good at proposing
candidate problems, unreliable about whether any given one is real.

**So the judge is a finding generator, not a gate, and the gate moves to the
disposition log.**

- The gate is met when **every finding has a written disposition**: fixed, or
  refuted with evidence, or accepted and scheduled against a named phase.
- **A verdict of `approve` is neither required nor sought.** Under this profile
  an `approve` mostly means the sampler missed, which is not information.
- **Re-rolling until a run approves is forbidden.** It is gaming an oracle, and
  it defeats the reason the start-site table was derived rather than asserted.
- Where a finding is checkable, **measurement outranks the judge** — three
  refutations in this sprint came from running `bash -n`, not from arguing.

This is deterministic, auditable, and uses the instrument for what it is
actually good at. It is also what was done in practice across A1, A2 and A7;
this records it as the rule rather than leaving it as a habit.

### Disposition of the review findings

**Closed by §Committed Start Sites** (Sprint 97, story A1):

- *The finite start-site/delimiter table is missing* — both reviewers' central
  finding, and Asa's first blocker. The table now exists, and it is **derived
  from a pinned bash-5.3 oracle with a ratcheted baseline**, so it is evidence
  rather than assertion and a shape cannot silently change compatibility class.
- *The Normative Superset claim contradicts the unsupported-form diagnostic*
  — Asa's second blocker. Resolved by making the diagnostic class-dependent;
  `package foo`, the counterexample, falls back to shell.

**Still open, and each now blocks its own phase rather than the whole design.**
These were majors, not blockers, and the reason they are recorded here is that
the review was right that they are load-bearing — but each binds at a specific
phase, and pretending otherwise would stall P0/P1 on questions P0/P1 do not ask.

| Finding | Blocks | Why it binds there |
|---|---|---|
| The typed process boundary (inbound capture/decode, outbound encoding) is sequenced at P5, but P1–P4 introduce typed calls, imports, functions, errors and foreign boundaries that need the contract to be coherent. | **outbound: already settled. inbound: P5** | An earlier draft of this row claimed P1 values never cross `execve`. That was wrong — `x := 42` followed by `echo $x` crosses immediately. The **outbound** half is not open: L0 settled it, and `expand.ObjectString` implements it — a value crossing to an external command serialises as JSON, total and non-failing. What P5 still owes is the **inbound** half: explicit capture/decode, and the rule that arbitrary command output is never inferred to be JSON merely because it parses as JSON. P1 needs no inbound decode, so it is genuinely unblocked; the claim that made it look unblocked was not. |
| The concurrency model names task-local shell state but does not specify descriptor, trap, job-control, signal and cancellation ownership precisely enough. Asa adds that inherited pipes and duplicated descriptors sharing offsets are undefined. | **P4, except the fork half at P3** | No P0–P3 construct creates a second thread of control, so goroutine ownership is P4 and the race/leak gate (`sh` todo `7b2d441dd7c8`, p0) is its hard prerequisite. **Corrected by A1b:** the plan also requires that deferred work never silently crosses a fork, and `defer` is P3 — so fork-crossing ownership binds a phase earlier than this row first claimed. See §The three residual findings. |
| P2 assumes arbitrary module, vendor and GOPATH packages execute through a replaceable evaluator, without saying what happens for packages Yaegi cannot interpret — cgo, unsupported features, compiled-only integration. | **P2** | It is a precondition of the evaluator time-box, not a consequence of it: the fallback must be decided before an evaluator is chosen, or the choice will be made by whatever the prototype happened to run. |

None of the three blocks P0 or P1. Each must be closed before its own phase
opens, and the phase gates above are the enforcement point.
