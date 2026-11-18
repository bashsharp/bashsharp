# GoSource goroutine argument values

Sprint: #118  
Story: #51  
Story-ID: 825f8083451e

Original Go evaluates the function value and arguments before launching a
new goroutine. Argument values now travel separately from the Classic Bash++
shell snapshot. Existing typed parameter binding copies structs and arrays,
keeps pointer targets and slice/map/channel identity, and creates independent
parameter variable cells. Assigning a parameter therefore never rebinds its
caller variable. Computed argument producers execute once, in order, and an
argument panic launches no task. Literal signature preparation is ephemeral;
it does not retain an extra parent closure across Runner.Reset.

The exact lexical-capture analysis still determines outer variable identity.
GoSource task scope copies retain those bindings and immutable constants; they
omit unrelated parent locals rather than inspecting or traversing their live
contents. This matters even if the child never reads the discarded copy: a
second launch previously raced with a synchronized first task while the shell
snapshot copied its pointee or map. Classic Bash++ and public Subshell keep
their existing deep-copy behavior. The GoSource boundary is identified only
by the transient, nonnil task-capture map installed around the private clone.

Typed aggregate arguments no longer call ObjectString merely to fill legacy
argument text. Their full typed value cells already carry the argument. Such
stringification was another extraneous read of synchronized mutable backing
storage. Native handle and channel arguments retain session/group admission
checks and every actual operation still checks authority. No original body is
compiled or forwarded to a dependency process.

The provided package-channel failure was independently reproduced against the
unmodified dependency baseline with a Go build overlay: interpreter stderr
`false 3 1` versus native Go `true 9 1`. The corrected test requires matching
raw streams across interpreter, native oracle, and generated source-free Go
artifact execution, without normalization or original source edits.

Fourteen authored controls cover pointer/channel identity, independent
parameter rebinding, caller reassignment after launch, scalar and computed
callee evaluation order, struct/array value copies with interior references,
slice bounds and backing storage, interface-held pointers, panic/no launch,
closure arguments, and repeated launches mutating pointers/slices/maps under
a real native mutex. The last control exposed both host races above and then
passed under the race detector. Prior capture precision/reassignment, Classic,
public Subshell, native session Reset/cancellation, and ordinary invocation
controls are also selected for the combined gate.

A separate native pointer-parameter defect remains outside this change:
passing `*sync.WaitGroup` as a parameter then deferring `Done` reports
`type *__gosource_import_0_0.WaitGroup has no method Done`. The synchronization
control uses a captured package-level native mutex/WaitGroup to test local
reference arguments without conflating that unrelated native-method issue.
The failed pointer-parameter attempt is retained in review logs and is not
counted as passing coverage. Native field/method resolution remains with its
separate owner.

Final combined focused gate: PASS, 97.946 seconds, under
`GOMAXPROCS=2 go test -race -p=2 ./interp`. All fourteen new original-source
controls passed in all three modes with exact streams; the combined selection
also passed eight ordinary invocation cases, prior capture registry and
captured-variable reassignment cases, precision/negative controls, Classic
snapshot checks, public Subshell isolation, and native Reset/cancellation.
The baseline mismatch and intermediate host-race reports are retained in
`.agents/review/` and are not passing evidence.
