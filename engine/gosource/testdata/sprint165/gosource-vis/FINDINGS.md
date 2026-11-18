# S165.2 — lane `gosource-vis`: identity-keyed `internal` visibility (story #98, D8 = (a))

## Decision as recorded (Sprint 165 master execution plan, D8, approved 2026-09-13)

> **D8 — identity-keyed `internal` visibility (a reviewed security boundary).**
> The checker refuses `internal/…` imports from the wrong identity in 15
> package compiled rows + `intrinsic.go` + `escape_runtime_atomic.go`.
> **(a)** implement §4.1 of `sh/docs/bashpp-multi-package-execution.md` —
> `cmd/go`'s rule on identities, admitted only when the program's *declared*
> identity (`--go-import-path` inside std/cmd, or the backend-asserted
> `TestMain`) qualifies; a user identity is dotted, hence never standard, hence
> never inside `cmd/` / `internal/`; the reviewed inventory
> `BashPPStdlibImportAllowed` is not widened, the *gate in front of it* gains
> one identity-keyed branch. […] the four-negative reproducer set as the proof
> that no user program can reach the branch, and the decision text itself
> recorded on the card (**the boundary changed; the inventory did not**).

## Mechanism (one rule, three sites)

- **The rule** — `syntax.BashPPInternalImportVisible(identity, path, testMain)`
  (`syntax/bashpp_internal_visibility.go`), cmd/go's `disallowInternal`
  (`load/pkg.go`) on identities, exactly the §4.1 table: no `internal`
  element → visible; `testMain` → `testing/internal/…` visible; top-level
  `internal/…` → visible iff the identity is standard (cmd/go's
  `IsStandardImportPath`: first element has no dot); otherwise visible iff
  the identity is inside the parent of the *final* `internal` element
  (cmd/go's module branch, the importer's path not its directory). Two
  details the table leaves implicit and the code pins: the top-level case is
  decided *before* the prefix case (cmd/go's `HasPathPrefix(x, "")` is
  vacuously true, which would admit every top-level internal package for
  every identity); an empty identity sees no internal package at all.
  Table-driven test with every cmd/go case named.
- **Why it lives in `syntax` (seam note for the manager).** The brief says
  "one function used at all three sites". `interp` cannot import `gosource`:
  bashy's `TestGoSourceFrontEndIsNotLinkedIntoClassicBash` guards that the
  classic `bash` binary never links the Go front end, and `interp` is linked
  into it. `gosource` must not import `interp` (front end → runner). The only
  shared home both sites and `lower` already import is `syntax`, and it is
  where the inventory gate `BashPPStdlibImportAllowed` lives — the gate the
  decision says gains the branch. It is a NEW file (plus its test), touching
  nothing else in `syntax`. If the manager prefers no `syntax` edit from this
  lane, the alternative is a private copy of the 20-line rule in `interp`
  (two copies of one security rule) — the function moves mechanically.
- **Site 1 — the checker's map importer** (`gosource/import_visibility.go`,
  additive edits in `packages.go` / `source.go`). Identity = `PackageSpec.Path`
  for a mapped package (always declared), `Options.ImportPath` for the
  program (declared iff non-empty). With a declared identity the map
  importer decides visibility **before** consulting the map or the fallback
  (a mapped internal package was a policy-free map hit before — a foreign
  identity could import it), refuses with gc's exact importer wording
  (`use of internal package P not allowed`, rendered by go/types as
  `could not import P (use of internal package P not allowed)`), and asks
  the fallback through the new `gosource.IdentityImporter` interface
  (`ImportFromPackage(path, identity, srcDir)`) when the fallback offers it —
  a policy-free lookup, because the decision was already taken on the
  identity and a directory rule keyed on a scratch directory is exactly what
  refused the corpus rows. Without a declared identity NOTHING changes: map
  hits stay policy-free, fallback imports go through the fallback's own
  rule (golden test with `lower.NewModuleImporter` on a directory outside
  GOROOT). `Options.TestMain` (bool, requires `ImportPath`) is the
  backend-asserted fact; a mapped package is never the test main.
- **Site 2 — the runtime resolver** (`interp/bashpp_import.go`, the one
  interp file; additive): `interp.GoSourceIdentity(importPath, testMain)`
  RunnerOption → `bashPPEvalRequest.ImportPath/TestMain` (Go programs only;
  shell source carries none) → `nativeBashPPEvaluator.Resolve`: the
  inventory gate gains `bashPPIdentityAdmitsInternal` (an INTERNAL standard
  package the identity qualifies for; `cmd/go`, `cmd/compile` and every other
  non-internal unreviewed path stay with the inventory's verdict), and
  `validateBashPPImportVisibilityFor` decides `internal` on the identity
  while traversal/vendor keep the directory rule. `policyBashPPEvaluator.
  Resolve` (`bashpp_eval.go`, the interp integrator's file) needs the
  two-line diff under "requests" to take the same branch.
- **Site 3 — `moduleImporter.ImportFrom` (`lower`)** is the lower owner's
  file: the exact diff (interface implementation + its test) is under
  "requests: lower". Measured in-process with the diff applied (then
  reverted): every corpus import shape resolves through `lower`'s importer
  from a scratch module directory outside GOROOT — `internal/buildcfg` as
  `cmd/compile/internal/base.test`, `internal/runtime/sys` as `main`,
  `internal/runtime/atomic` as `p`, `cmd/internal/src` as
  `cmd/compile/internal/foo`, `testing/internal/testdeps` with `TestMain`,
  `internal/testenv` as `cmd/internal/testdir.test` — and the negatives keep
  gc's wording. **Until that diff lands the corpus rows stay refused**, by
  `lower`'s directory rule instead of the map's (the gosource negative set is
  independent of the fallback; the positive set is not).
- **§4.3 (worker build with importcfg): not applied.** It concerns the
  interpreted execution of an admitted internal import (the bridge worker,
  `interp/bashpp_native_bridge.go` / `bashpp_import_scratch.go` — the
  runtime/bridge lane's files); every interpreted package row is by ID (D1)
  this sprint and no compiled row depends on the worker. Reported, not
  started.

## Rows (Barrier C, `barrier-c/active-*.tsv`)

The harness passes the identity today as: package roots (both modes)
`--go-import-path <pkg>.test` (cmd/go's testmain path); directory compile
phases `--go-import-base <-D> --go-import-path <-p>` (upstream's own
`compileInDir`: `main` for the program, `test/<file>` for a library);
single-file compile phases and the execute phase of a single-package
directory program: **no identity** (`bashpp_backend_test.go`: `mapArgs` only
when `directory`; `program.mapArgs` only when `len(earlier) != 0`).

| root | mode | first cause | mechanism | rule as written admits it? | status |
|---|---|---|---|---|---|
| package:cmd/compile | compiled | `main.go:23:2 could not import internal/buildcfg` from identity `cmd/compile.test` | site 1 + lower | yes: standard identity, top-level internal | fixed in gosource (this lane); closes when the lower request lands |
| package:cmd/compile/internal/base | compiled | `internal/buildcfg` from `cmd/compile/internal/base.test` | site 1 + lower | yes | same |
| package:cmd/compile/internal/dwarfgen | compiled | `internal/buildcfg` | site 1 + lower | yes | same |
| package:cmd/compile/internal/importer | compiled | `internal/exportdata` | site 1 + lower | yes | same |
| package:cmd/compile/internal/inline/inlheur | compiled | `internal/buildcfg` | site 1 + lower | yes | same |
| package:cmd/compile/internal/liveness | compiled | `internal/abi` | site 1 + lower | yes | same |
| package:cmd/compile/internal/logopt | compiled | `internal/buildcfg` | site 1 + lower | yes | same |
| package:cmd/compile/internal/noder | compiled | `internal/pkgbits` | site 1 + lower | yes | same; next defect on this root is `writer.go:2479 gosource: unsupported type switch initializer` (S162.2 ledger) |
| package:cmd/compile/internal/ssagen | compiled | `internal/buildcfg` | site 1 + lower | yes | same; next defect `nowb.go:115 unsupported expression *ast.ArrayType` (S162.2 ledger) |
| package:cmd/compile/internal/syntax | compiled | `error_test.go:34:2 internal/testenv` | site 1 + lower | yes | same |
| package:cmd/compile/internal/test | compiled | `clobberdead_test.go:8:2 internal/testenv` | site 1 + lower | yes | same |
| package:cmd/compile/internal/typecheck | compiled | `builtin_test.go:9:2 internal/testenv` | site 1 + lower | yes | same |
| package:cmd/internal/testdir | compiled | `testdir_test.go:17:2 internal/testenv` from `cmd/internal/testdir.test` | site 1 + lower | yes | same |
| package:cmd/compile/internal/amd64 | interpreted | `galign.go:8:2 cmd/compile/internal/ssagen` | site 1 (mapped identity `cmd/compile/internal/amd64` inside `cmd/compile`) | yes | mechanism fixed; interpreted stays by ID (D1) |
| package:cmd/compile/internal/loopvar | interpreted | `cmd/compile/internal/base` | site 1 | yes | by ID (D1) |
| package:cmd/compile/internal/reflectdata | interpreted | `cmd/compile/internal/base` | site 1 | yes | by ID (D1) |
| package:cmd/internal/testdir | interpreted | `internal/testenv` | site 1 | yes | by ID (D1) |
| package (all 26) `_testmain.go: testing/internal/testdeps` | interpreted | cmd/go's testmain exemption | `Options.TestMain` (site 1) + `--go-test-main` (bashy) + the backend passing it | yes, with the fact | mechanism fixed; interpreted by ID (D1); the fact is the backend seam's to pass |
| testdir:intrinsic.go | compiled | `intrinsic.dir/main.go:9:4 internal/runtime/sys`; directory compile phase carries `--go-import-path main` | site 1 + lower | yes: `main` is a standard identity (upstream compiles it with `-p main` against a policy-free importcfg) | fixed in gosource; closes when the lower request lands |
| testdir:intrinsic.go | interpreted | same first line at the `--check` phase; the execute phase of a single-package directory program carries **no** `--go-import-path` (`program.mapArgs` empty) | site 1 + harness | yes at the check; the execute would fall to the directory rule | check phase closes with lower; execute needs the harness request (b) |
| testdir:escape_runtime_atomic.go | compiled | `escape_runtime_atomic.go:12:2 internal/runtime/atomic`; single-file `errorcheck -0 -m -l`, upstream compiles it with `-p p`; the harness's transpile step passes **no** `--go-import-path` | site 1 + lower + harness | yes for identity `p`; no identity is passed today, so the rule cannot apply | fixed in gosource for the identity; needs the harness request (a) to pass upstream's `-p` |

Interpreted rows of `escape_*` are retained (optimizer diagnostics, non-blocking).

## Reproducer set — `gosource/testdata/sprint165/internal-visibility/` (driving test `gosource/import_visibility_test.go`)

Positive: mapped `example.com/m/internal/x` imported by mapped/program
`example.com/m/y` (and by `example.com/m`, `example.com/m/z`); identity
`cmd/compile/internal/foo`, `…/base.test`, `…/base_test` importing
`internal/buildcfg`; `main` → `internal/runtime/sys` (intrinsic); `p` →
`internal/runtime/atomic` (escape_runtime_atomic); `cmd/compile/internal/foo`
→ `cmd/internal/src`; `testing/internal/testdeps` with `TestMain` for a cmd
and for a user test main; `internal/testenv` from `cmd/internal/testdir.test`.
Negative (each exactly one diagnostic, at the import's line:col, gc's
wording — missing/extra/duplicate/wrong-line/wrong-wording/unexpected-success
all fail): the same `internal/x` from `example.com/other` (as the program and
as a mapped importer); dotted identity → `internal/buildcfg`,
`internal/runtime/sys`, `internal/runtime/atomic`, `cmd/internal/src`;
`std/foo` → `cmd/internal/src` (standard but outside `cmd`); `testdeps`
without `TestMain` for both identities; `internal/testenv` from
`example.com/m.test` even with `TestMain`; a mapped package with
`TestMain` (never the test main); `TestMain` without `ImportPath` (option
error); `cmd/link/internal/ld` and `example.com/dep/internal/secret` from
`cmd/compile/internal/foo` and from `main` (nothing wider). Unchanged: no
identity → the fallback's rule (fake directory-rule fallback and
`lower.NewModuleImporter` golden), mapped internal package stays a
policy-free map hit; a legacy fallback without the interface still sees the
map's refusal first. Disabling the rule makes 9 negatives fail (measured).
`interp/bashpp_import_identity_test.go` mirrors the set at site 2 against
`go list` (13 stdlib cases, 5 module-internal cases, the option).

## Requests to other seams

### lower (owner: lower lane) — `moduleImporter` offers `gosource.IdentityImporter`

Exact diff, measured in-process (probe test through `gosource.Load` with
`lower.NewModuleImporter` on a scratch module dir: 9/9 shapes as expected):

```diff
--- a/lower/module_importer.go
+++ b/lower/module_importer.go
@@ -333,3 +333,23 @@ func (m *moduleImporter) loadLocked(target string) error {
 // It does not execute the importing program. Callers loading unchanged Go source
 // can provide this importer to their Go type checker.
 func NewModuleImporter(dir string) types.Importer { return newModuleImporter(dir) }
+
+// ImportFromPackage is gosource's IdentityImporter: the explicit package map
+// has already decided internal visibility on the importer's DECLARED
+// identity (the compiler's -p), so this resolves the path policy-free — the
+// directory rule of ImportFrom, keyed on where the importing files happen
+// to sit, is exactly what would refuse an import the identity admits. Module,
+// workspace, vendor and GOPATH resolution are the delegate's, unchanged.
+// identity is the importer's declared path, for diagnostics.
+func (m *moduleImporter) ImportFromPackage(path, identity, srcDir string) (*types.Package, error) {
+	m.mu.Lock()
+	sdkErr := m.sdkErr
+	m.mu.Unlock()
+	if sdkErr != nil {
+		return nil, fmt.Errorf("cannot resolve %q for %q: %w", path, identity, sdkErr)
+	}
+	if from, ok := m.delegate.(types.ImporterFrom); ok {
+		return from.ImportFrom(path, srcDir, 0)
+	}
+	return m.delegate.Import(path)
+}
```

with `lower/module_importer_identity_test.go`:

```go
package lower

import (
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"mvdan.cc/sh/v3/gosource"
)

// Sprint 165 S165.2: the module importer offers gosource.IdentityImporter —
// the policy-free resolution the explicit package map asks for after it has
// decided internal visibility on the importer's declared identity. The
// directory rule of ImportFrom is unchanged for every other caller.
func TestModuleImporterIdentityImporter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scratch\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	imp := NewModuleImporter(dir)
	by, ok := imp.(gosource.IdentityImporter)
	if !ok {
		t.Fatal("the module importer does not offer gosource.IdentityImporter")
	}
	// The directory rule still refuses from a scratch directory outside GOROOT.
	if _, err := imp.(types.ImporterFrom).ImportFrom("internal/buildcfg", dir, 0); err == nil || err.Error() != "use of internal package internal/buildcfg not allowed" {
		t.Fatalf("ImportFrom = %v, want the directory rule's refusal", err)
	}
	// The identity path resolves policy-free: the map decided.
	for _, path := range []string{"internal/buildcfg", "cmd/internal/src", "testing/internal/testdeps", "internal/runtime/sys"} {
		pkg, err := by.ImportFromPackage(path, "cmd/compile/internal/base.test", dir)
		if err != nil || pkg == nil || pkg.Path() != path {
			t.Fatalf("ImportFromPackage(%q) = %v, %v", path, pkg, err)
		}
	}
	// And is the same package object the plain path yields for a reviewed import.
	a, err := by.ImportFromPackage("strings", "example.com/app", dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := imp.Import("strings")
	if err != nil || a != b {
		t.Fatalf("Import(strings) = %v, %v; identity path gave %v", b, err, a)
	}
}
```

(`lower` test files already import `gosource`; the non-test `lower` package
does not and this diff keeps it that way.) `go test -count=1 -run
TestModuleImporterIdentityImporter ./lower/` passed with the diff applied on
darwin; reverted before committing.

### interp integrator — `interp/bashpp_eval.go` (`policyBashPPEvaluator.Resolve`)

```diff
@@ func (e *policyBashPPEvaluator) Resolve(ctx context.Context, req bashPPEvalRequest, path string) (string, error) {
 	capability := classifyBashPPPackage(facts, path)
+	// Sprint 165 D8: an INTERNAL standard-library package outside the
+	// reviewed inventory is admitted for a declared identity that cmd/go's
+	// rule admits it for (bashPPIdentityAdmitsInternal); nothing else moves.
+	if capability == capUnreviewedStdlib && bashPPIdentityAdmitsInternal(req, path) {
+		capability = capReviewedStdlib
+	}
 	if bashPPPolicyFor(capability) != policyToolchain {
@@
-	if err := validateBashPPImportVisibility(req.Dir, facts.Dir, path); err != nil {
+	if err := validateBashPPImportVisibilityFor(req, facts.Dir, path); err != nil {
```

Both helpers are in `bashpp_import.go` (this lane). The existing
`bashpp_eval_test.go` `unreviewed-stdlib` case is unaffected (no identity).

### bashy (manager) — the flag and the wiring

- Flag: `--go-test-main` (boolean, `--source=go` only, requires
  `--go-import-path`; refuse otherwise — `gosource.Load` and
  `interp.GoSourceIdentity` both refuse the fact without an identity).
  Parse it next to `--go-import-path` in `internal/cli/gosource.go`
  (`GoSourceSelection.TestMain`, `GoSourceOptions.TestMain`) and in
  `internal/agentos/transpile.go` (`goTestMain`).
- `internal/agentos/gosource.go` `loadGoSource`: `TestMain: opts.TestMain`
  in the `gosource.Options` literal.
- Runner: where `cli.GoSourceModuleDir(in.Dir)(r)` is applied
  (`internal/cli/gosource.go` ≈ line 877), also apply
  `interp.GoSourceIdentity(startupGoSource.ImportPath, startupGoSource.TestMain)(r)`
  when `ImportPath != ""` (a hook like `cli.GoSourceModuleDir` keeps the
  drop-in free of the front end; `interp` is already linked).
- `transpile --go-library`: pass `TestMain` through `base` like
  `ImportPath` (it is only meaningful for the `_testmain.go` unit, which the
  compiled route never transpiles — harmless, and keeps one option shape).

### harness + backend seam

- (a) **Single-file compile phases carry no identity.** `escape_runtime_
  atomic.go` compiles upstream with `-p p` (`compileFile`); the backend's
  transpile/`--check` argv for non-directory compile phases has no
  `--go-import-path`, so the identity rule cannot apply and the row stays
  refused by the directory rule after this lane. Pass upstream's `-p` as
  `--go-import-path` for every compile phase whose native argv carries one
  (`p` for errorcheck/compile, `main` for run/build) — the same identity
  handoff the directory phase already does. It is upstream's own identity,
  not a new one.
- (b) **The execute phase of a single-package directory program carries no
  identity** (`program.mapArgs` only when earlier packages exist).
  `intrinsic.go` interpreted would pass `--check` and then fall to the
  directory rule at execute. Remember `--go-import-base/--go-import-path`
  for single-package programs too.
- (c) **`--go-test-main` at the testmain site** (plan step 1‖, "the
  `TestMain` fact"): `bashpp_backend.go` `bashppTestPlan` interpreted branch,
  next to `--go-import-path pmain.ImportPath` — the plan's `_testmain.go` IS
  cmd/go's generated testmain. Compiled branch: not needed (cmd/go's own
  `_testmain.go` runs natively).
- (d) v10.6 rule (b): the `use of internal package … not allowed` rows are
  one set across package/151/152; after (a) the set is closed by one
  mechanism.

## Leaf requests (manager submits)

- `leaf-gosource-vis-1.tsv`: `testdir:intrinsic.go`,
  `testdir:escape_runtime_atomic.go` + compiled-PASS canaries
  `testdir:escape_sync_atomic.go`, `testdir:escape_unsafe.go`,
  `testdir:escape_iface.go` (same `errorcheck -0 -m -l` family, no internal
  import), `testdir:alias3.go`, `testdir:ddd2.go` (rundir family, the
  directory map path). Expect: intrinsic compiled PASS only with the lower
  diff on the candidate; escape_runtime_atomic compiled PASS only with lower
  + harness (a); canaries unchanged.
- `leaf-gosource-vis-pkg.tsv` (package lane, 10-min bound): the 13 compiled
  `could not import internal` package roots + `package:cmd/compile/internal/abt`,
  `…/compare`, `…/devirtualize` (compiled PASS at integ-2/C) as canaries.
  Expect the 13 to move past the checker only with the lower diff on the
  candidate; the next defect per root is the S162.2 ledger's (noder, ssagen)
  or generated-package fidelity (lower lane).

Bundle: `<workspace>/gosource-vis.bundle` (HEAD of the workspace branch).

## Verification (darwin)

`gofmt -l` clean on touched files; `go test -count=1 -timeout 30m
./gosource/...` ok; `./syntax/` ok with `PATH=/bin:/usr/bin:$(dirname $(which
go))` (the `TestParseConfirm/bash` failures without it are the CLAUDE.md
shim gotcha: `bash` on PATH is bashy); full sh gate `go test -short -timeout
30m ./interp/ ./lower/ ./gosource/ ./syntax/` (same PATH): `lower` ok
(648 s), `gosource` ok (199 s), `syntax` ok, `interp` FAIL with exactly the
8 pre-existing `GoSource*` darwin failures (Unwrap/TourCallback/
RetainedCallbackPolicy/ReceiveRejects/OriginalTCPScratch/NilAggregate/
NativeReadSlice/ImageRetained — none mention visibility or the inventory;
0 occurrences of the new wording in the log); `git diff --check` clean.
