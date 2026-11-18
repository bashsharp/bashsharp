# GoSource receive operand follow-up

Sprint: #118; Story: #51; Story-ID: 825f8083451e.

Receive statements and receive declarations now retain `ChanExpr`, just as sends
retain both operands. The positioned expression survives typed JSON, walking,
printing and native lowering. The GoSource interpreter evaluates that expression
once before waiting, including function calls, parentheses and a receive whose
result is another channel. All local select receive operands and send operands
are evaluated in source order before any arm is selected. Classic Bash++ still
uses its existing words and ownership checks.

The shared operand resolver preserves typed channel cells, task-group ownership
and direction checks. A typed nil channel has neither a ready communication nor
a close notification, so a nil receive blocks, permits a select default, or exits
when the owning context is cancelled. A closed channel of channels produces a
typed nil channel, not a rendered string. Closed ordinary/structured receives
retain the existing typed zero and comma-ok behavior.

Nine authored complete programs run through a real Go oracle, the interpreter,
and a built native artifact with source files removed before artifact execution.
They check computed scalar/declaration/assignment operands, closed two-result
receives, nil defaults, selection evaluation order, panic ordering, nested
receives, structured results and closed-channel nil zeros. Three further tests
observe the operand side effect, prove the receive remains blocked, cancel it,
and require prompt join, status 1 and no later statement. A typed-JSON/AST tamper
control rejects stale shadow words. The existing unchanged upstream channel
fixtures and classic channel regressions remain part of the focused gate.

This does not support every valid Go channel program. Channel-bearing aggregate
storage such as `[]chan int{c}` still fails at the collection representation
boundary. A negative guard records that failure rather than allowing a scalar
to impersonate a channel. General assignment-shaped select arms and native
channels remain separate work.

The exact unchanged GbE timers, tickers, timeouts and rate-limiting programs were
also probed in all three modes. Their oracle and compiled processes run, while
interpretation still fails at native `<-chan time.Time` handles. Raw timing-bearing
outputs are retained without normalization or a timing-output equality claim.
Those probes are failures of remaining runtime coverage, not successes of this
slice. No frozen corpus ledger is recertified.

Native channel protocol work must preserve atomic selection. Sequentially
polling and consuming native arms before choosing a local arm is unsound: it can
consume an unselected event. A blocking native receive also must not hold the
only dependency RPC lock while a concurrent Stop/cancel operation needs that
same lock. Direct native receives require authenticated cancellable request
multiplexing; mixed native/local select requires a coherent rendezvous authority.
This change makes no native-worker or bridge-protocol edits and keeps that
boundary fail-closed until those properties are proved.
