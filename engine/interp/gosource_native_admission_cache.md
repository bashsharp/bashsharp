# Session-local native interface admission facts

Sprint: #243
Story: #676
Story-ID: 8aaa61718184

Repeated original callbacks can return different values of the same native
concrete type. Rechecking each value against the same interface sends identical
assignability and method-signature requests to the dependency process.

The worker now assigns opaque tokens using `reflect.Type` map keys. A token is
issued only for a concrete handle, never an interface-typed handle whose payload
may change. The authenticated response records the handle-to-token association
in that session. Caller-provided type strings and tokens are not cache authority.

Successful admissions are keyed by the concrete type token and the destination's
scoped bridge identity. Imports, local declarations, embeds and instantiated
types must still match the session's immutable registry before a hit is used.
Unknown handles, other sessions, canceled contexts and closed sessions cannot
hit. Reset/replacement starts a new session, so numeric tokens may be reused
without carrying any prior admission. Errors are not cached.

The fallback asks for the exact type's method signature, rather than binding an
addressable value's method. Binding could implicitly add pointer-receiver methods
to a value type; a method-set check must not do that. The worker returns only a
signature string. No receiver, bound function, payload, pointer update, or output
is reused. Ordinary reads and calls still execute against their actual values.

The focused controls exercise repeated colors with different values and no second
admission RPC, mismatched/missing methods, pointer versus value method sets,
defined parameter types, alias/scoped signatures, forged display metadata,
unrecognized handles, session changes, and Runner.Reset. Existing canonical
compatibility runners remain the authority for unchanged image/mutex deadlines.
