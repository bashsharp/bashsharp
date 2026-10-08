# Bash++ compatibility claim and implementation decisions

**Decision record:** Sprints 174 and 198, 2026-09-16. Supersedes the 2026-09-15
analysis (preserved in Git history), including its unsupported cumulative
counts and proposed “100%” headings. Sprint 198 implements the syntax contract and leading-package routing.
Sprint198 is delivered. Its exact candidate and verified, bounded regression results are recorded in
`sprint-198-delivery-evidence.json`.

## Language, not implementation: what Bash# does not mimic

**Operator decision, 2026-09-22 (Sprint 249).** Bash# is a programming
language in its own right. Its Go compatibility means **Go's language
semantics**: what a program does. It does not mean reproducing every
feature, diagnostic or artifact of the `gc` toolchain. Bash# does not need to
mimic each and every Go feature, and it does not try to.

The line is the one every alternative Go implementation (gccgo, TinyGo,
interpreters) already draws. The Go specification defines program
behaviour. It says nothing about optimizer reports, generated machine code or
runtime internals; those belong to one compiler, and they change from release
to release.

| In scope: the language (Bash# must match Go) | Out of scope: gc implementation (Bash# does not mimic) |
|---|---|
| Types, values, conversions, constants | Optimizer diagnostics: `-m` inlining and escape reports, `-d=ssa/...` pass output (e.g. `prove`), `-live` liveness, `-d=nil` nil-check removal |
| Control flow, closures, methods, interfaces, generics | Generated machine code and opcode patterns (`asmcheck`, `codegen/`) |
| Goroutines, channels, `select`, `defer`, `panic`/`recover` | gc-only checks: compiler-specific error text, flags and limits |
| Observable output, exit status, run-time panics | Runtime observations: allocation counts, stack layout, finalizer and GC timing (`gc-observation`) |
| The standard library as programs use it | `unsafe` memory reinterpretation and runtime-internal `linkname` dependencies |

Consequences:

- **Accounting.** An upstream test that asserts a gc artifact is not
  applicable to the language. It stays listed by exact root and mode ID with
  its admissible reason (`compiler-diagnostic`, `asmcheck`, `gc-only-check`,
  `gc-observation`, `unsafe-reinterpretation`; see D1 and
  `go-corpus-targets.md`). It is never counted as PASS, never silently dropped,
  and the original denominator is always published next to any adjusted one.
- **Replacement tests.** Where such a test also touches behaviour Bash# does
  own, a replacement conformance test checks that behaviour against native
  Go in interpreted and compiled mode. For example, `prove.go` asserts the
  bounds-proof pass's output; its replacement asserts that a source bounds
  guard controls whether indexing happens.
- **No imitation.** A missing gc artifact is never faked: no synthesized
  expected diagnostic text, no source-path-specific matching, and no running
  the tested source natively to borrow gc's answer.
- **Bash#'s own reporting.** If Bash# ever reports on its own optimisation or
  lowering, that is a Bash# feature with its own flags, messages and tests.
  It is not a copy of gc's text.
- **Interop is not imitation.** Calling native code is an execution boundary,
  not a gc artifact. From Sprint 249, same-package assembly companions
  (including assembly that calls back into interpreted Go) and cgo
  `import "C"` packages run as native companions while the Go source stays
  Bash#-executed. The `assembly-companion` and `cgo` exclusion reasons
  therefore cover only shapes that still refuse precisely. The next barrier
  re-derives them; they are not edited by hand.

Applied in Sprint 249 to `closure3.go` (`-m` inlining), `codegen/switch.go`
(opcode patterns), `live_regabi.go` (`-live`), `nilptr3.go` (`-d=nil`) and
`prove.go` (`-d=ssa/prove`): both modes of each stay FAIL-by-ID with the
decision recorded, and `TestS249CompilerArtifactReplacementConformance` in
`sh` covers the behaviour Bash# owns
(`sh/docs/bashpp-compiler-artifact-contracts.md`).

## Current claim

*Rewritten 2026-10-08 (Sprint 379 S6) from the 2026-10-04 operator decisions
in `bashsharp-design-review-2026-10.md` §6. The figures it replaces are kept
in `claims.md` under "Historical Go figures".*

Bash# has **two Go support surfaces, claimed and measured separately**.

1. **Go in Bash# scripts.** Go mixed into a Bash# script, and a package-led Go
   unit in a `.bsh` file, on stdin or under `-c`, are interpreted by the
   Bash# engine and may be lowered. The supported grammar productions and
   the semantic exclusions are enumerated in the [Go delta table](go-delta.md)
   (`bashy explain go`). Tour of Go (97 programs) and Go by Example (85
   programs) are mandatory script gates, each in three modes, and both pass
   in full on the Sprint 376 candidate; the upstream Go corpus stresses the
   interpreter and reports interpreted and lowered-then-compiled results
   separately, with exact failures and exclusions by root ID. No universal Go
   percentage is inferred from a corpus denominator.
2. **Fenced or embedded Go source.** Go-only source enters through
   `~~~go [as ALIAS]`, `embed go "./file.go"`, a `.go` file or
   `--source=go`, and is **compiled by the provisioned Go toolchain**. No
   interpreted whole-Go-source mode is claimed. Since Sprint 379 (S3, `sh
   2a6d42599`) the entry contract is: Go-only source is compiled from the
   nearest `go.mod` (a missing module is a refusal, never guessed or fetched);
   a `package main` fence or embedded file runs as a program with the script's
   streams and arguments; an alias exposes the exported functions of a
   non-`main` package through the typed bridge, and an alias on a program is
   refused; a `.go` file whose package is not `main` is refused because it
   cannot run as a program; refusals are positioned (`file:line:col`). An
   explicit `--bashsharp` keeps the interpreted route that the corpus harness
   drives. This surface claims Go compiler execution plus Bash#'s entry,
   build and bridge behaviour. A compiled island result is not interpreted
   script credit, and the lowered-then-compiled corpus mode (which lowers a
   *script* to Go) is not an island result.

Neither surface has proved that every valid Go program runs unchanged. Each
publishes its candidate, mode, exact fixture results and exclusions on its
own: `claims.md` holds the numbers, the delta table holds the constructs, and
`bashsharp-tests/tools/go-delta-gate.sh` proves each delta row by fixture.

Entry points. `bashy --bashsharp FILE.bsh` runs script Go. A leading
`package` in `.bsh`, stdin or `-c` text selects the Go-unit parse before shell
parsing, with leading comments, directives and source bytes preserved and
malformed units remaining Go errors; it stays on the interpreted script
surface (it is what the Tour and Go by Example gates measure). No
PATH-dependent dispatch and no parser fallback after a Go error. The
front-door restrictions still apply: Bashy, non-POSIX.

The mixed dialect intentionally changes meanings at enumerated start sites.
Classic Bash/POSIX modes remain separately selected and tested. Reserved
words and escapes must be listed in the syntax contract; a fixture pass count
or a published exception list is not a universal Bash or Go conformance proof.
Formal shell attestation remains Sprint 119's responsibility on its measured
candidate. Sprint 198 publishes fresh evidence for its changed parser.

Under Bash++ activation, unquoted `var const func import package goto` are
reserved, and the syntax contract enumerates binding, update, channel, label,
type and comment start sites. Ordinary classic mode remains available. In
particular, upstream GNU fixtures that define a shell function named `func`
are intentionally rejected under activation. Those raw failures remain visible;
they are not compatibility passes. Spaced `name --` stays a shell command,
while adjacent `name--` is a mixed-program Go decrement.

Mixed-program goroutines currently snapshot ordinary Go bindings. That
measured limitation is distinct from Go-source lexical capture, whose shared
counter fixture is verified separately. No namespace or concurrency redesign
is part of this delivery.

## D1 — definition and exclusions

Use a specification-level, versioned Go profile. Keep cgo, assembly companions,
gc optimizer/debug diagnostics, runtime-internal linkname dependencies and
implementation-specific allocation/finalizer observations explicit where the
existing policy excludes them. An unsupported semantic operation is a defect
or recorded limitation, not automatically an exclusion.

An exclusion needs an exact root/mode ID, reason and decision provenance.
Existing D7 native-memory reinterpretation decisions remain explicit profile
limitations (FAIL by ID), not specification conformance or passing credits.
*Amended 2026-10-08 (operator decision 2026-10-04):* `unsafe` memory
reinterpretation of interpreter-owned values is expressly excluded
(`unsafe-reinterpretation`; delta row U02). Existing tested emulation may
remain; no additional emulation is owed. Whole-package multi-package Go
source is likewise not an interpreted-mode obligation: the compiled path is
the supported one.
Do not exclude whole `runtime`, `syscall` or compiler-related namespaces merely
because their names occur in a failure. Preserve the original blocking
559-root / 661-mode baseline in the reconciliation; exclusions do not become
PASS credits or silently reduce it. Any future adjusted claim denominator
must be published alongside the original denominator and exact exclusions.

## D2 — reuse the typed substrate

Do not commission a new interpreter value model. The current Go-source path
already carries typed scalar/object metadata, structured storage, pointer
identity and native bridge codecs. Shell string projection alone does not
explain every current conversion failure. Classify each remaining failure
against its actual conversion, address, collection or bridge seam and repair
bounded mechanisms using that substrate. See the Sprint 174 causal ledger
and runtime findings for concrete paths and outstanding limitations.

A larger redesign requires evidence that a bounded repair cannot preserve
identity or semantics. It is not part of Sprints 174 or 198.

## D3 — reflection and mutation

Keep ownership/writeback checks until a bounded test demonstrates correct
interpreter/native identity and mutation. Read-only reflection and native
value transport are not general writeback support. Do not remove the existing
refusal globally. A successor may repair a specific bridge operation with an
outside-corpus alias/writeback reproducer and negative ownership tests.
Unsupported reflection remains visible in the profile and failure ledger.

## D4 — claim by surface and mode

*Replaces the 2026-09-16 text, which treated "both modes pass" as one
whole-program claim.* Publish the two surfaces separately (see Current claim)
and, within the script surface, interpreted and lowered-then-compiled results
separately. A root that passes both applicable modes is identified as such,
but a corpus root pass is evidence for the script claim only and never for the
island claim. A repaired interpreted failure earns one mode result; a root
closure requires every originally blocking applicable mode to have an
authenticated passing receipt, with no later contradictory failure.
Historical sampled receipts form a qualified lower bound, not a fresh
whole-corpus result for the current candidate. Unmeasured rows remain unknown.

## D5 — source selection

*Replaces the 2026-09-16 text.* `--source=go` and a `.go` file select the
compiled island route (S3, Sprint 379); they do not select the interpreter.
`--bashsharp --source=go` keeps the interpreted route for the corpus harness.
A leading `package` in `.bsh`, stdin or `-c` text selects the Go-unit parse
and stays interpreted (script surface). Pure Go input keeps Go lexical
semantics, including comments and literals; mixed-syntax shell rules such as
unspaced `x=5` do not apply inside a selected Go unit. No extension-based
fallback after a Go error and no PATH-dependent dispatch. Every refusal names
the file, line and column, and `bashy explain go "<refusal text>"` resolves
it to its delta-table row and workaround.

## D6 — performance

The compiled route is the supported choice for performance-sensitive work:
*(amended 2026-10-08)* CPU-bound loops belong in a Go island (`~~~go` or
`embed go`), while script-visible interpreter overhead remains a performance
obligation. The 11 compute-bound corpus roots that exceed the 60 s limit are
recorded with verified full-scale correct completion in
`interpreted-performance-v1.md`; optimization is post-v1.0.
Do not promise a universal speed ratio or increase leaf timeouts to gain
closure. Keep existing resource limits and evidence. An interpreted timeout
requires a bounded profile/reproducer before choosing a repair; a compiler
resource failure on byte-identical lowered source is a separate limitation.
No general interpreter performance project is authorized by this plan.

## Coordinate and boundaries (Sprint 206, 2026-09-17)

The reviewed Go coordinate is **go1.27.1** (corpus, oracle, SDK, the evaluator's
toolchain allow-list, the stdlib import inventory, the `toolchain` directives).
The Go 1.27.0 language floor in `lower` is a minimum, not the coordinate. The
1.27.1 corpus adds two roots (`fixedbugs/issue80976.go`, `fixedbugs/issue81165.go`)
and modifies one asmcheck file; both denominators are published (3,495 original,
3,497 adjusted).

**Tier 2 — the exposed standard library — is outside this claim.** The bridge
exposes 180 stdlib packages and `bashpp-tests/docs/bridge-corpus/` inventories
their 1,132 upstream test files, but every obligation there (O1–O9) is
`executed = no`: only the native oracle has run those suites. Nothing in the
`go1.27-profile-v1` claim covers executing stdlib test bodies through the
bridge; that is its own backlog card, not a limitation hidden inside the corpus
numbers.

## Evidence and next gate

The authoritative reconciliation is `sprint-174-master-execution-plan.md`,
its failure union, baseline-mode ledger and credit audit. These supersede the
old 91/559, 125/559 and 130/559 rolling totals. Sprint 172 contributes one new
fully passing selected root; Sprint 173 contributes three. Their passing
controls are not additional credits.

Barrier D requires the bounded successor work, exact residual/exclusion
accounting, a frozen published candidate and authenticated full applicable
coverage on that candidate. It must report both modes, native applicability,
all failures/skips/timeouts, survivors and source/binary identities. Historical
samples cannot substitute for that run. Certification follows its own approved
profile and prerequisites; neither Sprint 174 nor Sprint 198 claims it passed.
