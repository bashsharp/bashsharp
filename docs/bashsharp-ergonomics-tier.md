# Bash# — the ergonomics tier inside Bash++

**Status:** design of record for the tier, 2026-08-31. Sprint 97 story A4.
Subordinate to [`bashpp-posix-superset-syntax.md`](bashpp-posix-superset-syntax.md),
which owns grammar, activation and the committed start sites. Nothing here
authorises implementation; the accepted items are scheduled after P3.

## The question

Go is deliberately minimal. It omits exceptions, default and keyword arguments,
inheritance, comprehensions, operator and method overloading, sum types with
pattern matching, and a REPL. Those omissions are choices, not oversights — but
several of them are things a *shell* audience reaches for daily, and Bash++ is
aimed at that audience.

So: which of them can Bash++ adopt from Python and TypeScript **without
breaking anything**? The operator's constraint is the right one and it is
strict — *natural fit only; if it introduces conflicts, do not consider it.*

## The admissibility test

A construct enters the language only if **all four** hold. Three follow from
the existing design; the fourth is what makes the compiler phase possible.

1. **It has a committed start shape.** A finite token sequence recognisable
   before tokenization, with a near-miss fallback to shell and a
   `command`/quote escape. **Class R** passes free; **Class E** needs a table
   row and a commit signal.
2. **It lowers to ordinary Go.** P7 compiles Bash++ to Go
   (`bashy/docs/bash-plus-plus-compilation.md`). A construct that cannot lower
   forks the language into two semantics and makes the byte-identical
   interpreted-versus-compiled gate **unsatisfiable by construction**.
3. **It is inert under `--posix`** and invisible with the flag off.
4. **It does not re-spell a construct Go already has**
   (`bashpp-posix-superset-syntax.md:457-459`).

Test 2 is the one that decides most cases, and it is derived from the compiler
design rather than invented here. Test 1 is now *measured* rather than argued:
every candidate below carries its class from
[`bashpp-tests/tools/startsites`](../bashpp-tests/tools/startsites/).

## Accept

The five delivered features below retain their existing acceptance. The
2026-09-07 user-approved addition is the bare `agentic` MVP in Sprint 134,
with ordinary-Go lowering in Sprint 117; see
[the action contract and plan](bashpp-agentic-mvp-plan.md). It covers functions,
methods, scripts and command/tool wrappers in the same Bash++ dialect. It is
planned, not an already-passing sixth feature. Numeric rungs are excluded.
The four admissibility tests still apply. An ordinary Go runtime boolean check
satisfies the lowering shape; test 2 does not require all enforcement to erase.

| Feature | Shape | Class | Lowers to |
|---|---|:-:|---|
| **Keyword arguments** | `f(name: "x", retries: 3)` | **R** | generated options struct, or a positional wrapper |
| **Default parameters** | `func f(a int, b int = 3) {}` | **R** | generated wrapper applying the default |
| **Deep immutability** | `readonly obj` — the **existing bash builtin**, extended to freeze `Object` values | E, *no new grammar* | a runtime freeze; no Go source change |
| **Enums + exhaustive `switch`** | `type Color enum { Red; Green }` | E (`type` is already a Day-1 site) | `const` + `iota`, plus a generated exhaustiveness assertion |
| **Null safety** | *no syntax* — a `bashy check` rule | — | static analysis only |

Two of these deserve emphasis.

**Keyword and default arguments are the highest-value pair and cost nothing.**
Highest-value because a shell audience already thinks in named flags —
`--retries 3` is how every command they run takes an option, and positional-only
calls are the thing they complain about in Go. Cost nothing because both
measured **Class R**: `f(name: "x")` and `func f(a int, b int = 3) {}` are
*already* bash syntax errors, so no existing script can contain them and
claiming the shapes is purely additive.

**Immutability is nearly free and fixes a real defect.**
`sh/expand/environ.go` records that "Obj is not deep-copied when a Variable is
copied, so an object is shared, not snapshotted, by an assignment or a
subshell." So a `readonly` that actually freezes an object has a correctness
motive, not merely an ergonomic one — and it needs **no new grammar at all**,
because `readonly` is an existing bash builtin whose semantics simply extend.

## Already shipped — not gaps

- **A REPL.** Go lacks one because it is compiled. **bashy *is* an interactive
  shell**, and imports are already specified as "idempotent and session-scoped
  in the interpreter, making them useful interactively"
  (`bashpp-posix-superset-syntax.md:145-146`). Bash++ hands Go a REPL for free.
  This is a headline for the launch narrative, not a work item.
- **String interpolation.** `${…}` and `$(…)` are the shell's own, and for this
  domain they are better than f-strings or template literals — they interpolate
  *commands*, which is the thing the audience actually wants.

## Reject, with the reason

| Feature | Class | Why |
|---|:-:|---|
| **List comprehensions** `[x*2 for x in xs]` | E | `[` is **triply** loaded: the `test` builtin in command position, a glob character class, and a type-parameter list. The worst collision in the set. `range`-over-func plus shell pipelines already cover map and filter. |
| **Ternary** `a ? b : c` | E | `?` is a glob metacharacter and `$?` is the status variable. Fails test 4 — Go excluded it deliberately, and re-adding it makes a Bash++ program stop reading as Go. |
| **Pattern matching** `match x { … }` | E | A new non-Go keyword → test 4. Go's type switch plus the exhaustive-enum `switch` above delivers most of the value with none of the conflict. |
| **try/catch** | **R** | **Syntactically free** — `try { } catch (e) { }` is already a bash syntax error. Rejected purely on semantics: the design states Bash++ adds no exception handling (`:99-101`), and its only Go lowering is `panic`/`recover`, which Go already spells. Fails tests 2 and 4. |
| **Operator overloading** | — | Cannot lower — a Go reader would misread `+`. Fails test 2. |
| **Method overloading** | — | Go's name resolution forbids it; breaks P7 outright. Fails test 2. |
| **Classical inheritance** | — | No Go target. Fails test 2. |
| **async/await** | — | Duplicate vocabulary for goroutines and channels. Fails test 4. |
| **Optional chaining `x?.y`, nullish `x ?? y`** | E | Go has no such operators → test 4. The value is captured by the null-safety **checker** instead, which needs no syntax. |
| **Decorators `@f`** | E bare / **R** called | Scheduled in Sprint #197 — `@decorator(x)` is Class R and lowers through the shared call contract; bare `@name` remains unclaimed. |

**try/catch is the instructive one.** It is the single most-requested thing on
the list and it is *syntactically free* — no compatibility argument stands
against it. It is refused anyway, because a construct that cannot lower to Go
splits the language, and because Go already spells the escape hatch
`panic`/`recover`. That is what test 2 is for: it stops "it fits" from becoming
"it belongs."

## Bash# is a tier, not a third dialect

**Do not add a `--bashsharp` flag.** Every gate in the design is stated as
*test all four mode combinations* (`bashpp` × `posix`), and that matrix is
already the most expensive part of P0's gate. A third dialect makes it eight,
and each new combination must be tested against the whole compatibility corpus.

Instead, Bash# features enter the **same start-site table**, under the **same
`--bashpp` flag**, admitted by the **same four tests**. The name stays useful as
a label for the ergonomics that are *not* derived from Go — and the "100% Go
compatible" claim survives intact, because test 2 guarantees every Bash#
construct lowers into Go. A Bash++ program using keyword arguments is still a
program a Go compiler can be handed, after lowering.

Stated as a hierarchy:

- **Bash++ ⊇ Bash 5.3** — opt-in syntactic superset; the Class E set is the
  enumerated exception list, each row with a shell escape.
- **Bash++ ⊇ Go** — every Go construct, Go spelling, Go semantics, staged by
  phase.
- **Bash# = Bash++ + ergonomics that must lower to Go.**

## Scheduling

After **P3** (`func`/`defer`), because keyword and default arguments are
function-signature features and there is no signature to extend before then.
Order within the tier:

1. **Keyword arguments** and **default parameters** — Class R, highest value,
   one shared lowering (a generated wrapper).
2. **Deep immutability** on `readonly` — no grammar, and it closes the
   shared-object defect.
3. **Enums with exhaustive `switch`** — a `type` row already exists; the work
   is the exhaustiveness assertion, not the syntax.
4. **Null-safety checking** in `bashy check` — no grammar at any point.

Decorators are being delivered by Sprint #197 against the measured Class R
called forms; the bare form remains outside Bash++.
*(2026-09-15: something did — cross-cutting trace/auth/guardrail concerns.
Researched and scheduled as Sprint #197, design of record
[`bashpp-decorators-and-advice.md`](bashpp-decorators-and-advice.md): the
called form only, a `c *Call` contract, and policy advice instead of a
pointcut DSL. Row 102 above stands; the measured detail that bare `@d`
is E while `@d()` + a compound body is also E lives there.)*

## What this document does not do

It authorises no implementation, adds no row to the committed start-site table
(the accepted shapes are measured but not yet claimed), and does not reopen
grammar decisions owned by
[`bashpp-posix-superset-syntax.md`](bashpp-posix-superset-syntax.md). A Bash#
feature that reaches implementation must first appear in that table like any
other start site, with its class, signal, fallback and escape.
