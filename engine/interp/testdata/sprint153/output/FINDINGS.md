# S153.5 output and exit findings

| Root / reproducer | Mechanism | State |
| --- | --- | --- |
| `interleaving/main.go` | A native dependency request replies on its TCP control channel before the child stdout/stderr copy has drained. The next interpreted statement can therefore overtake a preceding `fmt.Println` or `fmt.Fprintln`. | open: `interp/bashpp_native_bridge.go` needs a per-request output-drain/barrier protocol; this lane must not edit bridge files. |
| `fixedbugs/bug352.go` | Interpreter writes `BUG: bug352 [0]byte` and `BUG: bug352 struct{}`. | open: evaluator value/layout identity, outside this lane. |
| `fixedbugs/issue12577.go` | Interpreter writes `BUG: got 0 want -0.0` twice. | open: evaluator floating-point signed-zero formatting/value, outside this lane. |
| `fixedbugs/issue14646.go` | Interpreter observes the host `reflect/value.go` call site rather than the original source call site. | open: runtime.Caller/source-location bridge, outside this lane. |
| `fixedbugs/issue73917.go` | `fn: command not found`. | open: evaluator function value/call, outside this lane. |
| `fixedbugs/issue73920.go` | `fn: command not found`. | open: evaluator function value/call, outside this lane. |
| `fixedbugs/issue8047b.go` | `g: command not found`. | open: evaluator function value/call, outside this lane. |
| `fixedbugs/issue21879.go` (compiled) | Native recipe emits two `main.main` lines; transpiled mode not yet measured with the required recipe. | pending: use compiled harness. |
| `fixedbugs/issue20014.go` (compiled) | `runindir` recipe, not runnable as a single file. | pending: use compiled harness. |
| `fixedbugs/issue7690.go` (compiled) | Native output empty. | pending: use compiled harness. |
| `inline_callers.go` (compiled) | Native output empty. | pending: use compiled harness. |
| `uintptrescapes3.go` (compiled) | Native output empty. | pending: use compiled harness. |
| `maymorestack.go` | Requires its `-gcflags=-d=maymorestack=…` recipe. Single-file interpreter instead refuses `[1 << 10]byte`. | open: bridge array type registration; outside this lane. |
| `fixedbugs/issue4620.go` | Interpreted single-file run matches native (empty streams, status 0). | no current failure. |
| `fixedbugs/issue19467.go` (compiled) | linked package name reaches `runtime.Caller` as `__gosource_pkg_0_…`; likely linker/runtime-frame seam. | open: do not edit `gosource/`; reduce and assign after measurement. |
| GoSource panic with `GOTRACEBACK=none` | Interpreter previously printed an invented GoSource traceback even though Go suppresses it. | fixed by this commit: `bashPPPanicTerminate` honours the runtime setting. |
