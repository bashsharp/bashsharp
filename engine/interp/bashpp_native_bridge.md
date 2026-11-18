# Go-source dependency sessions

Sprint: #118 · Story: #50 · Story-ID: cf81e4868348

This is a bounded implementation of imported dependency execution for the
Go-source interpreter. It does not certify complete Go compatibility.

The interpreter evaluates original program expressions, declarations, and local
function bodies. A persistent child executes exported imported functions and
methods through a fixed reflection protocol. Generated helper source contains
dependency imports, export/type registries and private connection constants. It
never contains the original program body, expression source, or oracle output.

`GoSourceModuleDir(originalModuleDirectory)` binds import resolution and helper
builds to the original module while `Dir(runtimeDirectory)` controls dependency
runtime cwd and assets. The selected SDK supplies export data and builds the
helper. SDK build environment overrides are separated from the program runtime
environment. The import policy runs before a dependency starts.

All dependency imports, including blank imports, initialize before original
package globals, init functions and main. Program arguments, stdin, stdout,
stderr and runtime environment are supplied when the child starts; transport
credentials do not appear in its arguments or environment. Named scalar types
retain package-qualified identities. Native objects use session-tagged handles;
no Go heap pointer crosses the process boundary. Handles live until the session
ends. Each Run closes its session; Reset cannot preserve dependency process
state. Cancellation kills the owned process group on Unix and removes generated
helper source/artifacts. Other platforms kill the helper process.

The targeted regression command is:

```
GOMAXPROCS=2 GOTOOLCHAIN=go1.27.0 go test -race -p=2 ./interp \
  -run '^TestGoSource(PersistentNativeBridge|Bridge)' -count=1 -v
```

The verified set contains 13 original-source cases compared across native Go,
Runner, and a compiled lowered artifact: persistent environment state, scalar
results, interpreter side-effect order, slices, maps/anonymous structs, named
scalars, native time/random handles and methods, nil errors, tuple arguments,
stdin/argv, dependency subprocess environment, and a blank constraint-package
import. Two private-module tests compare native Go with Runner for blank/named
imports, initialization order, original arguments during dependency init,
constraint-only interfaces, generic aliases, and separate source/runtime cwd.
Three lifecycle tests verify Reset environment isolation, public Subshell/Reset
ownership with independent parent and child dependency state, and cancellation
while a real dependency subprocess is active, including process termination and
cleanup.
These are scoped tests, not full corpus or cross-platform certification.

## Structured native values (Sprint #118, Story #52, Story-ID d564bada90bb)

An imported call or package variable that is not a scalar answers with a
session handle. The original program's `len`, `cap`, index, slice, field and
`range` expressions read through that handle, so the dependency keeps ownership,
identity and mutation of the value and no interpreter copy of the sequence is
materialised. Only the access is forwarded; the surrounding expression,
statement and loop bodies stay on the interpreter path. A scalar element binds
as an ordinary typed interpreter variable, a non-scalar element keeps its
handle, and Go's own index/slice bounds errors are reported by the dependency.
`os.Args[3]`, `os.Args[1:]`, `u.Host` and `for _, e := range os.Environ()` are
the covered shapes. The three-index slice form is refused rather than
reinterpreted, because its capacity operand has no interpreter meaning.

A dependency process that ends on its own — `os.Exit`, or a successful
`syscall.Exec` replacement — is now reported as the original program's exit
status instead of a bridge failure. A non-zero status becomes the interpreter's
status and suppresses later bridge diagnostics on the same unwind; a zero status
ends the run with no error. Deferred interpreter work does not run, matching Go.
The session is closed at that point, so a later `Run` starts a fresh dependency.

Handles store an addressable copy, so pointer-receiver methods of a value-typed
native remain reachable and keep mutating the value behind the same handle.

`TestGoSourceNativeStructuredValues`, `TestGoSourceNativeSelfTermination` and
`TestGoSourceNativeSessionLifecycle` compare unchanged original sources against
a real Go build of the same file on stdout, stderr and exit status.

Still unsupported after this slice, and not counted as coverage: declaring a
variable of an imported non-scalar type (`var ops atomic.Uint64`,
`var mu sync.Mutex`, `embed.FS`), which needs the shared declaration and type
registry paths; carrying a handle through an interpreted function's parameters
and results (`defer closeFile(f)`); taking the address of an interpreter
variable for a native out-parameter (`flag.IntVar(&n, ...)`); interpreted
callbacks passed into dependencies (`wg.Go(func(){...})`); and indexing a plain
interpreter string. The generated helper workspace is also still visible in the
runtime directory while the program runs, so a program that lists its own cwd
does not match Go byte for byte.

Remaining unsupported or uncertified semantics include interpreted callbacks
passed into dependencies, generic imported calls, mutation/alias preservation
when interpreted aggregates are copied into native arguments, complete local
named aggregate type identity, general native panic/recover interoperability,
and cloning live native state into a public Subshell. Native handles from a
different session fail closed. Constraint-only interfaces and generic type
aliases are omitted from runtime reflection registries; blank imports register
no exports. These limits require parent-story follow-up and must not be counted
as passing corpus coverage.

## Original local types in the dependency (Sprint #118, Story #54, Story-ID c3a60493cde9)

The original program's own named types are materialised in the generated
helper as real Go declarations, read from the loaded program rather than from
declarations executed so far — the dependency session starts with the imports,
before any type declaration runs, and its type namespace is fixed for the life
of that session. `fmt.Println(Vertex{1, 2})` therefore reaches a `main.Vertex`
with the original field names, field types and defined type name, and `%T`,
`%v` and `%s` report what Go reports. Struct values, struct literals, struct
pointers (`&v` and `*p`), defined scalar and array types, slices and maps of
structs, declared interfaces, the empty interface and `error` all cross. A
value of a defined scalar type is re-read at that type's underlying kind, so
`type Celsius float64` arrives as a float and not as the shell's text form.
Original pointer receivers have a session-scoped identity. Their method bodies
bind to the original interpreter storage; mutation is sent back to the helper's
canonical pointer so aliases and subsequent formatting observe the same state.
General native out-parameters are refused before dispatch. Value receivers that
contain copied reference storage are also refused before dispatch; no mutation
is silently dropped.

No original method body is compiled into the helper. A local `String` or `Error`
method becomes a protocol stub. The request waiting for the dependency executes
the callback synchronously on its own Runner. Callback-capable requests serialize
at the session boundary; nested imports from that callback can reenter. Requests
without local callbacks retain ordinary concurrent native dispatch, including
native synchronization. This avoids borrowing the root Runner from an unrelated
interpreted task. A dependency handle that may retain a local callback carries
that provenance with its native session.

An original panic travels back as a panic to the native method stub, so `fmt`
performs its own recovery and prints its own panic diagnostic. Interpreter errors,
cancellation and explicit program exit remain failures or termination of the
owning request. There is no global callback-failure bucket and no unconditional
exit-status rollback. Original struct tags remain in the helper declaration.

A type the helper cannot reproduce exactly — a channel or func field, a generic
declaration, an imported element type, an embedded field, a name the fixed
helper template already uses — is not materialised, and the dependency answers
`unregistered bridge type`. A struct with an unexported field is materialised
but refused by field name when it is transported, because reflection cannot
write that field; it is never delivered with the field silently zeroed. Only `String` and `Error` are mirrored. Omitted methods fail closed when a
non-formatting dependency could observe them; omitted `Format` and `GoString`
methods also refuse formatting. Nil pointer callbacks and copied reference
receivers are explicit unsupported cases. General interpreted function callbacks
and native mutation of interpreter aggregates remain unsupported.

`TestGoSourceLocalTypeValues`, `TestGoSourceLocalTypeMethodCallbacks` and
`TestGoSourceLocalTypeUnsupported` compare unchanged original sources against a
real Go build of the same file on stdout, stderr and exit status. They are a
scoped slice, not Tour or corpus certification.


`TestGoSourceCallbackEffects` adds native-Go differential checks for pointer
mutation and aliasing, nested reentry, concurrent interpreter tasks, `fmt` panic
recovery, explicit exit, wrapped errors and original struct tags.
`TestGoSourceCallbackCancelReset` cancels a nested native sleep from an active
original callback and reruns a fresh program after Reset. Negative transport
checks verify native Go accepts the program while this implementation refuses
unsupported semantics before the original method or subsequent statement runs.
These focused tests run under the race detector.

Two independently observed task/send limitations remain open, outside this
transport correction. `testdata/gosource-callback-blockers/native-handle-task.go.txt`
records the original WaitGroup capture probe: task snapshotting rejects its
native bridge handle and the parent waits. A direct channel send of an imported
call expression also sends its source text rather than its result; callback
concurrency coverage uses a separately evaluated local value to isolate callback
ownership. Neither failure is classified as passing corpus coverage.

`TestGoSourceCallbackBodyFailure` retains an explicit unsupported runtime case:
a nil-slice index currently triggers an interpreter implementation failure. The
callback boundary reports it as failure and stops the request; it never turns it
into a successful formatting placeholder. This differs from native Go's bounds
panic recovery and is not counted as semantic equivalence.
