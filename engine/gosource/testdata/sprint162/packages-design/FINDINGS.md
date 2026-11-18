# S162.2 step 1 — lane `packages-design` findings

Design lane (no product code). Every row is a `package:` root of the Barrier B
manifests (`active-151-manifest.tsv` 9, `active-retained-manifest.tsv` 16,
`active-unclassified.tsv` 1). The design note is
`docs/bashpp-multi-package-execution.md`; the spike record is
`docs/evidence/sprint162-abt-spike.md`.

First cause, read from the rows (both modes carry the same first line); the
"library transpile" facts per row are the spike's S step on the leaf host
(`docs/evidence/sprint162-abt-spike.md`):

- **F1 — identity-less `internal` visibility** (`lower/module_importer.go:
  checkInternalVisibility`, caller identity derived from `Options.Dir` = the
  `_testmain.go` scratch directory instead of the `-p` identity the map
  carries; `gosource` never passes it). Fix seam: `gosource` + `lower` (+ the
  runtime resolver in `interp/bashpp_import.go`). Mechanism: identity-keyed
  visibility with `cmd/go`'s testmain exception as an explicit option.
- **F2 — reviewed-stdlib inventory excludes every `internal`/`cmd` path**
  (`syntax/go127stdlib_generated.go`; `interp/bashpp_eval.go`
  `capUnreviewedStdlib`). Interpreted only. Policy decision (§4.2 of the note).
- **F3 — native builds through `cmd/go`** (bridge worker in
  `interp/bashpp_native_bridge.go`; generated module in the backend plan):
  `cmd/go`'s own `disallowInternal` refuses the closure from a module outside
  the tree. Mechanism: `importcfg` build (worker, `interp`) / overlay at the
  original import path (compiled, `lower` + `gosource` + backend seam).
- **F4 — non-Go inputs** (backend `bashppTestPlan`: `*_test.s`). Overlay route
  assembles them natively as `cmd/go` does; recorded disposition.
- **F5 — evaluator** (first interpreted failure after F1–F3 on the smallest
  root): `avlint32.go:104:3: BASHPP-EASSIGN-TYPE: untyped result is not
  assignable to *__gosource_pkg_0_node32` → S162.1.
- **F6 — time**: native `go test` on 2 vCPU already 24–256 s for six roots;
  no interpreter slowdown fits the 10-minute package bound → interpreted
  design limitation by ID.

| root | first line (Barrier B) | first cause | mechanism / owner | status |
|---|---|---|---|---|
| package:cmd/compile | `main.go:8:2: could not import cmd/compile/internal/amd64` | F1 (+F3) | overlay (library transpile exit 0 on the leaf, 65.5 s); F1+F2+F3 then F6 (24.5 s native, `package main`) interpreted | design |
| package:cmd/compile/internal/abt | `_testmain.go:10:2: could not import testing/internal/testdeps` | F1 (+F2, F3) | overlay: **spike — PASS 9/9 in 0.68 s on the leaf** (record); flat route PASS 9/9; interpreted: F5 after the driver | design (spike) |
| package:cmd/compile/internal/amd64 | `galign.go:8:2: … cmd/compile/internal/ssagen` | F1 | overlay: library transpile `ssa.go:1742:3: LOWER-ETYPE: cannot convert x (variable of type *big.Int) to type int64` (S162.4); interpreted F1–F3 then S162.1 | design → S162.4 row |
| package:cmd/compile/internal/base | `flag.go:8:2: … cmd/internal/cov/covcmd` | F1 | overlay (library transpile exit 0 on the leaf); interpreted F1–F3 then S162.1 | design |
| package:cmd/compile/internal/compare | `compare.go:10:2: … cmd/compile/internal/base` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |
| package:cmd/compile/internal/devirtualize | `devirtualize.go:15:2: … cmd/compile/internal/base` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |
| package:cmd/compile/internal/dwarfgen | `dwarf.go:11:2: … internal/buildcfg` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |
| package:cmd/compile/internal/importer | `gcimporter.go:12:2: … internal/exportdata` | F1 | overlay (library transpile exit 0 on the leaf); interpreted F6 (30.9 s native) | design |
| package:cmd/compile/internal/inline/inlheur | `analyze.go:8:2: … cmd/compile/internal/base` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |
| package:cmd/compile/internal/ir | `abi.go:8:2: … cmd/compile/internal/base` | F1 | overlay: library transpile hits `LOWER-ETYPE: "unsafe" imported and not used` (S162.4 per-file import pruning); interpreted | design → S162.4 row |
| package:cmd/compile/internal/liveness | `arg.go:9:2: … internal/abi` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |
| package:cmd/compile/internal/logopt | `log_opts.go:8:2: … cmd/internal/obj` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |
| package:cmd/compile/internal/loopvar | `loopvar.go:10:2: … cmd/compile/internal/base` | F1 | overlay (library transpile exit 0 on the leaf); interpreted F6 (46.5 s native: the test drives the compiler) | design |
| package:cmd/compile/internal/noder | `codes.go:7:8: … internal/pkgbits` | F1 | overlay: library transpile `writer.go:2479:10: gosource: unsupported type switch initializer` (gosource owner); interpreted | design → gosource row |
| package:cmd/compile/internal/rangefunc | `rewrite.go:532:2: … cmd/compile/internal/base` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |
| package:cmd/compile/internal/reflectdata | `alg.go:13:2: … cmd/compile/internal/base` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |
| package:cmd/compile/internal/ssa | `Bash++ gotest backend: non-Go inputs` | F4 (then F1) | overlay only (`.s` native, recorded): library transpile `allocators.go:38:12: gosource: unsupported expression *ast.ArrayType` in 13.9 s (S162.1 compiled class); interpreted F6 (129.5 s native, 467k LOC) + F4 | design → S162.1 row |
| package:cmd/compile/internal/ssagen | `abi.go:9:2: … internal/buildcfg` | F1 | overlay: library transpile `nowb.go:115:22: gosource: unsupported expression *ast.ArrayType`; interpreted | design → S162.1 row |
| package:cmd/compile/internal/syntax | `error_test.go:34:2: … internal/testenv` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |
| package:cmd/compile/internal/test | `abiutils_test.go:9:2: … cmd/compile/internal/abi` | F1 | overlay (library transpile exit 0 on the leaf); interpreted F6 (83.9 s native: builds testdata with the compiler) | design |
| package:cmd/compile/internal/typecheck | `builtin.go:6:2: … cmd/compile/internal/types` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |
| package:cmd/compile/internal/types | `alg.go:7:8: … cmd/compile/internal/base` | F1 | overlay: library transpile `alg.go:7:1: LOWER-ETYPE: "unsafe" imported and not used` (S162.4); interpreted | design → S162.4 row |
| package:cmd/compile/internal/types2 | `alias.go:8:2: … cmd/compile/internal/syntax` | F1 | overlay (20 xtest files → the import-edge mechanism): library transpile `interface.go:124:24: gosource: unsupported expression *ast.ArrayType`; interpreted (14.8 s native) | design → S162.1 row |
| package:cmd/internal/testdir | `testdir_test.go:17:2: … internal/testenv` | F1 | overlay (xtest-only package → the import-edge mechanism; the test is the corpus runner, 256 s native); interpreted F6 | design |
| package:go/types | `api.go:43:4: … internal/types/errors` | F1 | overlay (26 xtest files): library transpile `interface.go:163:24: gosource: unsupported expression *ast.ArrayType`; interpreted (18.7 s native) | design → S162.1 row |
| package:internal/types/errors | `codes_test.go:14:2: … internal/testenv` | F1 | overlay (library transpile exit 0 on the leaf); interpreted | design |

Requests to other seams: §8 of the design note (lower: library emission;
backend seam: `transpile-overlay-go-test` + proofs + importcfg fallback;
harness: state the 10-minute package bound; S162.1: the `BASHPP-EASSIGN-TYPE`
row; S162.3-B: the per-call number against the native times).
