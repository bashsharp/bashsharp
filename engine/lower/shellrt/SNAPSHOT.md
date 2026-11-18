# Native shell-copy snapshots

Generated subshells must clone the complete visible typed graph before running
any child statement. Register the addresses of actual child variables. Returning
cloned values and then assigning them to child variables breaks pointers such as
`p := &x`: `p` would point to an invisible temporary instead of the child `x`.

```go
sourceX, sourceP := &x, &p // save parent addresses before any shadow
func() {
    x, p := *sourceX, *sourceP // infer native types; still shallow initially
    snapshot := shellrt.NewSnapshot(parentReadonly, childProgram.Channels)
    shellrt.MustReadonly(shellrt.Capture(snapshot, sourceX, &x))
    shellrt.MustReadonly(shellrt.Capture(snapshot, sourceP, &p))
    shellrt.MustReadonly(snapshot.CloneContext(ctx))
    childReadonly := snapshot.Readonly()
    // Child body uses x, p, childReadonly, and the child program.
}()
```

Capture all roots before cloning. The caller owns the parent graph during
cloning; it must not mutate concurrently. After success, the two graphs may be
mutated independently. On cancellation or rejection, do not execute the child
body or use its partially populated bindings. The helper creates no goroutines,
executes no callbacks, acquires no foreign resources, and never modifies the
parent's graph or readonly registry.

Pointers rebase to captured roots and addressable struct/array/slice elements.
One memo preserves pointer/map cycles and aliases across separate roots,
interfaces, named map conversions, and overlapping subslices including capacity
tails. Structs and arrays retain value-copy semantics. Dynamic interface types,
typed nils, nil collections, and slice lengths/capacities are preserved. Empty
objects follow Go's unspecified zero-size pointer identity rules.

Readonly roots, objects, and slice regions are mapped to cloned identities in a
separate `ReadonlyState`. Child-only marks remain local. Use that state for every
child mutation guard; the parent state cannot recognize cloned addresses.

Channel handles are opaque. Supply the child's fresh `ChannelScope` explicitly
when capturing non-nil channels; cloning rejects a scope that already owns a
captured channel. The handle retains identity and buffer contents while child
operations fail ownership checks. This is the subshell boundary policy from
`interp/bashpp_concurrency_test.go:TestBashPPChannelRefusesSubshell`. Structured
tasks have a different channel ownership contract and must not reuse this policy
implicitly. Snapshot does not revoke or close the parent's channels.

Non-nil functions require compiler-managed closure rebinding and are rejected.
Unsafe pointers and structs with inaccessible fields are rejected, including
opaque imported resources; they are not silently copied. Compiler support for
such values needs an explicit certified adapter or a positioned unsupported
boundary diagnostic. Passing a handle through an interface does not bypass
validation. The helper is not a serializer and does not infer ownership from
JSON, printed values, or a type's name.

`TestSnapshotPublicPointerAndReadonlyOracles` executes the public pointer subshell
and Bash# readonly mutation sources through the interpreter and compares their
observations with this helper. The other snapshot tests probe graph identity,
shared storage, readonly rebasing, cancellation, and parent/child mutation races.
These are runtime-helper checks. Whole-source compiler/artifact acceptance is a
separate lower-package gate.
