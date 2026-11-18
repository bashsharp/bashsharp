# Task snapshots and imported native handles

A Bash++ task (`go f()`, and the goroutines an original Go program lowers to)
runs as a goroutine over a cloned `Runner`. The snapshot deep copies every
mutable value reachable from the shell environment, because the parent keeps
running concurrently and sharing interpreter heap between the two would be a
data race. `interp/bashpp_task.go` carves out the one class of value for which
copying is wrong.

## Why native handles are not copied

An imported native value — `sync.WaitGroup`, `sync.Mutex`, `atomic.Int64`,
`*os.File`, a channel — is not interpreter state. The object lives in the
dependency session process and the interpreter holds only a session-scoped
ticket for it, a `bashPPBridgeValue` with a `Session` id and a `Handle` number.

Deep copying that ticket's *meaning* is not possible: the interpreter never has
the object, so there is nothing to copy. Copying the ticket while minting a new
handle would be worse than an error, because the original Go program's meaning
depends on the goroutine and its parent naming the *same* object:

```go
var wg sync.WaitGroup
wg.Add(1)
go func() {
    wg.Done()   // must release the wg the parent waits on
}()
wg.Wait()       // otherwise blocks forever
```

Before this layer existed the snapshot refused these programs outright with

```
bash++: task failed: task snapshot: unsupported mutable Bash++ object type *interp.bashPPBridgeValue
```

and a partial fix that copied the object would instead have hung the parent in
`wg.Wait`.

## The rule

`(*bashPPObjectCloner).cloneNativeHandle` is identity preserving and nothing
more:

- **The descriptor struct is copied.** `Elements`, `Fields` and `Entries` get
  their own storage, so a task writing through its descriptor cannot race the
  parent and `go test -race` stays clean.
- **`Session` and `Handle` are carried across verbatim.** Both sides keep
  addressing the one native object. Nested handles inside a struct or map
  descriptor are preserved the same way.
- **Aliasing survives.** Two shell names for one native object clone to one
  descriptor, so they are still two names for that object inside the task.
- **Unusable handles fail closed at the snapshot**, rather than being forwarded
  to whichever session the task starts next.

Goroutine and function bodies stay interpreted throughout. Nothing here
forwards original Go source; only the ticket travels.

## Staleness

Two checks, at different layers:

- At the snapshot, `bashPPNativeHandleScope.checkHandle` rejects a handle with
  no minting session (`Session == ""`), and a handle whose session slot is
  already `nil` — which is what `Reset` and the public `Subshell` leave behind
  when they close the dependency session. The check is deliberately
  conservative: a snapshot taken *before* the session has started is allowed
  through, because a not-yet-started session is not a closed one.
- At every request, `(*bashPPNativeSession).request` rejects a handle whose
  `Session` is not the session's own id. That is the authority check, and it is
  what catches a handle which crossed into an independent session.

The snapshot check compares session *slots*, not minted ids, on purpose:
reading `bashPPNativeSession.id` requires the session's start lock, which a
concurrent first request holds across a whole dependency build.

Errors name the handle as `type#number` and never include the session id, which
is an authority token.

## Tests

- `interp/bashpp_task_internal_test.go` — the cloning rule itself: identity,
  aliasing, descriptor storage, unbound/stale rejection, and the runner-level
  snapshot regression.
- `interp/bashpp_task_test.go` — original Go programs in
  `interp/testdata/gosource-task/`, run by the real Go toolchain and by the
  interpreter, with output *and* exit status diffed.

## Known gaps outside this layer

These are reproducible with no task involved and belong to native method
dispatch, not to task snapshots:

- `defer wg.Done()` does not route a native handle receiver to the bridge; it
  falls through to interpreted method lookup and fails with
  `type sync.WaitGroup has no method Done`. Reproducible from `main` with a
  plain `func() { defer wg.Done() }()`.
- A pointer taken to a native handle (`named := &total`) loses bridge dispatch:
  `type *sync/atomic.Int64 has no method Add`.
- An ordinary interpreted variable mutated inside a task is not visible to the
  parent, by design of the deep-copy snapshot; original Go programs which share
  a plain variable across goroutines under a mutex therefore diverge from Go.
