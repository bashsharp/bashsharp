# `sync.WaitGroup.Go` — the bounded asynchronous callback surface

Sprint #118, Story #54 (`c3a60493cde9`).

## The gap

Three unchanged Go originals — `examples/waitgroups`, `examples/atomic-counters`
and `examples/mutexes` — launch their goroutines with Go 1.25's
[`WaitGroup.Go`][wg] rather than a bare `go` statement:

```go
var wg sync.WaitGroup
for i := 1; i <= 5; i++ {
	wg.Go(func() { worker(i) })
}
wg.Wait()
```

`wg` is an imported dependency object, so `wg.Go(f)` reaches the native bridge
as an ordinary method call whose single argument is an *original* function
literal. The bridge encodes an interpreted closure as a `callback` handle, and
`validateLocalTransport` then refuses it:

```
gosource: asynchronous or retained original function callbacks are unsupported for Go
```

The refusal is correct and must stay. A callback handle is only sound while the
interpreter is blocked inside the very request that raised it — that is what
`synchronousFunctionCallback` allowlists (`sync.Once.Do`, `strings.Map`, …).
`WaitGroup.Go` is the opposite: it *retains* `f` past the return of the call and
invokes it from a goroutine the dependency process owns. Handing the dependency
a retained callback would need a persistent bidirectional callback protocol with
its own lifetime, cancellation and re-entrancy rules.

## What this story does instead

No such protocol is introduced. `WaitGroup.Go` is answered as a **bridge
operation of its own**, before the call is ever prepared as a native request:

1. the receiver is authenticated as a live `sync.WaitGroup` handle — by the
   native type the dependency reported for the object, never by the method name;
2. the *native* `Add(1)` runs synchronously on that handle, in the caller,
   before anything is launched;
3. the argument function is spawned through the interpreter's own task
   machinery — the same snapshot, launch handshake, trap and failure lifetime a
   `go func() { … }()` statement gets;
4. when the body returns, the *native* `Done()` runs on the same handle, from
   inside the task.

The original `func` body is therefore executed by the interpreter, exactly once,
in the task — never forwarded to the dependency, never re-compiled, and never
credited to a native implementation. Only `Add`/`Done` — arithmetic on a
counter the interpreter does not own — happen natively, and only against the
one handle the receiver already named.

## Why that is observationally equivalent

The pinned Go 1.27 `sync/waitgroup.go` reads:

```go
func (wg *WaitGroup) Go(f func()) {
	wg.Add(1)
	go func() {
		defer func() {
			if x := recover(); x != nil {
				panic(x)   // f panicked: do NOT Done, re-panic
			}
			wg.Done()      // f returned (or Goexit'd): Done
		}()
		f()
	}()
}
```

Point for point:

| Go 1.27                                   | this implementation                                          |
| ----------------------------------------- | ------------------------------------------------------------ |
| `Add(1)` before the goroutine exists      | native `Add(1)` before the task goroutine is started, and before `wg.Go` returns — so `Go` still happens-before a later `Wait` |
| `f` evaluated once, called once           | the argument expression is resolved to one closure and invoked once |
| `Done` deferred, after `f` returns        | native `Done` after the interpreted body returns              |
| no `Done` when `f` panics                 | no `Done` when the body leaves panicking                      |
| `Wait` unblocks when the counter hits zero| unchanged: `Wait` is still the dependency's own `Wait`        |
| the counter is the WaitGroup's            | unchanged: one native handle, `Add`/`Done`/`Wait` all on it   |

Captured cells, native handles and channel handles cross into the task through
the established task snapshot (`bashpp_task.go`), so a captured `atomic.Uint64`,
`sync.Mutex` or `Container` stays the *same* native object in the task and in
its launcher — which is what makes `atomic-counters` and `mutexes` agree with
real Go.

## What is deliberately not generalised

* **Only `sync.WaitGroup`.** The receiver must carry native type
  `sync.WaitGroup` or `*sync.WaitGroup`, as reported by the dependency's own
  `reflect`-derived type id. A user type with a `Go` method, or any other
  imported type with a `Go` method, is not claimed and keeps whatever behaviour
  it has today.
* **Only `func()`.** Zero parameters, zero results, matching the method's
  signature. A different shape is not claimed.
* **Only a shape whose evaluation is observation-free**: a function literal, or
  a name already bound to a function. Anything else — a call producing a
  function, say — would have to be evaluated in the launcher while the body ran
  in the task, so it is not claimed and still reports the existing diagnostic.
* **No fabricated callbacks.** The dependency is never asked to call back into
  the interpreter for this operation; `Add` and `Done` are ordinary one-shot
  requests with an integer argument and no argument respectively.

## Measured state of the three originals

| original                    | result                                                          |
| --------------------------- | --------------------------------------------------------------- |
| `examples/waitgroups`       | matches real Go (line set, stderr, status — the ten lines are a real schedule either way) |
| `examples/atomic-counters`  | matches real Go byte for byte: `ops: 50000`                       |
| `examples/mutexes`          | its launches now run; blocked further on **another** surface       |

All three are vendored verbatim under `testdata/gosource-waitgroup-go` and
pinned by SHA-256 against the upstream files, so the digests fail rather than
the comparison if a copy is ever edited.

## Exact remaining gaps, and why they are not this surface

Both are recorded as live tests rather than skips, so each flips to a full
three-mode comparison the moment its own surface lands.

### 1. A native value reached as a field of an original struct

`examples/mutexes` stops at:

```
type __gosource_import_0_1.Mutex has no method Lock
bash++: task failed: exit status 2
```

`c.mu` is read as a non-addressable copy of the `sync.Mutex`, so `Lock`'s
pointer receiver is not in the value's method set. This is not the
`WaitGroup.Go` surface: the identical failure reproduces with no `Go` anywhere,
using a plain launch and manual `Add`/`Done`:

```go
type Container struct {
	mu       sync.Mutex
	counters map[string]int
}
func (c *Container) inc(name string) { c.mu.Lock(); defer c.mu.Unlock(); c.counters[name]++ }

var wg sync.WaitGroup
wg.Add(1)
go func() { c.inc("a"); wg.Done() }()
wg.Wait()      // → type __gosource_import_0_1.Mutex has no method Lock
```

(That control also hangs until its deadline, because the failing task never
reaches its `Done` and the launcher is blocked inside the dependency's native
`Wait` — see the divergence below.)

### 2. An interpreter-owned pointer to a native value

`goSourceWaitGroupHandle` already authenticates a `*sync.WaitGroup` receiver,
because that is what the dependency reports for a pointer to one — but today no
original can reach it. `p := &wg` produces an interpreter pointer, which
`bashPPNativeExpr` does not recognise as naming a dependency object, so the
selector resolves against the interpreter's own method sets:

```go
var wg sync.WaitGroup
p := &wg
p.Add(1)       // → type *sync.WaitGroup has no method Add
```

Again independent of `Go`: `Add`, `Done` and `Wait` all fail the same way. It
matters for this story only because the waitgroups example's own doc comment
says a WaitGroup passed into a function should be passed by pointer.

## Known remaining divergence

When the launched body panics, real Go re-panics on a bare goroutine and the
process dies. The interpreter records a task failure and cancels the task
group, but a launcher already blocked inside the dependency's `wg.Wait()` is
not itself cancellable, so the run stops on its enclosing deadline rather than
on the panic. That is the pre-existing lifetime of *any* native blocking call
during a task failure — a bare `go func() { panic(…) }()` alongside `wg.Wait()`
behaves the same today — and is not introduced here. Cancelling a native
blocking request on task-group failure is the separate protocol change this
story deliberately did not take.

[wg]: https://pkg.go.dev/sync#WaitGroup.Go
