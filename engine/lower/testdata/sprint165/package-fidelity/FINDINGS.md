# Sprint 165 — story #98 (`f206111602d9`)

## package:cmd/compile/internal/types

`cmd/compile/internal/types` compiled-library lowering could retain an import
whose only source use was folded from a function-local constant initializer.
For the `unsafe.Sizeof` shape, `gosource` materialized the checked value, but
the generated file still declared `unsafe`, producing `"unsafe" imported and
not used`.

`converter.valueDecl` now preserves an explicit, function-local const
initializer when it selects through an imported package and does not use
`iota`. This keeps `unsafe.Sizeof` and imported constants such as
`math.MaxInt8` live in generated Go. Ordinary unused imports remain refused;
local const expressions without an imported package still fold. Initializers
that use `iota` remain deliberately folded because this per-spec path has no
const-group carrier and replaying `iota` would change its value.

The outside-corpus fixture `local-const-import/` and
`TestGoSourceLibraryLocalConstImport` cover both selector shapes and a real Go
library build. The paired tests pin unused-import refusal and the iota bound.
