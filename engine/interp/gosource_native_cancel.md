# GoSource task EOF and native session cancellation

Sprint: #118; Story: #56; Story-ID: 3ef468f4e831

The unchanged Tour `solutions/binarytrees_quit.go` sometimes printed both expected
PASSED lines and then failed with a closed TCP socket, a malformed native
comparison, or a positioned `context canceled` diagnostic during EOF task cleanup.

GoSource child expansion now uses the same task context as explicit statements.
The native session records cancellation only when a cancellation closer actually
closes its connection, or its command context requests a process kill. A locally
closed write is classified as cancellation only when that provenance exists and
the requesting context is canceled. Explicit process exit statuses, live-context
requests, unrelated network failures, and callback failures retain their errors.
Connection close/provenance publication is serialized with writes and exit
classification. A canceled GoSource task unwinds native expressions through the
existing interruption sentinel instead of emitting a scalar diagnostic.

The source fixture remains 1297 bytes, SHA-256
`25157973b97a82b066516ea92df34bbb6ea26294603a471be5523b2ab19a8f03`.
No original source body, generated child source, comparison rule, or deadline was
changed. Imported helper execution remains the existing dependency bridge.

Validation uses the original fixture in native Go, interpreted GoSource, and
compiled artifact modes, with ten repetitions under the race detector. Separate
race controls cover real locally closed TCP sockets, live parent requests,
connection reset/EOF/body errors, explicit process exits versus signaled kills,
callback body failures, task-method Reset, deferred Classic dispatch, Unwrap
cancellation/Reset, and native channel cancellation/Reset.

A separate authored `go func(){os.Exit(7)}(); <-done` probe remains a parity FAIL:
Go exits 7; the interpreter preserves `task failed: exit status 7` but can return
1 after the parent's receive is canceled. The committed containment control
asserts only that this real failure is never suppressed or replaced by a
cancellation diagnostic. Raw parity failure and all intermediate original-fixture
failures are retained in
`$HOME/.local/state/bashy/sprint118-evidence/native-cancel-025`.
