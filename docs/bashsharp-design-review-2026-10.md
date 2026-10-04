# Bash# design review: Go in scripts and Go source as an island

**Operator decision requested, 2026-10-04 · Sprint 378 story 2.** This is a proposal, not a change to the executable or to the current claim. Counts below are snapshots from different barriers; they must not be added together or represented as a new full run.

## 1. Goal and disagreements in the existing documents

The product goal is to let a shell script use Go where shell has no corresponding language facility. `why-bashsharp.md` calls Bash# “the bash you already know, Go where you need types, any fenced language where you need a library” and says to “Put the ten lines of Go in the shell script: typed `func`, structs” (`docs/why-bashsharp.md`, “The one-screen version” and “The script that outgrew bash”). `bashpp-posix-superset-syntax.md` says the opt-in dialect “adds Go-shaped structured programming while retaining shell commands, pipelines, expansion, and exit status” (§1). The operator's October goal is stronger: Bash# is a superset of bash and should strive for 99 percent of sensible Go language constructs **within a script**, with a simple workaround for each missing construct. That is a target, not a measured percentage.

The existing scope statements disagree. `bashpp-import-resolution.md` §3 states: “Bash++ is a superset of bash *and* of Go — a bash script runs unchanged, and a Go module runs unchanged.” `bashpp-go-implementation-claim.md` instead says Go support is an “enumerated implementation profile with separate interpreted and compiled evidence” and that neither mode has proved every valid Go program. `claims.md` calls the corpus “Go 1.27.1 mixed and whole-program support” and requires both modes for PASS (§“The proof”), whereas `why-bashsharp.md` says “If you are writing Go anyway, write Go.” The first formulation makes whole Go modules an interpreter obligation; the latter frames Go as an addition to scripts. This review recommends the latter boundary for execution while retaining a broad Go language obligation inside Bash# scripts.

There is also a current implementation mismatch: `bashpp-posix-superset-syntax.md` selects a leading `package` unit for the Go-source frontend, and `bashpp-go-implementation-claim.md` D5 retains `--source=go`. `sh/docs/bashpp-p2-evaluator-decision.md` used “interpreted” for shell execution with toolchain-backed package calls, while the later Go-source corpus uses it for interpreting Go package bodies. The two uses must be named separately. `docs/fenced-go-and-shell-dialect-plan.md` promises a compiled `~~~go` worker, but the current worker accepts exported top-level functions with a narrow signature surface (`sh/docs/bashpp-polyglot-fences.md`, “Go fences”); it cannot yet take arbitrary whole Go programs through the same entry shape.

## 2. Shell gaps that Go fills

| Shell shortcoming | Go construct in a Bash# script | Small example |
|---|---|---|
| Shell variables have no checked nominal types | typed bindings, types, structs, methods | `type Job struct { ID int }; func (j Job) Key() int { return j.ID }` |
| Positional strings do not express structured API contracts | typed functions, interfaces, generics | `func First[T any](xs []T) T { return xs[0] }` |
| Shell arrays and text streams do not provide Go's collection semantics | arrays, slices, maps, `range` | `counts := map[string]int{"ok": 1}` |
| Shell arithmetic has limited typed numeric behaviour | Go integers, floats, conversions and constants | `n := uint64(1) << 63` |
| Shell has no lexical closure or typed callback model | closures and function values | `next := func() int { n++; return n }` |
| Jobs and pipes do not provide shared typed concurrency | goroutines, channels, `select`, `sync` | `ch := make(chan int); go func() { ch <- 1 }()` |
| Exit status is too small for cleanup and error propagation | `defer`, `(T, error)`, `panic`/`recover` | `defer file.Close()` |
| Stringly external commands are awkward for libraries | imports and the standard library | `json.Marshal(value)` after `import "encoding/json"` |
| Shell quoting and expansion do not provide typed addressability | pointers and addressable values | `p := &items[0]` |

These are examples of the intended Go language surface, not assertions that every example currently passes as mixed top-level syntax. `bashsharp-ergonomics-tier.md` adds shell-oriented default/keyword arguments and an interactive REPL to the rationale; these are Bash# additions, not Go compatibility requirements.

## 3. Three execution cases, current and proposed

| Case | Today | Proposed boundary |
|---|---|---|
| `~~~go as g` inline fence, or `embed go "./path.go" as g` | Bash# prepares a Go worker with a provisioned/project Go toolchain; exported functions are called through a typed JSON bridge. Current Go fence accepts imports/functions, not a package clause, methods, top-level values/types, variadics or arbitrary result shapes (`sh/docs/bashpp-polyglot-fences.md`). | Keep toolchain compilation. Extend the Go island adapter for whole-source program entry and package/module files while preserving the callable-function form. Explicitly document the entrypoint and module resolution. |
| Go constructs among shell statements in a Bash# script | The mixed parser commits at enumerated start sites; `interp` executes them. `bashy transpile` can lower the script to Go for compiled execution. Ordinary shell commands and expansion remain shell operations (`docs/bashpp-posix-superset-syntax.md`; `docs/lowered-runner-fences.md`). | This is the broad interpreted Go-language support target. Tour of Go and Go by Example programs are **Bash# engine tests**, treated as scripts, run interpreted, and held at 100 percent. Their Go-looking text does not reclassify them as whole Go-only source. |
| Go-only source run as a program in its own right, including a module with multiple packages | `bashy --bashpp --source=go` or leading `package` routes to the Go-source frontend; the corpus has interpreted and lowered/compiled modes (`docs/bashpp-go-implementation-claim.md` D5; `docs/go-corpus-targets.md`). | Bring the source into a Bash# script with `embed go "./path.go" [as alias]` or an inline `~~~go [as alias]` … `~~~` body, then compile with the provisioned toolchain. There is **no separate interpreted whole-Go-source mode**. A direct Go-only CLI invocation should either be explicit sugar for this route or give a positioned migration diagnostic; the operator must choose its CLI spelling. |

The documented embed path is a double-quoted `./` or `../` literal resolved relative to the script; `embed` and fence openers are recognized at column 1 in Bash# (`sh/docs/bashpp-polyglot-fences.md`, “Embed”). The proposal requires more than relabeling: the existing Go fence worker's limited declaration/bridge contract must gain program/package compilation, including module context and `main` entry where applicable. The toolchain remains provisioned and project-aware (`docs/fenced-go-and-shell-dialect-plan.md`; `docs/bashpp-polyglot-environments.md`). Neither an interpreted script gate nor a compiled source gate may borrow the other's pass credit.

## 4. Options for Go-only source

| Option | Benefit | Cost |
|---|---|---|
| **A. Treat as a Go island and compile (recommended)** | One rule for whole C, C++ and Go source; native Go package semantics and bounded runtime; frees the script interpreter to serve the actual mixed-script use. | Extend fence/embed entrypoints beyond current exported-function bridge; migrate `--source=go`, leading-`package` and corpus harness expectations; report compiled evidence separately. Tour and Go by Example remain interpreted Bash# scripts at 100 percent. |
| **B. Keep whole-Go interpretation as best effort** | Preserves an exploratory route and some existing tests. | Users cannot infer a dependable support claim; multi-package evaluation, unsafe layout and CPU cost remain open. Best effort must never be the default or counted as script conformance. Tour and Go by Example still remain mandatory interpreted script gates. |
| **C. Keep the three current modes** | Least immediate migration work and continuity of historical two-mode corpus scores. | Continues treating interpreter execution of Go-only compiler packages as a product requirement. The interpreted mode carries every remaining Sprint 374 corpus failure while compiled has zero; `cmd/compile/internal/ssa` needed seven engine fixes in one day and still has tests over four minutes. It spends capacity on whole-source emulation beyond the stated script purpose. Tour and Go by Example remain interpreted regardless. |

Recommend A. The Sprint 374 outcome is supplied operator evidence, not a newly verified barrier in this review. `sh/docs/bashpp-multi-package-execution.md` describes the 26-package interpreted design cost; `sh/docs/bashpp-interpreter-per-call-cost.md` shows that a compiled fallback would yield no interpreted credit under the old scoring rule. A changes that rule openly rather than relabeling a compiled result as interpreted.

## 5. Interpreted gate under A

The required interpreted gate contains all Bash# scripts that exercise mixed shell/Go semantics, **all Tour of Go and Go by Example programs as Bash# engine tests at 100 percent**, focused language fixtures for the support line below, and applicable single-file `testdir` run programs as a stress tier. A Tour/GbE failure is a gate failure, even if a compiled Go island passes. The native Go oracle remains useful for expected observable behaviour; compiler artifact checks are not script-language tests.

The 26 `package:` multi-package roots leave the **interpreted** gate and enter the compiled Go island gate; 25 already passed compiled at the older `go-corpus-targets.md` snapshot. Whole Go-only sources, including other package roots, likewise stop receiving interpreted PASS/FAIL keys. The approximately 11 compute-bound roots named in §11 leave the interpreted deadline gate and are recorded individually as compute-bound exclusions with a fence workaround; the five script-visible overhead roots stay. Compiler diagnostics, assembly checks/inputs, gc-only checks, runtime observations, unsafe reinterpretation and cgo leave interpreted applicability by explicit reason and ID, without becoming passes. This changes denominators: publish original root/mode totals, changed applicability, exact IDs and new compiled/island totals side by side. Do not reuse the old “both modes PASS” percentage as a new script score (`docs/claims.md`; `docs/go-corpus-targets.md`; `docs/go-corpus-exclusions.tsv`).

## 6. Proposed claim edits, without editing the claim documents

For `docs/claims.md`, replace the single “Go 1.27.1 mixed and whole-program support” row with two independently measured rows. Proposed wording:

> **Go language constructs in Bash# scripts:** The opt-in mixed dialect supports the Go productions listed in the Bash# combined grammar and the Go delta table. Tour of Go and Go by Example are mandatory interpreted Bash# script gates at 100 percent. Applicable single-file `testdir` run programs stress the interpreter. Report interpreted and lowered-script results separately, with exact failures and exclusions; no universal Go percentage is inferred from a corpus denominator.

> **Fenced or embedded Go source:** Go-only source enters through `~~~go` or `embed go` and is compiled by the provisioned Go toolchain. Report a separate compiled island gate, including multi-package programs and module resolution. This claim covers Go compiler execution plus Bash#'s entry, build and bridge behaviour; it does not imply that the Bash# interpreter executes a whole Go module.

The `claims.md` proof row, its “PASS means both Bash# modes” sentence, and the “six island languages and `--source=go`” toolchain row would need corresponding scope and denominator revisions after implementation. Preserve their dated historical figures as historical evidence, with a new measurement rather than rewriting history.

For `docs/bashpp-go-implementation-claim.md`, replace “Current claim,” D4 and D5's whole-source interpretation with this proposed wording:

> Bash# has two Go support surfaces. Mixed Go in a Bash# script is interpreted by the Bash# engine and may be lowered; its supported grammar productions and semantic exclusions are enumerated in the Go delta table. Go-only source is a fenced or embedded island compiled by the provisioned Go toolchain; no interpreted whole-Go-source mode is claimed. `--source=go` and leading `package`, if retained, route to the same compiled island semantics. A compiled island result is not interpreted script credit. Both surfaces publish candidate, mode, exact fixture results and exclusions separately.

Retain the document's principle that Go language behaviour is in scope while gc artifacts are not. Amend D1 so unsafe memory reinterpretation is expressly excluded, current tested emulation may remain, and no additional emulation is owed. Amend D6 to say CPU-bound loops use a Go island while script-visible interpreter overhead remains a performance obligation. The existing D2/D3 repair obligations for script-shaped typed values and reflection on program-owned values remain; they should be assessed against the new gate.

## 7. Questions requiring the operator's decision

1. Should direct `bashy --source=go file.go` and leading-`package` input remain as documented sugar for a compiled `embed go`, or should they produce a migration diagnostic? Either choice must remove the implied interpreted whole-source route.
2. What is the program entry contract for a whole-source Go island: run `main`, expose selected functions, or support both with an explicit marker? How do multiple files and modules enter through `embed` without accidental dependency discovery?
3. Is the 99 percent goal a design aspiration only, or should it have a measured denominator of Go specification productions? The proposed table can count productions but semantic conformance still needs behaviour fixtures.
4. Which baseline/date should become the first authoritative recalculated corpus ledger? The current named sources use different Sprint 206/269/374 counts.
5. Should `unsafe.Pointer` pass-through in a native import be a specifically supported mixed expression even when interpreter-owned memory reinterpretation is refused? This review proposes yes, with an ownership diagnostic at the boundary.
6. Does the operator approve the proposed single-source claim table, combined grammar as specification/test oracle, and Sprint 376 rescope together? These are separable decisions if implementation must be staged.

## 8. Support line and exclusions for one decision

The line is drawn by what a script author writes, not by every file in the Go test suite. **Support as Go language behaviour in mixed Bash# scripts:** types, structs, methods, interfaces, generics and closures; goroutines, channels, `select`, `defer`, `panic`/`recover` and `runtime.Goexit`; pointers and addressability including `f().field[i]`; maps, slices, arrays, strings and numeric edge cases; standard-library imports as scripts use them (`fmt`, `strings`, `sort`, `sync`, `os`, `time`, `encoding/json`, and `reflect` on the program's own types); and `unsafe.Sizeof`, `unsafe.Offsetof`, `unsafe.Alignof`, plus `unsafe.Pointer` passed unchanged to an import. The last item does not promise interpreter-owned raw-memory aliasing. The passing bar is Tour of Go and Go by Example at 100 percent interpreted, plus applicable single-file `testdir` run programs as stress tests. A missing supported semantic operation is a defect, not an automatic exclusion.

**Exclude from interpreted script applicability, by exact root/key and reason:**

| Family (Sprint 374 operator catalog) | Reason | What a script author does instead |
|---|---|---|
| Compiler diagnostics (104 rows) | Asserts gc optimizer/error report wording rather than program behaviour | Use `go build` or compiler diagnostics on a Go island; test observable behaviour in the script. |
| Assembly checks (74) | Asserts generated instruction patterns | Put the relevant Go in a fence/embed and inspect its compiled artifact with Go tooling. |
| Assembly inputs | Native `.s` companion has no interpreted instruction model | Compile Go and assembly companions together via a Go fence/embed. |
| gc-only checks (29) | Depends on gc-specific flags, limits or diagnostic text | Use the provisioned Go toolchain on fenced/embedded Go. |
| Runtime observations (25) | Allocation counts, GC/finalizer timing and stack layout reflect the interpreter host, not the guest program | If the measurement matters, compile it in a Go fence/embed. |
| Unsafe memory reinterpretation (28) | Interpreter-owned values lack a native byte-layout contract | Compile that operation in a Go fence/embed. Keep existing tested emulation; add no more. |
| cgo (16) | Requires C compilation and native ABI outside mixed interpreter semantics | Put the C source in a `~~~c`/`embed c` island and use a compiled Go island for Go/C package integration where supported. |
| Whole multi-package programs (26 package roots in older catalog) | A module's package graph is whole Go source, not mixed script text | Use `embed go` or `~~~go`; compile with the provisioned Go toolchain. |
| Compute-bound single-file runs (about 11; §11) | Interpreter call/loop overhead overwhelms the fixed leaf deadline, without indicating script-visible overhead | Put CPU-bound loops in a Go fence/embed. |

The older `go-corpus-targets.md` lists 103 diagnostics, 22 gc observations and 26 unsafe rows; the operator's Sprint 374 catalog says 104, 25 and 28. These are **different snapshots**, not corrected totals. Reconcile exact IDs before publishing a new denominator. `go-corpus-exclusions.tsv` is the older keyed ledger and must not be silently rewritten from these rounded/newer family counts. Every refusal must include source position, a stable diagnostic code, and a direct workaround, normally “use a Go fence or embed.”

The short list of **Go source constructs not supported mixed into shell text** under this proposal is: a `package` clause and whole multi-file package graph (it changes the source unit; embed or fence the program); a backtick raw-string import path at a mixed import start site (backticks are shell substitution; use a double-quoted import path or a Go island); `import "C"` and cgo directives (native C ABI; use compiled C/Go islands); assembly companion inputs (native instructions; compile them with a Go island); and unsafe pointer arithmetic/reinterpretation of interpreter-owned storage (no guest native layout; use a Go island). gc optimizer directives and runtime allocation/GC observation tests are also outside the **interpreted claim**, because they ask about a particular compiler/runtime rather than a Go source production; use Go tooling on a compiled island when needed. Ordinary `unsafe` size/alignment operators and passing `unsafe.Pointer` through a native import remain supported. This is a proposed target exception list, not a statement that all other Go constructs are already implemented today.

## 9. One source of truth, two renderings

Adopt a versioned, structured **Go delta table** as the normative support ledger. Each row identifies the Go specification section and grammar production, mixed-script status (`supported` or `excluded`), reason, workaround, Bash# diagnostic code where refusal is possible, and a fixture that proves acceptance or refusal. A supported row may name a differential Go oracle fixture; exclusion fixtures must check position and workaround. The table describes *differences from ordinary Go* plus the mixed entry sites where Bash# commits, so “other Go productions follow Go” stays short for readers. Generate both a one-page human “Go in Bash#: delta from Go” and a TSV/JSON artifact shipped with bashy, reachable through a stable CLI such as `bashy explain go --json`. Generate the combined grammar's row links from the same IDs. CI checks regeneration and fixtures, preventing prose, agent data and diagnostics from drifting. This is proposed infrastructure, not a claim that it exists now.

Ten illustrative source rows (production names are proposal keys to be aligned to the pinned Go specification and grammar during implementation):

| ID / Go spec § / production | Mixed status | Reason | Workaround or use | Diagnostic / proving fixture |
|---|---|---|---|---|
| G01 / Declarations / `TypeDecl` | supported | Typed data belongs in scripts | `type Job struct { ID int }` | — / `mixed/type_struct.bsh` |
| G02 / Declarations / `MethodDecl` | supported | Methods on script types | Declare receiver method | — / `mixed/method.bsh` |
| G03 / Types / `TypeParameters` | supported | Generic script helpers | `func First[T any]` | — / `mixed/generic.bsh` |
| G04 / Statements / `GoStmt` | supported | Typed concurrency | `go f()` in Go context | — / `mixed/goroutine.bsh` |
| G05 / Expressions / `Address` | supported | Go addressability, including `f().field[i]` | Use an addressable element | — / `mixed/address_result.bsh` |
| G06 / Source file / `PackageClause` | excluded | A package unit is whole Go source, not an in-script declaration | `embed go "./main.go"` | `BASHPP-EGO-PACKAGE-ISLAND` / `mixed/package_refusal.bsh` |
| G07 / Imports / raw-string `ImportSpec` | excluded | Backquotes have shell command-substitution meaning at mixed start sites | `import "fmt"`; or a Go island for verbatim source | `BASHPP-EGO-RAW-IMPORT` / `mixed/raw_import_refusal.bsh` |
| G08 / Unsafe / pointer reinterpretation expression | excluded | No native layout for interpreter-owned data | Go fence/embed | `BASHPP-EGO-UNSAFE-LAYOUT` / `mixed/unsafe_layout_refusal.bsh` |
| G09 / Imports / `import "C"` | excluded | cgo requires native C toolchain and ABI | C and Go islands | `BASHPP-EGO-CGO` / `mixed/cgo_refusal.bsh` |
| G10 / Source file / `GoAsmCompanion` (build input, not Go grammar) | excluded | `.s` requires native assembly | Go fence/embed with companion file | `BASHPP-EGO-ASM` / `island/asm_companion.bsh` |

The sample codes and fixture paths are proposed, not existing engine output. A table row for a compiler diagnostic or gc observation belongs to a separate **corpus applicability** table because those are test demands, not Go grammar productions. The agent rendering should still link these applicability IDs when reporting corpus refusals. The human rendering lists the short mixed-script exception list and writes the workaround beside each; it does not ask a Go programmer to read a corpus ledger.

## 10. Combined shell plus Go grammar

Define Bash# as the union of GNU Bash 5.3 shell productions, the supported Go productions, and explicit boundary tokens. The shell half cannot be imported from `antlr/grammars-v4`: the 28,581-file tree (checked by the sprint conductor on 2026-10-04) has a Go grammar but no Bash/sh grammar, and issue 1403 remains open. Candidate shell bases are the yacc grammar in the POSIX Shell Command Language standard (a portable core that must be extended to Bash 5.3), a grammar transcribed from `sh/syntax`'s hand-written production parser with behaviour fixtures, or the small MIT `endvroy/antlr4_bash` grammar after a coverage/license review. Do not copy GNU Bash `parse.y` into the permissive tree: it is GPL. Use the Go grammar from `antlr/grammars-v4` as a starting reference, then pin its revision and reconcile it to the reviewed Go version. ANTLR4 is BSD-3-Clause; a similar grammar tool is acceptable if it can express the required lexical modes and reproducible tests.

The shell grammar owns commands, lists, pipelines, functions, compound commands, redirections, substitutions, words and expansions. At **enumerated start sites** it switches to Go: typed `func`/method/function literal; `type`, `var`, `const`, `import` declarations; identifier-list `:=`, spaced assignments and compound updates; `++`/`--`; address/dereference and selector/index expressions; `ch <- v` and `<-ch`; labels and `goto`; and Go control statements within Go function bodies. Supported Go subproductions include declarations, types (arrays, slices, maps, structs, interfaces, pointers, functions, type parameters/arguments), expressions (literals, composite literals, calls, conversions, selectors, indexing/slicing, operators), and statements (`if`, `for`, `range`, `switch`, `select`, `go`, `defer`, `return`, `break`, `continue`, `fallthrough`, `goto`, send, assignments and declarations). The grammar must distinguish the shell `go build`, shell `return 1`, unspaced `x=5`, shell backticks and `func` command forms from committed Go forms; the exact compatibility/start-site table in `docs/bashpp-posix-superset-syntax.md` is the seed. Top-level Go `if`/`for` among shell commands is still listed as planned in `docs/why-bashsharp.md`, so its grammar row must be marked planned until shipped.

Add lexical modes for `~~~go [as alias]` through closing `~~~`, `~~~bash`/`~~~sh`, other existing fence types, and the column-1 `embed <type> "./path" [as alias] [!runner]` directive. The fence body is opaque to the mixed grammar and is handed to its language adapter. A leading `package` is recognized as a whole-source boundary token; under option A it cannot silently switch into an interpreted Go compilation unit. Shell-only keywords, `//` comments and quoting need mode-specific rules. The Go delta table links each mixed production or refusal to a fixture.

Use the combined grammar first as a **normative specification and test oracle** for the Bash# extension layer: parse fixtures, rejection positions, ambiguity/escape cases, and differential comparison with `sh/syntax`. This gives a precise contract without replacing the production parser. Replacing `sh/syntax` wholesale is a separate, high-risk project: its hand-written parser embeds shell word expansion, contextual tokenization, error recovery, streaming and Bash compatibility decisions; an ANTLR parse tree would require AST/API mapping and performance proof. A formal grammar can expose gaps and generate tests before that migration is justified. “All of bash plus nearly all Go” is the design target; the short exclusions and current shipped/planned distinctions remain explicit until demonstrated.

## 11. Rescope the performance sprint

Rescope Sprint 376 from “make every carried interpreted Go-only root pass” to **make script-shaped code fast enough** under the existing resource limit. From the 16 carried performance roots identified by the operator, move these approximately 11 pure compute runs to the new keyed `compute-bound` exclusion family: `64bit.go`, `divmod.go`, `ken/divconst.go`, `ken/modconst.go`, `abi/uglyfib.go`, `abi/fibish_closure.go`, `copy.go`, `stack.go`, `issue13169`, `issue59680`, `issue78081`. The workaround is a compiled Go fence/embed for the CPU-bound loop. Keep their original failures and measured timeouts visible in historical ledgers; do not turn them into PASS.

Keep the five roots that expose overhead a script author can feel: bridged `sync` calls (`ken/chan.go`, `issue79186`), callback round trips (`issue39541`), memory proportional to live data (`issue80188`), and array copies (`issue20780b`). Profile these by mechanism and maintain a collector-pacing account, with a representative script fixture and the same deadline. The older `sh/docs/bashpp-interpreter-per-call-cost.md` showed that frame pooling and map replacement alone would not meet a fib leaf's bound; this rescope avoids converting that compute-heavy observation into a blanket promise about interactive or automation script speed.

## Reference

The exact rows behind the family counts in section 8, the nine interpreted package roots, the type-checker lane accounting and the Tour of Go and Go by Example measurements are in `go-corpus-state-2026-10-04.md` and its keyed table `go-corpus-state-2026-10-04.tsv`; the 21 exclusions decided in Sprint 374 are in `go-corpus-exclusions-2026-10-04-proposed.tsv`.
