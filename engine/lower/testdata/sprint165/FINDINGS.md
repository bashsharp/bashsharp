# Sprint 165 lowering residue

The compiled `.dir` roots share an emitter boundary: flattening linked Go
packages into a generated `main` package changes both their native `init`
frames and the package-qualified names Go exposes through interfaces,
reflection, and compiler metadata. `lower.Options.Library` is the general
lowering form for that boundary: it emits one runtime-free Go file per input
file with the input package clause and native `init` declarations intact.
The caller lays the files out at their original package paths and compiles
them as Go packages; lower does not prefix declarations or invent package
identity.

`package-identity/` is an outside-corpus two-package reduction. Its driving
test lowers both packages independently, compiles the generated module, and
requires the dependency's native init frame and dynamic type spelling
`a.Item`. This is the shared mechanism for `interface/embed3`, `issue29612`,
`issue29919`, and `issue20014`; the exact root list is `roots.tsv`.

`range-function/` is an outside-corpus range-over-function reduction. Its
driving test lowers the result a second time and requires bounded output size
and unchanged native `for n := range values` syntax. The emitter therefore
does not expand range-function control flow or accumulate lowering scaffolding
on a repeat pass.
