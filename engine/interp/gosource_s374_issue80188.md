# BLOCKERS: Go issue 80188 resident memory

The full upstream program was not run. A standalone scratch copy retained its
40 `new(T)` calls per iteration, reduced the two delay loops from 10,000 to
100, and varied the outer iteration count. On `macos-host`, every run
was polled at 100 ms and killed above 4 GiB RSS or 120 seconds.

Before the changes, one interpreted goroutine measured:

| iterations | maximum RSS | elapsed |
| ---: | ---: | ---: |
| 100 | 23,424 KiB | 1 s |
| 1,000 | 302,240 KiB | 2 s |
| 2,000 | 573,744 KiB | 3 s |
| 4,000 | 1,091,024 KiB | 5 s |

A heap dump taken 3.5 seconds into the 4,000-iteration run retained 240.38
MiB. It showed the program's `all` slice retaining each interpreted allocation
as `bashPPPointer -> bashPPCell -> map[string]any` plus a
`bashPPCollectionMeta` tree. `Runner.bashPPPointerExprValue` allocated the
pointer target and called `Runner.bashPPZeroValue`; the latter retained 120.52
MiB, chiefly the payload and per-field layout maps. At that point the profile
also reported 3,277.62 MiB cumulative allocation. `bashPPBeginShortDecl` alone
accounted for 1,273.75 MiB because each `:=` copied every unrelated cell in
the current scope.

The contained changes in this branch bound a short-declaration rollback by its
existing left-hand targets and omit nil layout entries for scalar struct
fields. At the same 4,000-iteration/3.5-second heap-profile point, retained
heap was 193.17 MiB and cumulative allocation was 2,541.17 MiB; the profiled
process peaked at 906,448 KiB RSS (versus 1,254,288 KiB for the corresponding
pre-change profiled run). A final four-goroutine run of 1,000 iterations each
peaked at 974,416 KiB and exited successfully in 2 seconds. The native binary
for that same source reported 9,584,640 bytes maximum RSS via
`/usr/bin/time -l`.

The remaining issue is not an unbounded cache or a lost frame: the source
intentionally keeps all returned pointers live until its validation loop. The
interpreter represents every four-int `T` with a large cell, pointer, hash-map
payload, metadata object, and repeated pointer/type metadata temporaries. The
scaled RSS slope therefore projects beyond the 12 GiB host for the original
four times 10,000 iterations even after the contained reductions.

A complete fix needs a compact interpreter value representation: immutable,
per-type field layout shared by all instances; indexed field storage instead
of one `map[string]any` per small struct; and runner-local interning of immutable
pointer/type metadata. That change crosses struct selection and assignment,
value copying, reflection/native transport, task snapshotting, and concurrent
field access, so it is not safely contained in this story's two allocation
sites.
