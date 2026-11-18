# Sprint 162 diagnostics findings

| root | first cause | owner | status |
| --- | --- | --- | --- |
| testdir:fixedbugs/issue11362.go | gc's import-path rules (noder/import.go resolveImportPath, openPackage) live in the importer, rendered by types2 as "could not import P (E)"; ours ran as an aborting pre-checker phase with the bare message | 162.5 here | fixed in 756d134d (gosource: apply gc's import-path rules in the importer) pending leaf evidence |
| testdir:import6.go | line 38 `import "/foo"`: the absolute-path rule (resolveImportPath) was missing, so the fallback importer's wording was printed; as a pre-phase it would also have swallowed lines 17–34 and 39 | 162.5 here | fixed in 756d134d pending leaf evidence |
| testdir:fixedbugs/issue23586.go | gcsyntax accepts; go/parser drops the deferred non-call expression (GoStmt/DeferStmt.Call nil), so go/types cannot report "must be function call" and reports the unused imports/variables instead | 162.5 here | design: needs a source-preserving front end (recorded by ID) |
| testdir:fixedbugs/issue4468.go | gcsyntax reports "must not be parenthesized" and continues (5a759bc7 now type-checks after it, as gc does), but go/parser drops the non-call `go F` / `defer F` expression, so the checker's "must be function call" rows (lines 22–25) cannot be produced | 162.5 here | design: needs a source-preserving front end (recorded by ID) |
| testdir:fixedbugs/issue50372.go | gcsyntax accepts; go/parser reports its own "expected at most 2 expressions" on the range clause and drops the extra variables, so go/types never sees the 3/4-variable range | 162.5 here | design: needs a source-preserving front end (recorded by ID) |
| testdir:slice3err.go | all 24 parser rows were already emitted; the 27 checker rows gc prints after them were not, because Load stopped at any gc diagnostic while gc stops only at a "syntax error" (base.SyntaxErrors, irgen.go checkFiles) and then type-checks its own tree | 162.5 here | fixed in 5a759bc7 (gosource: type-check after gc's non-syntax parser diagnostics, on gc's tree) pending leaf evidence |
| testdir:nul1.go | not a position defect (errorCheck normalises the working-copy path; `class=position` is a partition misread). In-process, Load on the generated tmp__.go is identical to gc line for line (ca51da8e pins it). The leaf's missing UTF-8 rows come from the interpreted program that PRINTS the source: a string argument crossing the native bridge is JSON-encoded (interp/bashpp_native_bridge.go, `json.NewEncoder(s.conn).Encode(q)`) and encoding/json coerces invalid UTF-8 to U+FFFD, so `fmt.Print("\xc2\xff")` writes EF BF BD (os.Stdout.Write([]byte(s)) and print(s) are byte-exact) | 162.3 bridge | moved to 162.3 (request below) |
| package:cmd/compile/internal/abt | testing/internal/testdeps internal import | 162.2 packages | moved to 162.2 |
| testdir:blank.go | lowered package-level blank-name collision | 162.1 interp | moved to 162.1 |
| testdir:convT2X.go | interface comparison representation | 162.1 interp | moved to 162.1 |
| testdir:defernil.go | deferred nil call execution | 162.1 interp | moved to 162.1 |
| testdir:fixedbugs/issue15002.go | collection bounds | 162.1 interp | moved to 162.1 |
| testdir:fixedbugs/issue18661.go | interface comparison representation | 162.1 interp | moved to 162.1 |
| testdir:fixedbugs/issue30606b.go | reflect bridge type assignment | 162.3 runtime | moved to 162.3 |
| testdir:fixedbugs/issue50672.go | method resolution on function value | 162.1 interp | moved to 162.1 |
| testdir:fixedbugs/issue5793.go | complex builtin arity | 162.1 interp | moved to 162.1 |
| testdir:fixedbugs/issue71675.go | recover call lifecycle | 162.1 interp | moved to 162.1 |
| testdir:fixedbugs/issue7740.go | native scalar bridge conversion | 162.3 runtime | moved to 162.3 |
| testdir:index0.go | runtime output/recipe execution | 162.3 runtime | moved to 162.3 |
| testdir:iota.go | iota constant evaluation | 162.1 interp | moved to 162.1 |
| testdir:literal2.go | floating constant evaluation | 162.1 interp | moved to 162.1 |
| testdir:nilptr.go | interpreter allocation growth | 162.3 runtime | design |
| testdir:range.go (interpreted) | range value evaluation | 162.1 interp | moved to 162.1 |
| testdir:range.go (compiled) | lowered parallel assignment | 162.4 lower | moved to 162.4 |
| testdir:typeparam/equal.go | interface comparison representation | 162.1 interp | moved to 162.1 |
| testdir:typeparam/issue50690a.go | multi-result expression conversion | 162.1 interp | moved to 162.1 |
| testdir:typeparam/issue54302.go | interface comparison representation | 162.1 interp | moved to 162.1 |
| testdir:typeparam/mdempsky/13.go | generic bridge argument evaluation | 162.3 runtime | moved to 162.3 |
| testdir:typeparam/subdict.go | comparable type constraint | 162.1 interp | moved to 162.1 |
| testdir:typeswitch1.go | type-switch/default-value evaluation | 162.1 interp | moved to 162.1 |
| testdir:zerosize.go | zero-size pointer identity | 162.1 interp | moved to 162.1 |
| typechecker:go/types/TestCheck/decls2 | checker diagnostic column mapping | 162.1 interp | moved to 162.1 |
| typechecker:go/types/TestCheck/expr3.go | checker diagnostic column mapping | 162.1 interp | moved to 162.1 |
| typechecker:go/types/TestCheck/stmt0.go | checker diagnostic column mapping | 162.1 interp | moved to 162.1 |

## Pass 4 (lane gosource-diag-4): S162.1 gosource rows (story #91)

| root | first cause | owner | status |
| --- | --- | --- | --- |
| testdir:codegen/alloc.go, testdir:fixedbugs/issue65957.go, testdir:heapsampling.go, testdir:fixedbugs/notinheap2.go (form only; its expected rows are gc-only checks, D5) | `return new(struct{})` / `q = new([4]int32)`: a call delivered to the call converter had its type operand converted as a value | gosource | fixed in 7ebe9e68 (gosource: lower anonymous types in expression position) |
| testdir:fixedbugs/issue38125.go, testdir:method7.go, testdir:method4.go, testdir:typeparam/issue51521.go | method expression on a type literal (`struct{ I }.M`, `interface{ m1(string) }.m1`) had no lowering | gosource | fixed in 7ebe9e68 (forwarding closure, as the type-parameter receiver already had) |
| testdir:fixedbugs/issue53982.go | method expression on an instantiated generic type `(*S[K, V]).M` (IndexListExpr in type position) | gosource | fixed in 7ebe9e68 |
| testdir:fixedbugs/issue63489a.go, testdir:fixedbugs/issue63489b.go | the language version did reach the checker (the verdict was right); gc's conf.Error (noder/irgen.go) appends the cause — "(file declares //go:build V)" / "(-lang was set to L; check go.mod)" — which the corpus keys on | gosource | fixed in 2b257ce4 (gosource: name the cause of a language version error as gc does) |
| testdir:genmeth1.go, testdir:genmeth2.go | Go 1.27 generic methods (`(&T{}).m[int]()`, `S[T1, T2]{}.n[T3, T4]()`): a language feature — the front end reports "unsupported call target" first, and the runtime has no generic-method instantiation | gosource + interp | design: a feature, not a repair; recorded by ID |
| testdir:fixedbugs/issue15277.go, testdir:fixedbugs/issue29264.go, testdir:fixedbugs/issue29312.go (and issue20780b's panic) | gosource loads all three. At run time an interpreter collection nested ≳50 levels (100 / 253 in the corpus) is stored through expand.NewObject, whose ValidObject preflight caps the reflect depth (expand/object.go maxObjectDepth) and substitutes expand.invalidObject; the bridge then reports "unsupported interpreter collection value expand.invalidObject" (interp/bashpp_native_values.go). Not a gosource defect; the message prefix is misleading | 162.1 collections | moved to 162.1 (request below) |
| testdir:fixedbugs/gcc61244.go, testdir:fixedbugs/gcc61253.go, testdir:live_regabi.go | gosource limitations outside this lane's assigned set: a type switch with an init statement (`switch i := x; i.(type)`), a select case with a tuple receive assignment to dereferenced targets (`case (*v), (*b) = <-c:`) | gosource | not attempted this pass; recorded for the gosource owner |

Verification of 5a759bc7 beyond the reproducer: every errorcheck root of the Go corpus whose gc verdict carries a non-syntax diagnostic was diffed against `go tool compile -e` (local toolchain); all 19 roots whose result changed match gc line for line (slice3err, goto, label, label1, char_lit1, bug014, bug068, bug136, bug179, bug213, bug217, bug344, issue14540, issue15611, issue30722, issue32133, issue6500, issue7538a, switch4), and gc's one-equal-message-per-line filter also brings const1, shift1, bug198, issue14136, issue3925, issue7129, issue7153 and issue9370 to gc's list. The remaining go/types-vs-types2 wording differences (e.g. issue6572 "2nd function result") are out of this lane. Ordering: in gc-stderr mode diagnostics are now sorted by position as base.FlushErrors sorts them; the checker-test policy keeps parser-first order.

## Requests to other seams

- **162.3 (bridge codec), for testdir:nul1.go:** encode string arguments byte-exactly across the native bridge. `interp/bashpp_native_bridge.go` sends the request with `json.NewEncoder(s.conn).Encode(q)`; encoding/json replaces invalid UTF-8 with U+FFFD, so any Go string holding non-UTF-8 bytes (`"\xc2\xff"`) reaches fmt.Print, os.Stdout.WriteString, … re-encoded. A byte-preserving string encoding in the bridge codec (e.g. base64 for non-UTF-8 strings, or []byte transport) is the general mechanism; the gosource verdict on the resulting tmp__.go is already gc's (ca51da8e). Note gc's scanner skips a bad byte and go/scanner does not, so after the bridge fix the nul1 root may still show one extra `undefined: A` row from `var \xc2A int` (a recovery difference of the go/parser tree, the same class as issue4468).
- **162.1 (collections), for issue15277 / issue29264 / issue29312 / issue20780b:** Go values in gosource mode should not be JSON-preflighted as shell objects (`expand.NewObject` → `ValidObject` → depth cap → `invalidObject{}`); the cap is a design limit of the Bash++ object model, not a knob to raise.
- **Harness lane:** the nul1 row's `class=position` is a misclassification (errorCheck rewrites the working-copy path to `tmp__.go` before matching; the failure is missing/wording).

Manifest movement is owned by the harness lane; the assignments above are its requested destinations.
