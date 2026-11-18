# GoSource receive declaration capture

A receive short declaration used the channel's legacy display word during task
capture. The typed receive visitor already handled selector roots, but this
separate path bypassed it. In the unchanged Go by Example ticker, the launched
function consequently lost the `ticker` cell at `case t := <-ticker.C`.

Receive declarations now visit the existing typed receive command before
binding their result names. This preserves select-case lexical scope and the
existing legacy fallback. No source expressions or original bodies are rewritten.

The precision regression covers ordinary receive declarations, one/two-result
select receives, assignment receives, shadowing, computed operands, and existing
send/literal controls. Five cases fail before the fix; all eleven pass afterward.

The original ticker fixture is copied byte for byte from
`bashpp-tests/examples/tickers/tickers.go`, SHA256
`9dfd404595ed484a3f4e19ec9ab712d2d3451d2555f75197c94296af234d532f`.
Its three-mode comparison requires exactly three ticks followed by `Ticker
stopped`, exact stderr, parseable strictly increasing wall/monotonic timestamps,
and wall times inside the actual test observation window. Only validated times
are normalized. The compiled artifact executes after original and generated
source removal. Original timers and cancellation/Reset remain regression guards.

The requested `TestRunnerRunConfirm` was also executed with actual GNU Bash
5.3.15 and CI=true. It fails on existing host oracle expectation differences
including xtrace quoting and null arithmetic; no fixtures or Classic behavior
are changed by this correction. Full raw outputs are retained in the handoff
`ticker-capture-030` evidence directory.
