# The evaluator seam — measured, and why the evaluator stays in `sh` (Sprint 211, S211.0)

Sprint 211 asked one question first: can the Bash++ evaluator
(`sh/interp/bashpp_*.go` + `gosource_*.go`, 140 files, 39,574 LOC) leave
package `interp` behind ONE exported `interp.Extension` interface of at most
a page? The plan's own rule: *if it cannot, stop and re-plan — that is the
finding.* Measured 2026-09-18 on sh `07e501a0`:

| direction | what | count |
|---|---|---:|
| evaluator → classic | unexported `Runner` fields/methods the evaluator reads (`r.<lower>`) | **49** (`seam-evaluator-reads.tsv`) |
| classic → evaluator | references to Bash++ identifiers from the 77 classic non-test files | **636** |
| classic → evaluator | *distinct* Bash++ identifiers those references name | **204** (`seam-inventory.tsv`) |
| of which | `Runner` struct fields declared in `api.go` | 71 |
| of which | `bashPP*` methods on `*Runner` called from classic code | 73 |
| of which | Bash++ types named in classic signatures/fields | 21 |
| tests | `package interp` (internal) test files importing `lower`/`gosource` | 24 |

Where the 636 sit: `api.go` 254, `runner.go` 170, `builtin.go` 37,
`vars.go` 15, `declaration_policy.go` 14, then signal/setsid/nohup/trace/
task_policy/os_unix at 1–3 each. They are not calls at a few choke points;
they are the engine's own control flow — FIFO publication and reconciliation
in `runner.go`, task cancellation, writer/env overlays, scope push/pop,
decorator chains at function definition, the concurrent-task arm on every
fork. An `Extension` interface covering them is not a page: it is ~150
methods plus 71 fields, i.e. the evaluator's private surface re-stated in
public with no line moved.

**Finding: this is not a move, it is an engine redesign.** Extracting the
evaluator means re-partitioning `Runner` itself (FIFO/task/scope/writer
concerns split into classic and Bash++ halves with a designed boundary), not
exporting accessors. That is Sprint-scale design work with its own Barrier
replays, and nothing in this sprint depends on it.

**Decision (KISS): the evaluator, `lower`, `gosource` and `polyglot` stay in
`sh` under the existing `VSC_PROFILE=cert` gate.** Sprint 211 delivers the
part of "Bash++ is its own language repo" that IS a move: the language's
front door (dialect selector, direct Go-source interface, transpile) becomes
importable `bashpp` packages plus `cmd/bashpp`, bashy imports them, and
bashpp-tests measures the binary. The engine-seam design is one follow-on
card, `todo:af65f24e`, which carries these numbers.

Reproduce the counts:

```sh
cd sh/interp
CLASSIC=$(ls *.go | grep -v _test.go | grep -vE '^(bashpp|gosource)')
grep -ohE '\b(bashPP|BashPP|gosource|goSource|GoSource)[A-Za-z0-9_]*' $CLASSIC | sort | uniq -c | sort -rn   # 204 lines, sum 636
```
