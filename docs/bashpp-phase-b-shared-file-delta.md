# Bash++ Phase B — the shared-file delta owed

**Status:** prepared 2026-08-31, Sprint 97 (lane `lintel`). This document exists
to make the Phase B merge request *reviewable as a diff* rather than as a
change. It is the companion to `docs/bashpp-posix-superset-syntax.md`, which
owns the grammar; this one owns only the question **"which certification-owned
lines does Phase B need, and why each one."**

## Why this document exists

`sh/syntax/parser.go`, `lexer.go`, `nodes.go`, `walk.go`, `printer.go` and
`sh/interp/runner.go` belong to the **certification** workstream
(`docs/bashy-three-workstreams.md` §File partition). Bash++ owns the
`LangBashPP` paths only. Those two workstreams pull in opposite directions —
fidelity versus deliberate divergence — on one file set, which is exactly why
the partition is written down.

So Phase B was prepared to touch **none of them**. Go permits types, marker
methods and functions to live in any file of a package, so the entire P1
design — nodes, the start-site decision table, the evaluation arms, and 108
tests — sits in five new files and compiles today:

```
sh/syntax/bashpp_nodes.go            the Day-1 typed nodes
sh/syntax/bashpp_startsites.go       the start-site decision table
sh/syntax/bashpp_startsites_test.go  78 subtests
sh/interp/bashpp_p1.go               the evaluation arms
sh/interp/bashpp_p1_test.go          30 subtests
```

Branch `s97-phaseb-prep` off `f091034a`, commit `8503ac17`.

**It is provably inert, and the proof is structural rather than statistical:**
every new symbol has **zero external references**, so nothing can construct a
node and no code path can reach the evaluation. `TestBashPPMatchesBash` is
still green, so `LangBashPP` remains byte-identical to `LangBash`. A
failure-set diff against `f091034a` shows **zero new failures**.

## The delta, in full

Six insertion points. Each is a `case` arm or a single call; none changes
existing behaviour, and each is inert while the parser dispatch is absent.

| # | File | Site | What is added | Inert without dispatch? |
|---|---|---|---|:-:|
| 1 | `sh/syntax/parser.go` | `gotStmtPipe` (~`:2948`), command position | one `if p.lang.in(LangBashPP) { … RecognizeStartSite(…) … }` guard | — **this is the enabling edit** |
| 2 | `sh/syntax/walk.go` | type switch (~`:173`) | four `case` arms | yes — unreachable node types |
| 3 | `sh/syntax/printer.go` | type switch (~`:1411`) | four `case` arms | yes |
| 4 | `sh/syntax/nodes.go` | the `Command` doc comment (`:251-254`) | four names in a prose list | yes — comment only |
| 5 | `sh/interp/runner.go` | command type switch (~`:7873`) | four `case` arms, one call each | yes |
| 6 | `sh/syntax/tokens.go` | — | **nothing.** P1 adds no token | n/a |

**Five of the six are inert.** Only #1 can change what any input does, and it
is guarded on `LangBashPP`, which nothing selects until `bashy` is wired
(story B1). That ordering is deliberate: it means the shared-file edits can be
reviewed and merged *before* anything can possibly execute them.

### On #4, which is the one to argue about

`nodes.go:251-254` documents which types implement `Command`. The marker
methods themselves are declared in `bashpp_nodes.go` and need no edit; only the
prose list is incomplete without one. It is a comment, so it cannot regress a
fixture — but leaving it stale means the next reader of `nodes.go` gets a wrong
answer to "what implements `Command`". Worth the one line; trivially droppable
if the cert lane would rather carry zero diff in that file.

### What is NOT owed

- **No token additions.** P1's sites are recognized from existing words, so
  `tokens.go`, `token_string.go` and `tokens_parse.go` are untouched and no
  `go generate` run is needed.
- **No lexer change.** The decision is made at the command position from the
  source prefix, within a bounded budget (below).
- **No `expand` change.** `:=` binds through the existing `expand.Object`
  model. Adding a second representation for "a Go value in the shell" is the
  most likely way this phase does lasting damage, and it is refused.

## The bounded-lookahead constraint, and why it is a hard limit

`sh`'s parser is streaming and non-backtracking. This is not a style
preference; it decides what Bash++ can recognize at all.

An earlier attempt to fix an unrelated defect failed three times on exactly
this. Scanning ahead for a matching bracket ran off the end of the buffered
chunk, and the conservative answer at a chunk boundary silently restored the
old behaviour — **a failure that looks exactly like success**, which is the
worst kind. `p.readEOF` measured `false` even when the whole input was already
buffered, and `p.fill()` would have invalidated the slice being scanned.

So every Day-1 recognizer must decide within **64 bytes** of the command
position, and `TestStartSiteBoundedLookahead` asserts it by padding and
truncating each input and requiring the verdict not to move.

**Brace-form `if` is the one Day-1 site that cannot meet this**, and it is
identified now rather than at a third failed attempt. A shell `if` may legally
carry `{` as the last word of its condition and continue with `then`, so only
the absence of `then` **after the matching brace** commits — unbounded by
construction. It is therefore an open design question, not scheduled work.

## Two findings the tests produced by failing

Both were surfaced by a test failing, not by inspection, and both would have
been expensive later.

**1. Start site and supported form are two gates, not one.** A Bash# enum
(`type Color enum { Red }`) was listed as an unsupported form the recognizer
should reject; it claimed it anyway. The recognizer was right — the shape opens
at `type`, which *is* a Day-1 site, and only the body is unsupported.
Consequence: **any construct sharing a start site with a supported form cannot
be rejected by the recognizer at all**, so its shell fallback must be enforced
where the body is parsed. A reviewer checking only the recognizer tests would
conclude that case is covered when it is not.

**2. Complete-form class is not commit-point class.** The corpus in
`bashpp-tests/tools/startsites` measures *"what does bash do with this complete
string"*. The parser needs *"what does bash do at this commit point"*. For
single-line shapes they coincide; for multi-line ones they diverge, and in the
direction that matters:

```
if err != nil { echo a }      complete     → REJECT → Class R
if err != nil {               commit point → ACCEPT → Class E   (with then…fi)
```

Both measurements are correct and describe different strings. The parser
commits at the opening line, so the **commit-point** class governs — and it is
the riskier one, since Class E is what requires an escape and a fallback.

**This makes one row of the design of record ambiguous.** Its `if` row is
spelled with the complete form (`if err != nil { … }`, Class R) but carries the
commit-point class (E). Both facts are true of different strings; the row
conflates them, and an implementer following it would understate the risk. The
row should be split, or re-spelled as the prefix it actually describes.

## What the ack request will name

When Phase B is ready to merge, the request will name exactly:

1. the branch and commit (`s97-phaseb-prep`, `8503ac17` plus the dispatch commit);
2. the six insertion points in the table above, with the diff for each;
3. `make test-bash` 86/86 serial with the flag **off** and **on**;
4. the failure-set diff against the then-current `sh` head, judged on **new**
   failures rather than absolute green;
5. `classify.sh --posix-gate` green, proving no Bash++ grammar reaches the
   certification profile.

## Companions

`docs/bashpp-posix-superset-syntax.md` (the grammar and the start-site table) ·
`docs/sprint-97-bashpp-p0-p1.md` (the sprint) ·
`docs/bashy-three-workstreams.md` (the file partition this respects) ·
`bashpp-tests/tools/startsites/` (the measured corpus).
