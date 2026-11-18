# GoSource channel-send review

Sprint: #118; Story: #51; Story-ID: 825f8083451e.

The raw `9d0fac236a226540241257689d0100216e7cb334` submission retained only
the RHS AST and converted its evaluated value with `fmt.Sprint`. That loses
numeric precision and structured value identity, leaves computed channel operands
as text, and lets a closed select arm stop evaluation of later operands.

The reviewed implementation retains positioned channel and RHS expressions through
conversion, typed JSON, walking, printing and lowering. The interpreter evaluates
the channel operand first and the RHS once, before waiting; select evaluates its
send operands in source order before registering or selecting arms. Typed nil
send channels still evaluate their RHS and block, or permit a select default.
Closed Go sends unwind the interpreted panic/defer stack. Classic Bash++ retains
its literal-word payloads, errors and channel capability boundary checks.

GoSource channels carry interpreter value cells. Scalars retain their type and
precision; structs and arrays copy at send, while slices, maps, pointers and
channels retain their existing reference semantics. Receives, select bindings,
range bindings and imported-call arguments retain that cell. Imported `fmt` only
formats values; original expressions and functions execute in the interpreter.
No original program body is forwarded to a native dependency worker.

Validation uses seventeen authored complete programs and three byte-identical,
SHA-bound Go by Example programs. Each runs through a real native Go oracle,
`Runner.Run`, and a genuinely built lowered native artifact. Native artifacts run
after both original and generated source files are removed. Raw stdout, stderr
and successful status must agree; there are no output filters. Additional tests
observe RHS output, prove nil/full/select sends stay blocked, cancel the Runner,
and require prompt join, cancellation status 1 and no statement after the send.
An AST tamper test ensures stale compatibility words cannot replace the original
positioned operands, including after typed-JSON round trip.

This slice is not full Go concurrency acceptance. Computed and nil local receive operands are covered by the follow-up in
[gosource_receive.md](gosource_receive.md). Dependency-owned native channels and
arbitrary channel-bearing aggregate storage still depend on other runtime paths. The
existing panic engine carries text, so this slice proves closed-send unwind and
message behavior, not the recovered `runtime.Error` dynamic type. Public
shell-copy/task-group ownership restrictions remain in force. Broader goroutine
cloning and scheduling are outside this send-evaluation repair. In particular,
these focused passes do not recertify any frozen corpus ledger.
