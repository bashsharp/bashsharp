# Sprint 162 perf-design findings

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:abi/fibish.go` | Recursive calls repeatedly traverse the generic syntax/statement path and allocate/copy typed frames and scalar cells; the source is the smallest deadline root and its two-result short declarations add cost beyond the one-result control. | Interpreter per-call cost; a semantics-complete non-recursive scalar instruction path is the only candidate with a bounded order-of-magnitude gain. | design; no product change; keep FAIL pending D2 |

## Requests to other seams

None. Deadline rows whose scaled controls indicate bridge, collection,
concurrency, finalizer, or nontermination causes must be reassigned by their
owners from fresh verdict evidence; this lane did not inspect them deeply
enough to request an exact diff.
