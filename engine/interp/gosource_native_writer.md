# Original formatting callbacks with native writers

Sprint 118, story 54 (`c3a60493cde9`).

Go-source dependency transport now admits `fmt.Fprint`, `fmt.Fprintln`, and
`fmt.Fprintf` when the first argument is an authenticated dependency-process
handle. Its reflection conversion must still satisfy `io.Writer`. Other
arguments use the existing local String/Error callback machinery, including
original receiver identity and interpreted body execution.

An original writer is rejected before Write or formatting callbacks execute.
This does not implement original `Write` callbacks or grant general mutation
access to interpreter-owned references. Existing omitted-method, reference-shape
and session checks still apply. No unsafe operations or original Go method bodies
are emitted into the helper.

Four authored three-mode comparisons verify buffered writer output, pointer
String side effects, Error callbacks, nested callback reentry, and fmt recovery
from an original String panic. Cancellation during a nested native sleep is
followed by successful Runner Reset and reuse. A valid original writer control
runs in the native oracle and is explicitly refused by the interpreter before
its Write/String/next-statement effects. These and existing callback error,
reference and lifecycle regressions pass under the race detector.

The complete unchanged official 64-bit generator now passes this policy gate
but remains FAIL at local private-field transport: reflect cannot set `hi` of
mirrored `main.Int64`. Typed helper-package field codecs are a separate followup.
The compiled generator matches native output exactly; the complete unchanged
retained child continues to pass all three modes. The unnamed receiver lowering
panic exposed by an auxiliary formatting control is separately retained, not
silently counted as supported. Detailed evidence lives in manager directory
`native-writer-012`.
