Sprint: #118; Story: #54; Story-ID: c3a60493cde9

These are unchanged Go Tour sources, including their OMIT directives and original
copyright notices. Tests authenticate the three positive fixtures by SHA-256.
The dependency helpers are golang.org/x/tour v0.1.0, pinned by module checksums.

The original WordCount/Pic bodies execute in the interpreter. The generated
native callback trampoline only asks that interpreter to invoke its registered
function. wc.Test consumes map[string]int results synchronously without mutation
or retention; pic.Show does the same for [][]uint8. Those two reviewed consumers
are explicit policy entries. Aggregate callback arguments and unreviewed
retaining consumers still fail before an original body executes.

Imported native assignment now asks the helper whether its actual authenticated
value is assignable to the expected Go type. Native display names do not decide
assignability. Local fields preserve the static interface on a copied native
value and resolve native method dispatch without evaluating an original call or
index expression during the ownership check. Typed nil pointers remain distinct
from nil interfaces and do not change the source pointer's identity.

Validation covers all three modes for exercise-maps, maps and slices, reader
interface fields, typed-nil fields, original callback side effects with nested
imports, and panic propagation. Additional race controls cover cancellation and
Reset, forged type names on genuine incompatible handles, stale sessions,
unregistered types, and explicit aggregate-reference boundaries. Source files
are removed before the compiled artifact executes with no tools on PATH.

The rot13 fixture remains an explicit interpreter failure: io.Copy would call an
original Read method with mutable slice reference semantics that the current
callback transport does not support. Native and compiled modes pass. Its source
is retained unchanged and its failure is not removed from the denominator.

Raw three-mode stdout, stderr, statuses, source digests and artifact evidence:
~/.local/state/bashy/sprint118-evidence/callback-aggregates-017/manifest.json.
Full sprint convergence is not claimed by these focused checks.
