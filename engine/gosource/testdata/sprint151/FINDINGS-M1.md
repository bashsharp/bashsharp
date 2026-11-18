# Sprint 151 M1 — running a Go program against the explicit package map

Sprint: #151, Story: #72, Story-ID: 828995f7380b. Bounded spike, findings only;
no product code changed. Fixture: `mapped/main.go` + `mapped/a/a.go` in this
directory (import base `test`, mapped path `test/a`, relative import `./a` —
the convention of `gosource/packages_test.go`).

**Status (implemented, Story #72).** §3 is in `gosource`: `checkDependency`
retains files/Info/package, `Load` links every mapped package into the one
`*syntax.File` ahead of the program (`converter.lowerPackage`,
`checkLinkedNames`, `refuseEmbedDirectives`), the converter collapses mapped
qualifiers (`mappedPkgName`, `qualifier`), and `Program.Packages` lists what
was linked. `packages_test.go` (`TestMapped*`) runs the fixture, init order,
diamond imports and the selector sites through `interp` against `go run`, and
pins the go:embed refusal and the `%T` known difference.

**Name mangling (implemented, Story #72, follow-up).** The collision
diagnostic of §3.4 is gone: `mangleLinkedNames` renames every package-level
object of a mapped package (types, funcs, vars, consts, exported or not) to
`__gosource_pkg_<i>_<Name>` by map index through the converter's existing
`renames` map, so `a.F`/`b.F`, `a.T`/`b.T` and a program name shadowing a
dependency's all link. Methods keep their names (dispatch is by receiver
type, which is renamed); an embedded field of a renamed type takes the
type's rename, since the runtime derives the field name from the type
spelling and the converter spells the field's uses through the field
object. The three `types.TypeString` sites go through `converter.typeString`,
which qualifies a mapped package by a marker and rewrites `<marker>.Name`
into the rename. `checkLoweredNames` guards the result: a duplicate
top-level name in the flat file is a converter defect. The `%T` difference
narrows but stays: a mapped `a.T` prints as `main.__gosource_pkg_0_T`
(the native bridge mirrors the type into its helper under that name;
a display name would be a bridge change). `TestMappedNameCollisionRuns`,
`TestMappedCollidingNamesAcrossTwoDependencies`,
`TestMappedProgramNameShadowsDependency` and
`TestMappedEmbeddedTypeKeepsFieldName` cover it.

**Bottom line.** The map is a *lowering-time* input that today stops at the
type checker; the runtime never sees it and has no field that could hold it.
The smallest honest change is not to teach the runtime about packages at all
but to finish the job the checker started: lower each mapped package with the
same converter, into the same `*syntax.File`, ahead of the program, and
collapse `a.F` into `F`. That is a static link step inside `gosource.Load`,
touches no `interp` file, and leaves the native dependency bridge exactly as it
is. ~2 days. Details and the honest limits below.

## What happens today (verified with a throwaway probe, not committed)

`gosource.Load` with the map succeeds and records
`{From:main Import:./a Path:test/a Origin:package-map Name:a Files:[a/a.go]}`.
The lowered file is:

```
import __gosource_import_0_0 "fmt"
import __gosource_import_0_1 "./a"
func main() { __gosource_import_0_0.Println(__gosource_import_0_1.Greeting(string("mapped")));}
main()
```

`interp.Runner.Run(ctx, program.File)` then fails on the second import:

- relative form (`./a`): `bash++ import "./a": path traversal or absolute paths
  are not allowed` — `interp/bashpp_import.go:628`, before any resolver runs;
- non-relative form (`test/a`): `bash++ import "test/a": package could not be
  resolved` — `go list -e -json test/a` fails, `classifyBashPPPackage` returns
  `capMissing`, `interp/bashpp_eval.go:113`.

Nothing in `a/a.go` is ever lowered: `mapImporter.checkDependency`
(`gosource/packages.go:164`) type-checks it with a `nil` `*types.Info` and
keeps only the resulting `*types.Package`. bashy's refusal at its
`gosource.go:521` is therefore accurate about the engine as it stands.

## (1) Runtime call path: Go `import` → on-disk resolution

Lowering side (where the map *is* consulted):

| hop | file:func |
|---|---|
| `import "./a"` parsed | `gosource/source.go:Load` (parser loop, line 125) |
| explicit packages checked, in order, into `mapImporter.packages` | `gosource/packages.go:(*mapImporter).checkDependency` → `types.Config.Check(spec.Path, fset, files, nil)` (line 164) |
| program checked; the checker asks the importer for `./a` | `gosource/source.go:Load` line 165 `config.Check(programPath, …)` |
| relative join through `ImportBase`, map lookup first, importer fallback second | `gosource/packages.go:(*mapImporter).ImportFrom` (calls `resolve`) |
| the import binding is renamed to `__gosource_import_<file>_<n>`; `c.importAliases[path] = rename` | `gosource/source.go:Load` lines 208–223 |
| `BashPPImport{Alias, Path:"./a"}` emitted, hoisted to the head of `File.Stmts` | `gosource/convert.go:(*converter).importSpec` (line 331); `source.go:273` |
| `a.Greeting(...)` lowered to `BashPPCall{Fun:[alias, Greeting]}` | `gosource/convert.go:(*converter).call`, `callee` closure (lines 547–555) |
| `Program.Importer = imp`, `Program.Resolutions` recorded; **the map goes no further** | `gosource/source.go:Load` lines 176–177 |

Runtime side (where the map is *not* consulted):

| hop | file:func |
|---|---|
| bashy calls `runner.Run(ctx, program.File)` — only the `*syntax.File` crosses; `Program` stays behind | `interp/api.go:(*Runner).Run`, `case *syntax.File` (line ~3160) |
| each top-level stmt dispatched; imports must all precede the first non-import stmt (`goImportsStarted`) | `interp/api.go` lines 3195–3226 |
| `BashPPImport` dispatched | `interp/runner.go:5467` → `(*Runner).bashPPImport` |
| path cleaned; `./a` refused here | `interp/bashpp_import.go:(*Runner).bashPPImport` line 628 |
| resolver invoked | `interp/bashpp_import.go:632` → `r.bashPPTools.eval.Resolve` |
| default evaluator = policy evaluator | `interp/bashpp_eval.go:(*policyBashPPEvaluator).Resolve` (line 233) |
| **on-disk policy**: package facts loaded | `interp/bashpp_eval.go:bashPPGoListFacts` (line 182) |
| GOPATH-mode structural resolution (`go/build` `FindOnly`) | `interp/bashpp_import_context.go:bashPPImportListTarget` (line 14, `resolver.Import` line 33) |
| `go list -e -json <target>` executed in `ModuleDir`/`Dir` | `interp/bashpp_eval.go:187` |
| capability classified; missing/cgo/unreviewed refused; visibility (`internal`, `vendor`) checked | `interp/bashpp_eval.go:classifyBashPPPackage` (139), `interp/bashpp_import.go:validateBashPPImportVisibility` (138) |
| export data pulled for scalar types (`go list -export`) | `interp/bashpp_native_bridge.go:(*Runner).bashPPBridgeRegisterScalarTypes` (795, exec at 801) |
| at the first non-import stmt the **native dependency build** starts: one helper `main` importing every path in `r.bashPPImports`, built with `go build -overlay` and run as a subprocess | `interp/bashpp_native_bridge.go:(*Runner).bashPPStartGoSourceBridge` (785) → `(*bashPPNativeSession).begin` (182) → `bashPPNativeSource` (514, `go list -export` at 518) |
| later, `alias.Greeting(...)` is treated as native because `alias ∈ r.bashPPImports` | `interp/bashpp_p1.go:(*Runner).bashPPCall` (1842) → `interp/bashpp_native_values.go:(*Runner).bashPPBridgeHandles` (18) → `bashPPBridgeCall` (71) |

## (2) Is the checked importer / package map reachable at the refusal point?

**No, and it cannot be without a new carrier.**

- The runtime receives `*syntax.File` only. `Program.Importer` (commit
  `828e5b33`) and `Program.Resolutions` are dropped at the `runner.Run` call.
- `Runner` has no field for it. The import-related state is
  `Runner.bashPPImports map[string]string` (alias→path, populated by
  `bashPPImport`) and `Runner.bashPPTools bashPPToolchain`
  (`interp/bashpp_import.go:69`: `nativeTypes`, `goBinary`, `eval`, `bridge`,
  `moduleDir`). Neither knows a package's source.
- `interp` **must not import `gosource`**
  (`interp/gosource_dependency_test.go:TestGoSourceRuntimeDoesNotDependOnFrontend`),
  so `*gosource.Program`/`PackageSpec` can never be passed in by type; the
  existing precedent is the `interp.GoSourceTestProgram` interface
  (`interp/gosource_testing.go:20`) which carries the AST, package name,
  initializers and `SourceAt` — again no dependency sources.

If a *runtime* design were chosen, the carrier would be a new `RunnerOption`
(`interp.GoSourcePackages(...)`) writing a new field
`bashPPToolchain.mapped map[string]*syntax.File` (path → lowered package),
consulted in `(*Runner).bashPPImport` before line 628. But that design also
needs a runtime package namespace (functions, types, globals, method sets are
all flat today — `bashPPLookupFunc`, `bashPPGoSourceRegisterTypes`,
`bashPPGoSourceDecls`), which is the large change, not the small one.

The data the small change needs is already in the process during `Load` and
just isn't retained: `mapImporter` (`gosource/packages.go:44`) would gain, per
package, the parsed `[]*ast.File` and a `*types.Info` — a
`checked map[string]checkedPackage{spec PackageSpec; files []*ast.File; info *types.Info; pkg *types.Package}`
plus the `[]PackageSpec` order. `checkDependency` line 164 passes `nil` for the
info today; that one argument is the whole reason mapped packages cannot be
lowered.

## (3) The smallest change: link the map at lowering time

Semantics, exactly as the story asks:

- **Refuse anything not in the map** — already true at check time
  (`mapImporter.ImportFrom` refuses relative misses; non-relative misses fall
  to whatever `Options.Importer` the caller supplies — the same importer
  bashy already hands `--check`, so the run and the check see one policy).
  After the change, a
  map-resolved import never reaches the runtime, so it cannot be looked up on
  disk there either.
- **No disk lookup for mapped paths** — the converter does not emit a
  `BashPPImport` for an import whose `PkgName.Imported().Path()` is in the map;
  the runtime `go list` chain in (1) is never entered for it.
- **Mapped packages interpreted from their exact files** — the same converter
  runs over the ASTs `checkDependency` already parsed from the caller-supplied
  bytes (same `token.FileSet`, so positions are distinct and `SourceAt` keeps
  working once `Program.Sources` includes them).

Concretely, in `gosource` only:

1. `packages.go`: `checkDependency` allocates a `types.Info` (same map set as
   `Load` builds at line ~120) and retains `files`, `info`, `pkg` per path.
2. `source.go:Load`, after the program checks clean: for each spec in
   `options.Packages` order, run a converter with `packagePath = spec.Path`,
   that package's `info`, the shared `fset`, the shared `renames` map
   (objects are shared pointers across `types.Info`s because the same
   `*types.Package` is returned by the importer), a unique import-alias
   numbering (`__gosource_import_<pkg>_<file>_<n>`), and collect four lists:
   stdlib/importer imports, type+const decls, funcs (incl. methods), and
   var initialisers in that package's `info.InitOrder`, plus its `init`
   functions. Emit in this order into the single `File.Stmts`:
   *all* imports (every package's, hoisted — required by `api.go:3195`'s
   "imports first, then the bridge starts once" rule and by
   `bridgeImportIdentity` at `bashpp_native_bridge.go:178`), then per package
   in map order decls → funcs → zero-globals → `InitOrder` → `init_N` calls,
   then the program's own, then `main()`.
3. `convert.go`: collapse a mapped-package qualifier at the four places a
   selector on a `*types.PkgName` is rendered: `call`'s `callee` (547–555),
   `expr` `*ast.SelectorExpr` (465), `typ` `*ast.SelectorExpr` (190), and
   `text` (60, an `ast.Inspect` rename pass — drop the `X.` when `X` is a
   mapped `PkgName`). Make the three `types.TypeString` qualifiers
   (`convert.go:144`, `convert.go:355`, `function_values.go:22`) return `""`
   for a mapped path exactly as they do for `c.packagePath`.
4. `source.go`: **collision diagnostic**. The runtime namespace is flat and
   the converter renames only import bindings and `nil/true/false` shadows,
   never types (the `TypeString` sites cannot rename an object). So if two
   packages in the link set — including the program — declare the same
   package-level name (exported or not), `Load` returns
   `gosource: package-level name X declared by both test/a and main; execution
   against the explicit package map requires distinct names`. This is the
   honest boundary of the small change; name mangling lifts it later
   (see risks).
5. `Program`: add `Packages []Resolution`-style listing of what was linked
   (path, name, files) so `--go-list` can print it; keep `Importer`.

bashy's change is then to stop refusing at `gosource.go:521` when
`RunMain` is set: the same `Options` it already builds for `--check` are
handed to `Load`, and `program.File` runs as today. No `interp` edit.

## (4) Native dependency bridge

**Untouched for mapped packages.** A mapped package never appears in
`r.bashPPImports`, so it is neither `go list`ed, nor exported, nor compiled
into the helper (`bashPPNativeSource`), nor dispatched through
`bashPPBridgeHandles`. Its calls are ordinary interpreted `BashPPCall`s
resolved by `bashPPLookupFunc`.

What the bridge *does* see, unchanged in mechanism:

- the mapped package's own stdlib/importer imports (e.g. `fmt` inside `a`),
  hoisted with the program's — same alias→path registry, same single helper
  build. Multiple aliases for one path are already permitted in gosource mode
  (`bashpp_import.go:657`).
- the mapped package's named types, which `bashPPLocalTypeDescriptors`
  (`interp/bashpp_native_local_types.go:84`) discovers by walking
  `r.bashPPGoSourceFile` and mirrors into the helper — as `main.T`
  (`bashpp_native_bridge.go:701`). Functionally fine; identity is wrong, see
  risks.

## (5) Risks

- **Init order across packages.** Go initialises a dependency completely
  before its importer. Emitting in `options.Packages` order is sound because
  `checkDependency` only registers earlier packages, so a later package can
  only import an earlier one; the program comes last. Within a package,
  `info.InitOrder` is the checker's dependency-sorted order, as today.
  `bashPPValidatePackageInitOrder` (`interp/bashpp_p1.go:490`) inspects the
  flat file and is satisfied by construction. What is *lost*: Go runs every
  `init()` of package `a` before any var initialiser of `b`; the emission order
  above preserves that. Diamond imports (`b`→`a`, `c`→`a`, main→`b`,`c`) are
  fine: `a` is lowered once.
- **Package-level vars.** Flat namespace ⇒ the collision diagnostic in (3.4).
  Unexported globals of `a` are visible by name to the program at the shell
  level, but the Go checker already rejected any such reference, so no
  program that loads can observe it. Hazard: a *program* global named like
  an *unexported* dependency global (`var count` in both) collides and is
  refused rather than silently aliased — that is the point of the diagnostic.
- **Method sets across packages.** Methods are `BashPPFuncDecl`s keyed by
  receiver type name; with distinct type names they dispatch as today.
  Embedded types from a mapped package promote through the existing
  interpreted path, not `bashPPPromotedNativeReceiver`. Interfaces satisfied
  across packages work because satisfaction is structural at runtime.
  Two packages declaring the same type name is a collision (refused).
- **Type identity strings.** `%T`, `%v` on a `Stringer`-less struct, panic
  text (`bashpp_panic.go:244`) and the helper's mirrored types all spell the
  package as `main.`; a mapped `a.T` prints as `main.T`. Any corpus program
  that prints a type name from a mapped package will differ from `go run`.
  Fix belongs with name mangling, which needs a rename hook the `TypeString`
  sites don't have today.
- **`go:embed` in a mapped package.** `attachEmbedDirectives` runs on the
  program's files only; a mapped package's embeds would need the same pass
  and `bashPPGoSourceEmbedRequest`'s `SourceDir` assumes one source root.
  Refuse embeds in mapped packages in M1.
- **Positions / diagnostics.** Runtime line numbers come from the shared
  fset, so a panic in `a/a.go` reports `a/a.go:N` correctly once
  `Program.Sources` includes the mapped files; `discardRestOfLine` keys on
  line number only, which is already the multi-file-main situation.
- **Testing session.** `LoadGoSourceTests` uses the same `File`; `_test`
  packages in the map are out of scope.
- **Scope creep guard.** Name mangling (`__gosource_pkg_<n>_<Name>`) would
  remove the collision diagnostic *and* fix `%T`, but needs `TypeString`
  callers to post-rewrite qualified names; it is the M2, not M1.

## (6) Estimate and file list

Estimate: **12–16 h** (≈2 days), of which ~3 h is the collision/ordering test
matrix. No `interp` change; no bridge change.

| file | change |
|---|---|
| `gosource/packages.go` | retain `files`, `info`, `pkg` per checked package; allocate `types.Info` in `checkDependency` |
| `gosource/source.go` | link step: lower each mapped package in order, hoist imports, order inits, collision diagnostic, extend `Program.Sources`; expose linked-package listing |
| `gosource/convert.go` | mapped-`PkgName` collapse in `call`/`expr`/`typ`/`text`; `TypeString` qualifiers return `""` for mapped paths (also `function_values.go:22`) |
| `gosource/packages_test.go` | run the fixture end to end through `interp` (mirrors `runGoSourceRunner`); refusal on collision; init-order test `a`→`b`→main with side-effecting inits; `%T` limitation pinned as a known-difference test |
| `gosource/testdata/sprint151/mapped/` | fixture (this spike) |
| bashy `internal/cli/gosource.go:521` | drop the refusal when `RunMain`; pass the same `Options` (downstream, not this repo) |

Expected effect on the 138 corpus roots: those whose dependency packages
declare no name that also appears in another linked package, and that do not
print mapped type names, should pass outright; the rest fail loudly on the
new diagnostic instead of the old refusal, which is the honest state until
M2 mangling.
