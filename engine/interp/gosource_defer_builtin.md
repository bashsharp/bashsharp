Sprint: #118; Story: #53; Story-ID: 99bd1de0093b

The original Tour binarytrees_quit program deferred `close(quit)`, but deferred
call resolution omitted the GoSource close path and tried to execute a shell
command at function return. Direct close calls already used the channel engine.

The fix registers an internal deferred operation only for the unshadowed
GoSource close builtin. Registration evaluates the original channel expression
once, including argument side effects or panic, and retains the resulting
channel capability. Unwinding closes that saved channel through the same engine
as a direct call, preserving native handle identity and interpreter-owned channel
identity. Nil and double-close panic at unwind time; a panic during argument
evaluation registers no close. Existing LIFO, recovery, task ownership and
cancellation machinery remains responsible for execution. No original body is
forwarded to native Go.

The implementation is intentionally limited to deferred close. Classic Bash++
shell dispatch and declared functions named close follow their existing paths.
Other deferred builtins are not newly certified by this change.

Validation: unchanged Tour source in all three modes; authored controls for
argument evaluation timing, reassignment, LIFO during panic, nil/double close,
argument panic, named directional channels, pointer payload channel identity,
and function/local-variable shadowing. A classic deferred shell function named
close guards the GoSource boundary. Existing unified channel and function/defer
controls run alongside these tests.

The focused original plus authored race gate passed. A separate five-repeat
original run still observed one failure after both expected PASSED lines: a
remaining tree-walk task reported a closed native TCP connection during EOF
cancellation. This is retained as an open task/native-session lifecycle issue;
it is not suppressed by the close fix. The original test stays enabled and
strict. In particular, unrelated deferred operation errors remain failures even
when another defer recovers an earlier panic.
