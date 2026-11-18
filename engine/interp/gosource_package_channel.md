# Channel variable initialization

Sprint #118, Story #53, Story-ID `99bd1de0093b`.

Go source channel variables now evaluate their initializer once and retain the
whole typed channel cell. Package initializers continue to follow the existing
`go/types.InitOrder`; no source scheduling or rewriting is introduced. Variables
initialized from another channel preserve channel identity, ownership, capacity,
and their declared direction and named type. Nil channel declarations retain
an explicit channel type instead of a scalar initializer string.

Both var and short-declaration make expressions use the existing unified
allocation decision: native scalar/dependency channels stay in the authenticated
session; original reference payload channels stay in interpreter-owned storage.
The native channel binding operation checks assignability before creating a
typed view of the same channel. It performs no send or receive. Local channel
type descriptors and canonical type rendering support aliases and defined types.

Focused three-mode controls cover package dependencies, capacity effects,
function and function-literal initializers, goroutine channel use, aliases,
channel direction as observed through interfaces, named type identity, nil
channels, and synchronous pointer payload identity. Invalid source declarations
remain type-checker errors; native binding controls reject direction/element
mismatches without consuming queued values. Existing cancellation/reset and
classic-channel regression controls remain required.

A distinct pending task captures `go send(p)` pointer arguments incorrectly:
with `type S struct{N int}`, a global `chan *S`, and a pointer sent through that
go call, the received pointer compares unequal and mutation does not reach the
original. The synchronous `send(p)` transport control passes. The manager
assigned the capture successor; this commit does not claim to fix that failure.
Raw reproduction and observed results are retained in the sprint evidence at
`product-all-008-control/package-channel-022/pending-pointer-go.json`.
