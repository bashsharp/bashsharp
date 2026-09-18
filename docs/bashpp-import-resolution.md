# Bash++ import resolution — how Go does it, what Bash++ does, what it should do

Status: **DECIDED 2026-09-11 (user-approved); §3.2 shipped as S149.11 (`sh`) + S149.12 (`bashy`).
§3.4 (one SDK policy) and compiled-mode emission remain open.** Written from the code, not from memory: `sh/lower/{module_importer,go_sdk}.go`,
`sh/interp/bashpp_import*.go`, `sh/syntax/bashpp_import.go`,
`bashy/internal/{cli,agentos}/gosource.go`, and the frozen Go 1.27
`cmd/internal/testdir/testdir_test.go`.

## 1. How Go resolves imports — three layers, only one of them is policy

| Layer | What it does with `import "x"` | Relative `./a` |
|---|---|---|
| **Language spec** | The path is an opaque string; "the interpretation is implementation-dependent". | not defined |
| **`go` command** (build system, the *policy*) | Maps path → package directory → export data. Two policies: **module mode** (go.mod requires, `replace`, `go.work`, `vendor/`, GOROOT for std) and **GOPATH mode** (`GO111MODULE=off`: GOROOT/src, GOPATH/src, nearest `vendor/`). Enforces `internal/` visibility. | module mode: **rejected** ("relative import paths are not supported in module mode"); GOPATH mode: allowed only for command-line packages outside GOPATH, resolved against the importing *directory* and given the synthetic path `_/abs/dir/a` |
| **`go tool compile`** (the compiler, *policy-free*) | Does **no filesystem lookup at all.** It consumes an explicit map: `-importcfg` (`packagefile path=file.a`, `importmap old=new`), `-p <path>` naming the package being compiled, and `-D <base>` for relative imports. | `./a` → `<base>/a`, then looked up in the importcfg map |

The separation is the whole design: **the compiler takes a map; the `go`
command is one of several ways to produce that map.** The Go test corpus
exercises the compiler with a hand-built map and bypasses both policies. For a
`compiledir` root upstream does exactly this (`testdir_test.go`):

```text
pkgs      = goDirPackages(dir)                 # group files by package clause, lexicographic
importcfg = std + "packagefile test/a=<tmp>/test/a.a" (one line per group)
for pkg in pkgs (in order):
    go tool compile -e -D test -p test/<firstfile> -importcfg=<importcfg> <files...>
```

So `import "./a"` in `b.go` means: `./a` + `-D test` → `test/a` → the export
data compiled from the group whose first file is `a.go`. No module, no GOPATH,
no directory named `a` anywhere. **It is a naming convention plus a map.**

## 2. What Bash++ does today — two importers, two policies, no map

### 2a. Static path: type-check, `--check`, `transpile` (`sh/lower/module_importer.go`)

- `newModuleImporter(dir)` runs **`go list -export -deps -json <path>`** in
  `dir` and feeds the resulting export files to `go/importer` (gc format).
- `dir` is `Options.Dir`; for the direct Go-source interface bashy sets it to
  **the directory of the first `--go-file`**
  (`bashy/internal/cli/gosource.go:457`). All `--go-file` inputs are **one
  package** — a second package clause is rejected by `go/parser` merging.
- SDK selection (`sh/lower/go_sdk.go`): `$GOROOT` → the GOROOT baked into the
  bashy binary (empty under `-trimpath`) → **`go` on PATH**;
  `GOTOOLCHAIN=local` unless the caller set one. This is what spike D printed:
  `go SDK go1.26.0 … found via PATH, older than go1.27.0`.
- Module mode when `GOMOD` is set; otherwise a `go/build` structural resolver
  in GOPATH mode. `internal/` visibility is checked against the caller path.
- Relative `./a`: module mode → `go list` fails ("cannot find main module");
  GOPATH mode → resolves to **`<dir>/a` as a subdirectory** — i.e. the
  accidental Go semantics for command-line packages, never upstream's.

### 2b. Runtime path: the Bash++ `import` statement (`sh/interp/bashpp_import.go`)

- Syntax (`sh/syntax/bashpp_import.go`): `import "path"` / `import alias
  "path"`, Class E. The path must pass `module.CheckImportPath`, so `./a`
  and `../a` are **rejected at parse time**; `.`/`..`/`vendor` elements are
  rejected again at runtime.
- Standard library is restricted to the reviewed Go 1.27 list
  (`go127stdlib_generated.go`, source SHA pinned).
- Toolchain: **`bashPPGoBootstrap` — never PATH.** Linker-recorded GOROOT, else
  the downloaded `golang.org/toolchain@go1.27.0` module whose `bin/go` digest
  must match a reviewed allow-list.
- Resolution: `go list -json <path>` in `req.Dir`, with a `go/build`
  structural fallback for GOPATH mode (`bashpp_import_context.go`).

### 2c. The three facts that matter

1. **There is no map input.** Neither path can be told "package `test/a` is
   these files". The only way to make a package visible is to have `go list`
   find it on disk under a module or GOPATH.
2. **Two SDK policies disagree.** The static path will silently use whatever
   `go` is on PATH (1.26 on the dev box); the runtime path refuses PATH and
   authenticates the binary. Type-check and runtime can therefore see
   different export data for the same program. This is a latent product
   defect independent of the harness.
3. **Relative imports are unsupported everywhere**, by construction on the
   runtime path and by accident on the static path.

None of this is wrong for user programs — a Bash++ program in a module gets
exactly Go's semantics. It is wrong only for *driving* Bash++ with an explicit
package set, which is what the corpus, and eventually any build tool or IDE,
needs.

## 3. What Bash++'s way should be

**Principle: Bash++ is a superset of bash *and* of Go — a bash script runs
unchanged, and a Go module runs unchanged. Go was chosen (with minimal
TypeScript/Python ergonomics) precisely for syntax compatibility, so Bash++'s
import model must be Go's, not a variant.** Mirror Go's separation: keep the
`go` command as the only policy engine for programs found on disk (module
mode, `go.work`, `vendor/`, GOPATH mode, `internal/` — exactly as `go list`
decides), and add the compiler's policy-free map as an **opt-in** input for
programs handed over as an explicit package set by a tool. The map never
changes what a module on disk means; an ordinary Go repo needs no flag.

### 3.1 Keep: the `go` command as the sole on-disk policy

Module mode, GOPATH mode, `vendor/`, `go.work`, `internal/` — exactly as
`go list` decides, exactly as today. Bash++ adds nothing here.

### 3.2 Add: an explicit package map on the direct Go-source interface — SHIPPED 2026-09-11

The importcfg idea, expressed in Bash++'s own memory instead of `.a` files
(`sh/gosource/packages.go`, S149.11; `bashy` flags, S149.12):

```text
bashy --bashpp --source=go --check|--go-list \
    --go-package test/a=a.go \
    --go-package test/b=b.go,b2.go \
    --go-import-base test \
    --go-import-path test/c \
    --go-file c.go               # the last / main package, as today
```

`--go-list` is the auditable, verifiable counterpart of `go list`: after a
successful check it prints every import the checker resolved — explicit
packages and the program alike — as one JSON object per line in resolution
order (`from`, `import` as written, `path` after base joining, `origin` =
`package-map` | `importer`, `name`, `files`). It is a pure function of the
inputs: no timestamps, no host paths beyond the names given, identical bytes
across runs (tested). On the spike-D root:

```text
$ bashy --bashpp --source=go --go-list --go-import-base test --go-package test/a=a.go --go-import-path test/b --go-file b.go
{"from":"test/b","import":"./a","path":"test/a","origin":"package-map","name":"a","files":["a.go"]}
```

- `--go-package <importpath>=<files>` is repeatable and **ordered**; each
  package is parsed and type-checked in turn, and its `*types.Package` is
  registered under `<importpath>` in an in-memory map.
- `--go-import-base <base>` is `-D`: an import `./x` in any listed file means
  `<base>/x`. Without it, `./x` is rejected with gc's wording. **Never resolved
  against the filesystem.**
- The importer for every package is `mapImporter{map} → moduleImporter(dir)`:
  the explicit map first, then the existing on-disk policy for everything else
  (std, module deps). Nothing in `moduleImporter` changes.
- `--go-file` stays as sugar for the final package; existing invocations are
  unchanged. A relative import is now refused everywhere without a base (it
  used to be an accidental subdirectory lookup in GOPATH mode).
- The map is a **static-analysis input today**: `--go-package` requires
  `--check` or `--go-list`. Interpreted *execution* of an explicit package set
  is refused with a clear message, because the runtime import bridge
  (`sh/interp/bashpp_import.go`) resolves through the on-disk policy only and
  would answer wrongly, not slowly. `lower.Options.Importer` lets lowering take
  the same map; emitting the dependency packages into the generated module is
  the compiled-mode follow-up.

Shipped as one `types.ImporterFrom` wrapper (`mapImporter`) plus ordered
dependency checking in `gosource.Load`, four flags in
`bashy/internal/cli/gosource.go`, and a one-line hook in `lower.Options`. No
new resolver, no new policy, no filesystem probing.

### 3.3 Add: the same map to `transpile` (compiled mode)

`transpile` takes the same flags. Whether lowering can emit a non-`main`
package is a separate, already-known question (`sh/lower` emits `main.go`);
for compile-only phases of library packages the honest compiled-mode result may
be "unsupported by lowering" until Sprint 152 — that is a product row in the
ledger, not a seam gap.

### 3.4 Fix: one SDK policy

Make the runtime's resolver (`bashPPGoBootstrap`: never PATH, digest-reviewed)
the shared one and demote the static importer's PATH fallback to an explicit
opt-in (`BASHPP_GO=<abs path>` or an `Options.Go`), so type-check and runtime
provably use the same `go`. Out of Sprint 149's scope, but it is the reason
spike D printed a 1.26 SDK, and it should be a story in the product-fix
sprints.

### 3.5 Rejected: a harness-side synthesized GOPATH layout

Copying upstream files unchanged into `<tmp>/<pkg>/` and running each package
with `GO111MODULE=off` and the pinned `go` first on PATH would make `./a`
resolve — by leaning on the accidental subdirectory behavior in §2a. Rejected
because it (a) cannot express `-D test` / `-p test/a`, so diagnostics and
package identities differ from upstream's; (b) breaks on import chains
(`c → ./b → ./a` needs nested copies); (c) requires PATH manipulation to pin
the SDK; and (d) is the harness inventing a resolution policy, which the
Sprint 157 boundary forbids. The seam must hand over upstream's decisions
(package groups, order, `-D` base, `-p` names); §3.2 is the interface that can
receive them.

## 4. What this means for Sprint 149

- The seam extension for the directory family (D) is **blocked on §3.2**, a
  small product change in `bashy` + `sh`. Under the standing rule ("Sprint 149
  makes no product fix"; no unilateral edits to shared repos during a cert
  arm) it needs the user's explicit go-ahead and a story of its own.
- With §3.2 in place the D-seam is mechanical: for each upstream package group
  in order, one invocation with the earlier groups as `--go-package` entries,
  `--go-import-base test`, and the group's own `-p` name — exactly the values
  the instrumented harness already records in its `companions` and `phase`
  events.
- Without it, 149.2 / 149.5 / 149.7 cannot execute their phase honestly; they
  should stay `todo` with this document as the reason, not be closed exit-3
  under a synthesized layout.
