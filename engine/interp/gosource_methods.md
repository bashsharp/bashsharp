# Chained local Go method results

Sprint 118, story 53 (`99bd1de0093b`).

The official 64-bit generator calls `a.Uint64().Com().Int64()`. Its intermediate
receivers are function results, not flat identifiers. The Go-source selector
now records go/types method-value and receiver-addressability facts. A
function-valued field remains distinguishable from a method; typed JSON retains
these facts. The loader requests go/types selection information explicitly.

Computed local callees and explicit method values resolve through the existing
promoted-method binding machinery. An addressable receiver's address is evaluated
once, retaining indexed/selected storage and pointer identity. Explicit value
method values snapshot at creation. A directly called value method snapshots
its evaluated receiver after arguments, matching the native compiler's behavior
when an argument mutates the pointee; both argument-binding paths finalize this
before invocation or defer/task capture. The pending binding stores cells and
selection metadata, never a captured Runner closure. Original Go methods share
the package's live lexical scope, as ordinary Go functions already do.

A local call used inside dependency arguments transports its complete result
cell, rather than forcing a returned struct through scalar projection. Method
and function bodies remain interpreted. Native-member access, callback lifetime
policy and source bytes are unchanged.

Nine authored native/interpreter/artifact cases cover the exact extracted chain,
returned pointer aliasing, value and pointer method-value captures, indexed
receiver evaluation once, interface method values, receiver/argument ordering,
structured local results in native arguments, and direct/deferred value receiver
snapshots after argument effects. Compiled artifacts execute after sources are
removed with an empty PATH. Metadata and typed-JSON checks distinguish methods
from function fields. Existing general callback and callable-result race tests
also pass. The unnamed ordinary parameter arity gap discovered by an auxiliary
probe remains outside this slice and its failing original probe is retained.

The full unchanged generator still reaches an explicit native transport boundary:
`fmt.Fprintf` is not yet admitted when its arguments invoke original String/Error
methods, even if its writer is a native bufio.Writer handle. The interpreter
result remains FAIL. The complete retained child passes all three modes, and the
compiled generator output matches native Go byte-for-byte. The following narrow
native-writer formatting policy change is a separate commit and gate.

Raw complete attempts and historical failures are retained under manager evidence
`method-results-011`. No full-corpus completion is claimed.
