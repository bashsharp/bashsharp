# Original method calls launched as goroutines

Sprint: #118  
Story: #51  
Story-ID: 825f8083451e

The unchanged Tour `mutex-counter.go` failed at `go c.Inc("somekey")` because
task capture accepted named functions and literals but refused selector
callees. Its original source SHA256 is
`74488ecc347a123c8538069175ffeef5ea38577bb37b2c7a9ceb6bba47ab02b4`.
A retained build overlay using the previous callee implementation reproduces
that exact positioned failure without changing the original fixture.

Local method callees now bind in the launching Runner. The task carries an
ephemeral bound-function pin, avoiding a second receiver evaluation and avoiding
new persistent receiver entries in the parent's closure registry. The child
gets the method's exact lexical scope plus an independent receiver parameter
value. Pointer receivers retain their pointee; assigning the receiver parameter
does not rebind the caller's variable or a stored method value. Value receivers
copy their value while retaining interior references. Interfaces, promoted
methods, nil pointer receivers handled by original methods, computed receivers,
and stored method values use the same original method execution path.

Addressable value receivers are copied after call argument evaluation, matching
the pinned native Go behavior. A discarded eager copy would still read shared
user storage before an argument's synchronization, so that early copy was
removed from `goSourceLocalMethod` too. Addressable concrete receivers carry
only their address and type; constructing a temporary checked JSON object
would recursively inspect referenced map/slice storage before the method
acquired its lock. GoSource typed receiver copies copy fields directly without
that JSON traversal, while Classic keeps its existing checked-object path. Existing ordinary-method controls cover
this shared helper change. Function and argument expressions are not rewritten
or forwarded to a native dependency helper; original method bodies continue to
run in the interpreter.

Focused validation includes the byte-identical Tour example in interpreter,
native oracle, and source-free compiled-artifact modes, twelve authored
three-mode receiver controls, three fresh-session Reset cycles, a receiver
argument synchronization race adversary, prior ordinary method cases, task
argument controls, capture precision, Classic/public Subshell isolation, and
native cancellation/Reset. No stdout/stderr comparator is relaxed. Port 8090 is
not used by these checks.

Direct goroutine calls to imported native functions/methods remain outside this
bounded original-method capture path and continue to fail closed where there
is no original callable body. This change does not alter defer-close or native
session shutdown logic.

The exact unchanged Tour example passed three consecutive all-mode runs under
the race detector (19.120 seconds combined) after the receiver traversal fix.
Earlier rejected runs, including the positioned baseline refusal, incorrect
value-receiver timing, and host-race reports, remain in review logs and are not
counted as passing evidence.

The final combined focused race gate passed in 86.855 seconds. Its retained
raw log is `.agents/review/task-methods-release-race.log`; the earlier repeated
original-source gate is `.agents/review/task-methods-original-repeat-race.log`.
