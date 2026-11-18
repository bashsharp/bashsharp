# Ordinary Go source native lowering — Sprint 118

Story #53 (`99bd1de0093b`). This slice fixes measured compiled failures in
unchanged Go-by-Example sources. Native Go checks ordinary source types; Bash++
profile restrictions, promoted-literal rewrites, recover status handling and
checked-pointer runtime wrappers no longer alter these Go-source constructs.

Struct tags retain quoted text and positions through the typed AST, traversal,
printing and JSON serialization. Compiler `go:embed` comments remain attached
to original package variable declarations. Native global initialization stays
in package declarations. Build callers must stage the original assets beside
generated Go at their original relative paths; the existing corpus harness does
so and authenticates those bytes. Lowering does not read, copy or rewrite assets.
General interpreter embed execution and complete compiler-directive validation
remain separate obligations.

Emitted `//line` directives retain original caller filenames and positions for
native logging/stack APIs. External source-map line numbers remain physical
positions in the generated file. Each marker anchors its next nonempty physical
line, including a compiler directive, matching the public source-map contract.
No original body is delegated to a dependency helper.

`TestGoSourceGbENativeArtifacts` verifies archived, SHA-pinned original sources
and embed assets for recover, structs, XML, generics, range-over-iterators,
embed-directive and logging. It compares native Go against a real artifact built
from the serialized positioned AST, deletes both build source/asset directories,
and executes with empty PATH. Only logging timestamps are removed; the original
`logging.go:40` caller, messages, JSON fields and streams remain checked.

This is compiled-mode evidence. The separate original three-mode replay retains
interpreter failures in panic/recover, local/native structured values, generic
runtime execution, embedded FS and logging. Neither full corpus convergence nor
complete Go compatibility is claimed.
