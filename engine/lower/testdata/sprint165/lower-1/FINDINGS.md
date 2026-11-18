# Sprint 165 lower-1 findings

The exact Go 1.27 testdir harness owns the verdicts. Run 0 was read from the
leaf host before the second mechanism cluster; its verdicts reproduced Barrier
C for every row below. Local evidence uses outside-corpus driving tests and the
pinned compiler. A focused in-process check also confirmed that the six fixed
corpus roots now pass `gosource.Parse` and `lower.Compile`.

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:closure3.go` | lowering rewrites the Go function-literal structure, so gc assigns different closure identities and positions | byte-faithful native Go function literals | design; existing Sprint 152 fidelity classes C5/C6, not a safe local numbering rewrite |
| `testdir:fixedbugs/issue18895.go` | the explicit package map is flattened and `gosource` changes package-owned identifiers before lower receives them | preserve per-package identity and emit one native unit per package | moved to `gosource` + backend |
| `testdir:fixedbugs/issue19261.go` | the explicit package map is flattened and `gosource` changes package-owned identifiers before lower receives them | preserve per-package identity and emit one native unit per package | moved to `gosource` + backend |
| `testdir:fixedbugs/issue37837.go` | the explicit package map is flattened and `gosource` changes package-owned identifiers before lower receives them | preserve per-package identity and emit one native unit per package | moved to `gosource` + backend |
| `testdir:fixedbugs/issue42284.go` | the explicit package map is flattened and `gosource` changes package-owned identifiers before lower receives them | preserve per-package identity and emit one native unit per package | moved to `gosource` + backend |
| `testdir:fixedbugs/issue56280.go` | the explicit package map is flattened and `gosource` changes package-owned identifiers before lower receives them | preserve per-package identity and emit one native unit per package | moved to `gosource` + backend |
| `testdir:linkname.go` | the explicit package map is flattened and the linked package's `ContainsSlash` name is already mangled in the syntax tree | preserve per-package identity and compile native library units | moved to `gosource` + backend |
| `testdir:prove.go` | comment/expression layout is absent from the converted tree, shifting gc's source-keyed proof notes | full comment-position fidelity | design; existing Sprint 152 C1 finding |
| `testdir:nilptr3.go` | the comment between two unary operators is absent from the converted expression, so gofmt joins the expression and moves gc's second nil-check note | full comment-position fidelity | design; previously recorded in Sprint 162 `lower-2` |
| `testdir:live_regabi.go` | conversion stops at `gosource: unsupported compound simple statement` before lower is called | compound simple-statement conversion | moved to `gosource` |
| `testdir:chan/doubleselect.go` | lower treated a native `*int` initializer returned by a call as a Bash++ scalar and wrapped it as an address of `int` | keep native Go pointer initializers unchanged | fixed in `0dda1f4b` |
| `testdir:const8.go` | consecutive local const specs lost their shared group scope, so the declaration named `iota` could not shadow the predeclared counter in later implicit specs | reconstruct local const groups and their iota index | fixed in `497f5317` |
| `testdir:fixedbugs/bug115.go` | lower promoted the typed value of `^uint(0)` to `*big.Int` solely because its constant exceeded `int64` | restrict arbitrary-precision short-declaration promotion to Bash++ input | fixed in `50f587c6` |
| `testdir:fixedbugs/bug273.go` | `gosource` folds the only `unsafe` use before lower receives the tree but retains the import | preserve native Go expression and import use identity | moved to `gosource` |
| `testdir:fixedbugs/bug424.go` | flattening the directory packages merges distinct method identities and makes the legal selector ambiguous before emission | preserve package method identity in separate native units | moved to `gosource` + backend |
| `testdir:fixedbugs/issue24693.go` | `gosource` mangles the imported package's recursive type into the flat package, turning a legal cross-package cycle into self-reference | preserve the imported package as a separate unit | moved to `gosource` + backend |
| `testdir:fixedbugs/issue4370.go` | `gosource` mangles the imported package's recursive type into the flat package, turning a legal cross-package cycle into self-reference | preserve the imported package as a separate unit | moved to `gosource` + backend |
| `testdir:fixedbugs/issue54911.go` | conversion changes `Set[T].Add(s)` to `Set.Add[T](s)` before lower receives the expression | retain generic receiver instantiation syntax | moved to `gosource` |
| `testdir:fixedbugs/issue6847.go` | lower emitted `chan (<-chan int)` as `chan <-chan int`, which Go parses as a send-only outer channel | parenthesize a receive-only channel used as a channel element | fixed in `12e44391` |
| `testdir:shift3.go` | `gosource` folds `math.MaxUint` to a literal before lower receives it but retains the import | preserve native Go constant expressions and import uses | moved to `gosource` |
| `testdir:fixedbugs/issue22344.go` | `gosource` folds `unsafe.Sizeof` and nested iota expressions, removing the uses of local `x` and `y` before emission | preserve native const expressions | moved to `gosource` |
| `testdir:fixedbugs/issue7794.go` | `gosource` folds `len(a)` to `10`, removing the only use of local `a` before emission | preserve native const expressions | moved to `gosource` |
| `testdir:fixedbugs/issue16616.go` | package-map flattening changes a package-level type identity into the mangled variable `__gosource_pkg_1_V` before lower sees it | preserve package/type identity in separate native units | moved to `gosource` + backend |
| `testdir:blank.go` | `gosource.checkLoweredNames` treats repeated blank method declarations as a duplicate package name and stops before lower is called | exclude blank identifiers from lowered-name uniqueness | moved to `gosource` |
| `testdir:fixedbugs/issue18459.go` | gc diagnosed `//go:nowritebarrier` at the generated file because lower positioned only the following declaration | emit each compiler pragma's original source position | fixed in `b43402b5` |
| `testdir:fixedbugs/issue18882.go` | gc diagnosed `//go:cgo_ldflag` at the generated file because lower positioned only the following declaration | emit each compiler pragma's original source position | fixed in `b43402b5` |
| `testdir:fixedbugs/issue19467.go` | package-map flattening changes `test/mysync.(*WaitGroup).Add` into `main.(*__gosource_pkg_0_WaitGroup).Add` before lower sees it | preserve package and receiver identity in separate native units | moved to `gosource` + backend |
| `testdir:fixedbugs/issue20298.go` | `gosource` returns its own unused-import diagnostics and no syntax tree, so lower is never invoked | compiler-owned diagnostic pass-through for errorcheck input | moved to `gosource` |
| `testdir:linkname3.go` | free-floating `//go:linkname` pragmas are not attached to a declaration and are dropped during conversion | retain positioned free-floating compiler pragmas | moved to `gosource` |

## Requests to other seams

### `gosource`

- For native Go mode, retain source expressions instead of replacing
  `unsafe.Sizeof`, `len`, imported constants, and generic receiver expressions
  with checker-derived values. The exact affected rows are `bug273`,
  `issue22344`, `issue54911`, `issue7794`, and `shift3`.
- Do not flatten explicit package maps into one renamed syntax file. Preserve
  package-owned identifier, receiver, recursive-type, and frame identity and
  hand the already-supported `lower.Options.Library` path one package at a
  time. This is required by `issue16616`, `issue18895`, `issue19261`,
  `issue19467`, `issue24693`, `issue37837`, `issue42284`, `issue4370`,
  `issue56280`, `bug424`, and `linkname`.
- Extend positioned pragma retention to free-floating `//go:` groups, and make
  `checkLoweredNames` ignore `_` as Go does. These changes own `linkname3` and
  `blank.go`. Add compound simple-statement conversion for `live_regabi`.
- Errorcheck inputs such as `issue20298` need a conversion route that returns a
  native syntax unit even when the source intentionally has compiler-owned
  type errors; `lower` cannot defer a diagnostic when `gosource` returns no
  tree.

### Backend

Consume per-package native library output rather than requesting a flattened
explicit package map. Compile each emitted package with the original package
path and recipe flags, using the existing lower library API. This is the
backend half of every row above marked `gosource` + backend and is required for
gc's names and frame strings to match the original program.
