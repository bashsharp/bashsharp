# Sprint 171 W1 gosource-units findings

Authority is the exact upstream Go 1.27 testdir harness. Every row was
measured locally through the harness's own compiled directory route — one
`transpile` per package, `go tool compile -e -D test -importcfg -p test/<pkg>`
on the generated file with upstream's recipe flags, `go tool link` and a run
where the recipe runs — and compared line for line (file:line: message) with
the same route on the unchanged corpus files. Product tests are the
outside-corpus reproducers in `gosource/sprint171_units_test.go`.

**The rows flip only together with the bashy change under "requests to other
seams"**: the harness reaches `gosource.Load` through `bashy transpile -o`,
which does not yet ask for native units.

## Mechanisms

1. `3e0cc57a` gosource: lower an explicit package map one native unit per
   package. `Options.PreserveNativeInit` with `Packages` links nothing: the
   map serves the checker, the program's file keeps its package clause,
   names, imports of mapped packages (relative or not) and init
   declarations. The interpreter's flattening is unchanged.
2. `b88c9ba7` gosource: keep file-level compiler directives that document no
   declaration (`//go:linkname`, `//go:cgo_*` after the last declaration or
   above a type/const), each at its own source position.
3. blank `_`: already excluded from `checkLoweredNames` by `78a1a3fd`
   (Sprint 165 integration, landed 2026-09-13, never leaf-measured; driving
   test `TestSprint165BlankMethods`). `mangleLinkedNames` never renames `_`:
   the blank identifier is not in a package scope. No new change; verified
   `blank.go` checks, transpiles, builds and runs on this tree.

## Rows

| root | mode | first cause | mechanism | status |
|---|---|---|---|---|
| `testdir:closure3.go` | compiled | not the map: the function literal's `can inline main.func1` note moves from line 15 to 16 — the emitter's closure position (lower-1 165 FINDINGS: design C5/C6). Every other note matches. | byte-faithful native function literals | moved to `lower` (emitter), design per 165 lower-1 |
| `testdir:fixedbugs/issue18895.go` | compiled | flattened map renames `t.m` to `__gosource_pkg_0_t.m` | 1 | fixed in `3e0cc57a` (+ bashy request); route matches the original line for line |
| `testdir:fixedbugs/issue19261.go` | compiled | flattened map renames `F` | 1 | fixed in `3e0cc57a` (+ bashy request) |
| `testdir:fixedbugs/issue37837.go` | compiled | flattened map renames `F`, `G` | 1 | fixed in `3e0cc57a` (+ bashy request) |
| `testdir:fixedbugs/issue42284.go` | compiled | flattened map renames `E`, `T` | 1 | fixed in `3e0cc57a` (+ bashy request) |
| `testdir:fixedbugs/issue56280.go` | compiled | flattened map renames `g[go.shape.int]` | 1 | fixed in `3e0cc57a` (+ bashy request) |
| `testdir:linkname.go` | compiled | flattened map renames `ContainsSlash` | 1 | fixed in `3e0cc57a` (+ bashy request) |
| `testdir:fixedbugs/issue24693.go` | compiled | `b.T` embedding `a.T` flattened into one namespace becomes a self-referring type | 1 | fixed in `3e0cc57a` (+ bashy request); runs `ok ok` |
| `testdir:fixedbugs/issue24693.go` | interpreted | same cause in the interpreter's flattened file (`cyclic type declaration`) | the interpreter runs the flat file; a per-package unit is a compiled shape (D4 keeps the interpreted flattening) | not fixed; design — the flat-file rename of an embedded field's type must keep the outer type distinct from the embedded one (`mangleLinkedNames` gives both `T`s the same spelling only when both are mapped and the embedding type is the program's; measure before 174) |
| `testdir:fixedbugs/issue4370.go` | compiled | same as issue24693 | 1 | fixed in `3e0cc57a` (+ bashy request); compiledir route matches |
| `testdir:fixedbugs/issue16616.go` | compiled | `b.U` embedding the type `a.V` next to the variable `b.V` in one namespace | 1 | fixed in `3e0cc57a` (+ bashy request); compiledir route matches |
| `testdir:fixedbugs/bug424.go` | compiled | method sets of two packages' types merged into one namespace make `m` ambiguous | 1 | fixed in `3e0cc57a` (+ bashy request); runs and exits 0 like the original |
| `testdir:fixedbugs/bug424.go` | interpreted | same cause in the interpreter's flattened file | interpreter-side identity of unexported methods across packages | not fixed; moved to `interp`/converter (the flat file has no package-qualified method identity; a per-package unit is not an interpreted shape) |
| `testdir:interface/embed3.go` | compiled | `main.X1 is not main.__gosource_pkg_0_I1`: interface identity lost in the flat file | 1 | fixed in `3e0cc57a` (+ bashy request); route matches |
| `testdir:fixedbugs/issue29919.go` | compiled | `missing a.init`: the mapped package's initializer ran inside main's copy, never as `a.init` | 1 — and bashy must request native units for every `-D` phase, so package `a`'s own object carries its init (see request) | fixed in `3e0cc57a` (+ bashy request); route matches, `a.init` is on the stack |
| `testdir:fixedbugs/issue20014.go` | compiled | `issue20014.dir/a.T.X` expected from field tracking; the flattened module program reports `main.__gosource_pkg_0_T.X` | the module route (`runindir`, no `-D`) builds one generated `main.go` in a temp module; a per-package unit needs the backend to also emit the mapped package `a` as its own unit into that module under its import path | not fixed; moved to backend (bashpp-tests backend + bashy `transpile`): consume per-package units for module programs. sh provides the units (Load per package with `PreserveNativeInit`). |
| `testdir:blank.go` | compiled | stale first line: `T._ declared twice` was removed by `78a1a3fd` | 3 | fixed before 171 (`78a1a3fd`), not leaf-measured; claimed in the leaf TSV |
| `testdir:blank.go` | interpreted | same | 3 | fixed before 171 (`78a1a3fd`); claimed in the leaf TSV |
| `testdir:linkname3.go` | compiled | trailing `//go:linkname` pragmas dropped, gc compiled a unit the original refuses | 2 | fixed in `b88c9ba7`; gc reports the four expected lines (20–23) on the generated unit; does not need the bashy change (single-file `-p=p` route) |

## Canaries (native PASS, same family) measured on this tree through the route

`fixedbugs/issue15071` (rundir), `fixedbugs/issue31637` (compiledir),
`fixedbugs/issue19467` (rundir -l=4), `chan/doubleselect`, `const8`: identical
to the original route. `fixedbugs/bug191` (rundir, two dot-imports of mapped
packages) **regresses with the bashy request unless the `lower` request below
lands** — the emitter keys imports by alias, so the second `import . "./b"`
replaces the first; today the flattening hides it. With both requests applied
bug191 matches the original.

## Known non-name residue (not this lane)

`-m` note columns inside function bodies are off by the generated
indentation (`q.go:6:5` original vs `6:6` generated): each statement's
`//line file:L:C` re-bases the column at the tab. errorcheck matches by line,
so no row depends on it; recorded for the emitter lane.

## Requests to other seams

### bashy `internal/agentos/transpile.go` (W4 / manager) — required for every "+ bashy request" row

```diff
@@ func dispatchTranspile(args []string) int {
             GoVersion: goVersion, TestBuiltins: goTestBuiltins,
             CheckerBranchErrors: goCheckerBranchErrors, CheckAfterSyntaxErrors: goCheckAfterSyntaxErrors,
             Packages: packages, ImportBase: goImportBase, ImportPath: goImportPath, TestMain: goTestMain,
+            // The compiler's own directory route (-D, --go-import-base)
+            // compiles every package as its own unit and links the objects,
+            // so each phase lowers to a native unit (sh gosource:
+            // PreserveNativeInit): an explicit package map only serves the
+            // checker, and the generated file keeps the program's own names,
+            // imports and init for the compiler's -D/-importcfg and the
+            // linker to resolve, as they resolve the original. A module
+            // program (no -D) is still flattened into one file.
+            PreserveNativeInit: goImportBase != "",
         })
@@ func loadTranspileGoSource(in cli.GoSourceInput, base cli.GoSourceOptions) (*syntax.File, *cli.GoSourceProgram, error) {
-    if prog.Package != "main" || prog.Main == "" {
+    // A native unit keeps the source's own main and init; only a flattened
+    // program needs the synthetic entry calls RunMain appends.
+    if prog.Package != "main" || prog.Main == "" || opts.PreserveNativeInit {
         return prog.File, prog, nil
     }
     opts.RunMain = true
```

Why every `-D` phase and not only phases with a map: a lone non-main
package phase (`a.go` of issue29919, bug191) otherwise lowers its `init` to
an uncalled `__gosource_init_0`, and the program unit that now imports it
natively never runs it. Also lift `transpile: --go-library refuses
--go-package` if the package route wants library units checked against a
map; not needed for cluster A.

### `lower/callables.go` (emitter lane) — required to keep bug191 PASS

```diff
@@ func (e *emitter) importDecl(...)
-        e.imports[alias] = p
-        if spec.Alias != nil {
-            e.importAliased[alias] = true
-        }
+        // A dot or blank import binds no identifier, so several may name
+        // distinct paths; key those by path so none replaces another.
+        key := alias
+        if alias == "." || alias == "_" {
+            key = alias + " " + p
+        }
+        e.imports[key] = p
+        if spec.Alias != nil {
+            e.importAliased[key] = true
+        }
@@ func (e *emitter) importLines() string {
-    for _, alias := range aliases {
-        if e.goSource && !e.importAliased[alias] {
+    for _, key := range aliases {
+        if e.goSource && !e.importAliased[key] {
             // The input bound the package under its own name (C8).
-            fmt.Fprintf(&out, "import %s\n", strconv.Quote(e.imports[alias]))
+            fmt.Fprintf(&out, "import %s\n", strconv.Quote(e.imports[key]))
             continue
         }
-        fmt.Fprintf(&out, "import %s %s\n", alias, strconv.Quote(e.imports[alias]))
+        alias, _, _ := strings.Cut(key, " ")
+        fmt.Fprintf(&out, "import %s %s\n", alias, strconv.Quote(e.imports[key]))
     }
```

Verified locally: with it, bug191 through the route matches the original;
a single Go file with two dot-imports is the outside-corpus reproducer to
add beside it.

### backend (bashpp-tests + bashy) — issue20014

The module route must emit the mapped packages as units into the temp
module (`<module>/<import path>/…`) and keep the program's imports; sh
already produces each unit from `Load` with `PreserveNativeInit` and the
earlier packages as the map.
