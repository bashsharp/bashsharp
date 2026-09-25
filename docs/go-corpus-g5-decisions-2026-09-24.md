# Go corpus G5 decisions — 2026-09-24

Pinned source: Go 1.27.1 `test/` corpus. “Exclude” names only the
failing mode keys recorded in `go-corpus-state-2026-09-24.tsv`; “open” adds no
key. The state table is the observed Sprint-269 result, not a request to
reclassify an upstream fixture.

| root | disposition | evidence |
|---|---|---|
| testdir:asmhdr.go | exclude interpreted — assembly-companion | buildrundir; asmhdr.dir/main.s includes generated go_asm.h and supplies values executed by main.go. |
| testdir:closure3.go | exclude interpreted — compiler-diagnostic; compiled open | errorcheckandrundir -0 -m -d=inlfuncswithclosures=1; interpreter has no optimizer diagnostics, but no reviewed catalog basis excludes the compiled key. |
| testdir:codegen/switch.go | exclude interpreted — asmcheck; compiled open | asmcheck source annotations match architecture-specific generated opcodes; interpreter emits no assembly, but no reviewed catalog basis excludes the compiled key. |
| testdir:fixedbugs/issue22877.go | open | builddir has p.s, but the admissible assembly-companion family is expressly execute-phase; this root only builds. |
| testdir:fixedbugs/issue37513.go | exclude interpreted — assembly-companion | buildrundir; sigill_amd64.s executes UD2, and main.go checks its process output. |
| testdir:fixedbugs/issue47317.go | open | builddir has a.s, but no admissible family proves a build-only assembly input is inapplicable. |
| testdir:fixedbugs/issue50372.go | open | errorcheck asserts ordinary range-variable diagnostics; neither state failure nor source proves a gc-only diagnostic. |
| testdir:linknameasm.go | exclude interpreted — assembly-companion | buildrundir; a_amd64.s calls the Go callback and executes during main. |
| testdir:live_regabi.go | exclude interpreted — compiler-diagnostic; compiled open | errorcheckwithauto -0 -l -live ...; interpreter has no optimizer diagnostics, but no reviewed catalog basis excludes the compiled key. |
| testdir:nilptr3.go | exclude interpreted — compiler-diagnostic; compiled open | errorcheck -0 -d=nil; interpreter has no optimizer diagnostics, but no reviewed catalog basis excludes the compiled key. |
| testdir:prove.go | exclude interpreted — compiler-diagnostic; compiled open | errorcheck -0 -d=ssa/prove/debug=1; interpreter has no SSA proof output, but no reviewed catalog basis excludes the compiled key. |
| testdir:retjmp.go | exclude interpreted — assembly-companion | buildrundir; retjmp.dir/a.s uses assembly RET targets exercised by main.go. |

## Remaining applicable keys

The nine keys below remain applicable. They were checked against the pinned
Go 1.27.1 `cmd/internal/testdir` actions (native `go test cmd/internal/testdir`
passes the applicable selected roots). No upstream fixture was changed, and no
timeout, native source fallback, or broad compiled-mode exclusion was used.

| exact key | first cause and state evidence | decision |
|---|---|---|
| `testdir:closure3.go` / compiled | `errorcheckandrundir -0 -m -d=inlfuncswithclosures=1`; state: `test/closure3.dir/main.go:16:9: can inline main.func1`. The lowered compiled unit does not yet reproduce this checked gc artifact. | Open lowering/action repair. `compiler-diagnostic` is admissible for the already-recorded interpreted key only; this compiled key stays visible. |
| `testdir:codegen/switch.go` / compiled | `asmcheck`; state: `codegen/switch.go:249: linux/amd64/v1: opcode not found: ^CMPL ... $1836345390`. The checked output is the compiler's assembly for the lowered unit. | Open lowering/action repair. Do not add a compiled `asmcheck` exclusion. |
| `testdir:fixedbugs/issue22877.go` / interpreted | `builddir`; `issue22877.dir` contains `p.go` and `p.s` (`#include "go_asm.h"`, `TEXT ·sub(SB)`). State: the backend was handed non-Go input, `compile input "<run>" is not a Go source file`. | Open, scoped builddir harness/source-selection repair: retain Bash++ evaluation for Go input and handle the build action without treating a build-only companion as an execute-phase exclusion. |
| `testdir:fixedbugs/issue47317.go` / interpreted | `builddir`; `issue47317.dir` contains `x.go` and ABI0 companion `a.s`. State: the backend was handed non-Go input, `compile input "<run>" is not a Go source file`. | Open, scoped builddir harness/source-selection repair; `assembly-companion` does not apply because this action does not execute the program. |
| `testdir:fixedbugs/issue50372.go` / compiled | `errorcheck`; state: `test/fixedbugs/issue50372.go:16:19: expected at most 2 expressions`. The fixture expects the ordinary “at most two iteration variables” diagnostic for invalid `range` clauses. | Open source-preserving-front-end repair; no `gc-only-check` evidence exists. |
| `testdir:fixedbugs/issue50372.go` / interpreted | Same `errorcheck` action and same state first line. `go/parser` truncates the malformed range clause before the checker can report the expected diagnostic. | Open source-preserving-front-end repair; do not exclude. |
| `testdir:live_regabi.go` / compiled | `errorcheckwithauto -0 -l -live -wb=0 -d=ssa/insert_resched_checks/off`; state: `test/live_regabi.go:123: stack object x string`. The compiled path must preserve this specific compiler-action result. | Open lowering/action repair. The interpreted `compiler-diagnostic` key is already exact; this compiled key is not. |
| `testdir:nilptr3.go` / compiled | `errorcheck -0 -d=nil`; state: `test/nilptr3.go:253: generated nil check`. The compiled path's checked artifact differs from the pinned source action. | Open lowering/action repair; no compiled exclusion. |
| `testdir:prove.go` / compiled | `errorcheck -0 -d=ssa/prove/debug=1`; state: `test/prove.go:2867: Proved Leq64U`. The compiled path's proof report differs from the pinned source action. | Open lowering/action repair; no compiled exclusion. |

The first two and final three compiled rows require a narrowly reproducible
lowering/action investigation before implementation. The two `builddir` rows
require the harness to distinguish Go source selection from assembly inputs;
the `issue50372` pair requires the already-recorded source-preserving front
end work. None supplies one of the seven reasons for another exact exclusion.
