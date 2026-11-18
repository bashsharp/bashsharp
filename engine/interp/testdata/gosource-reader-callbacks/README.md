Sprint: #118; Story: #54; Story-ID: c3a60493cde9

readers.go.txt and rot13.go.txt are unchanged Go Tour solution sources, including
original build directives and notices. Tests authenticate their SHA-256 digests.
The Tour reader.Validate helper is pinned at golang.org/x/tour v0.1.0.

The generated Read([]byte) (int, error) stub carries an authenticated native byte
buffer handle to the interpreter. Every original statement, loop, expression and
method body remains interpreted. Indexed writes update the actual dependency
buffer before the callback returns, including writes preceding a partial-error
return or panic. Native subslices retain backing offsets and capacity; retaining
one in an original global keeps a live handle to that same array for the session.
There is no copied-buffer escape assumption or original-body native forwarding.

Only reviewed synchronous consumers accept an original Reader: Tour
reader.Validate, io.ReadAll, io.ReadFull/ReadAtLeast with native buffers, and
io.Copy with supported native standard-library writers. Arbitrary retained or
asynchronous Reader consumers remain refused. Passing an interpreter-owned
copied slice alongside an original callback remains refused by the existing
reference policy. Reset invalidates every previous native buffer handle.

Imported field types in mirrored receiver declarations use generated import
aliases, rewritten as type AST selectors; tags and original field names stay
literal. Native values returned/assigned through typed result paths preserve
handles, including error identities. Predeclared byte/rune aliases agree with
uint8/int32 arithmetic while user declarations remain distinct.

Both original programs pass native/interpreted/compiled comparison. The complete
1 MiB reader.Validate run initially took 160.704s. Avoiding a discarded native
index read for index-only byte ranges reduced the measured interpreted run to
75.177s. This remains a substantial corpus-deadline risk; no corpus input cap or
harness timeout changed. The complete original acceptance test remains separate
from the smaller lifecycle/race controls.

Five three-mode programs and cancellation/Reset passed under -race in 22.236s;
actual stale-buffer and retained-callback boundary controls passed in 5.330s.
Existing native Read, original function callback, local private codec and typed
nil regressions passed under the race detector in 90.312s.
Coverage includes unchanged rot13, nested native Read, partial errors, pointer
receiver state, overlapping/retained subslices, and writes visible after panic.
Authored fixtures use named receivers because the base lowerer still panics for
an unnamed method receiver; that separate defect is not fixed or hidden here.

Raw source digests, exact stdout/stderr, statuses, uncapped timing and standalone
compiled artifact execution (generated Go removed, no tools on PATH):
~/.local/state/bashy/sprint118-evidence/reader-callbacks-018/manifest.json.
Full sprint completion and deadline convergence are not claimed by this slice.
