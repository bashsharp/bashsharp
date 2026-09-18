# Bash++ compatibility claim and implementation decisions

**Decision record:** Sprints 174 and 198, 2026-09-16. Supersedes the 2026-09-15
analysis (preserved in Git history), including its unsupported cumulative
counts and proposed “100%” headings. Sprint 198 implements the syntax contract and leading-package routing.
Sprint198 is delivered. Its exact candidate and verified, bounded regression results are recorded in
`sprint-198-delivery-evidence.json`.

## Current claim

Bashy provides a Bash-compatible shell, an opt-in mixed Bash++ dialect, and
an explicit Go-source route. Go support is an **enumerated implementation
profile with separate interpreted and compiled evidence**. Neither mode has
proved that every valid Go program runs unchanged. A compiled path using the
Go toolchain does not prove that its source selection, lowering, package
handling and outputs are universally correct.

`bashy --bashpp --source=go program.go` selects the existing Go frontend.
Mixed Bash++ syntax and a Go compilation unit have different parsing rules.
A leading `package` marker now selects that same Go route before shell parsing
for Bashy command, stdin and file input. Leading Go comments/directives and
source bytes are preserved; malformed units remain Go errors. The existing
front-door restrictions still apply: Bashy, non-POSIX. The explicit flag
remains available; no extension-based dispatch was added.

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

## D4 — claim by mode

Publish interpreted and compiled results separately and identify roots that
pass **both applicable modes**. A repaired interpreted failure earns one mode
result; a root closure requires every originally blocking applicable mode to
have an authenticated passing receipt, with no later contradictory failure.
Historical sampled receipts form a qualified lower bound, not a fresh
whole-corpus result for the current candidate. Unmeasured rows remain unknown.

## D5 — source selection

Retain `--source=go`. Sprint 198 adds the explicit leading-package marker to
Bash++ entry points using the same frontend; it must cover command, stdin and
file input, leading comments/directives and malformed units. Pure Go input on
that route keeps Go lexical semantics, including comments and literals.
Mixed-syntax shell rules such as unspaced `x=5` do not apply inside a selected
Go unit. No PATH-dependent dispatch or parser fallback after a Go error.

## D6 — performance

The compiled route is the supported choice for performance-sensitive work.
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
