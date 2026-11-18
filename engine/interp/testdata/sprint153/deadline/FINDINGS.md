# Sprint 153 Deadline Roots

Measurement-only spike for Story S153.6 / Story-ID 11fffa6b51be.

Commands used:

- Built the lane-local runner as `./bashy.real` from the adjacent bashy checkout with this workspace replacing `../sh`.
- Baseline before measurement: `go test -count=1 -timeout 30m -run 'GoSource' ./interp/...`.
- Native oracle per root: `/usr/bin/time -l go run <corpus-root>`.
- Interpreted per root: `/usr/bin/time -l timeout --kill-after=5s 70s ./bashy.real --bashpp --source=go --go-file <corpus-root>`.
- Survivors: exact post-pass scan for this workspace's `bashy.real --bashpp`; no survivors remained. A broad `pgrep -f bashy.real` was polluted by unrelated lane command lines and is not used below.

The GoSource baseline failed after 1567.503 s with these 8 pre-existing failures on darwin: `TestGoSourceTourCallbackReferenceBoundaries/aggregate_parameter`, `TestGoSourceRetainedCallbackPolicy`, `TestGoSourceImageRetainedConsumerBoundary`, `TestGoSourceNativeReadSliceBoundaries/callback_alias_observation`, `TestGoSourceNilAggregateReviewThreeModes/{func_channel_fields,func_channel_fields_aliases}`, `TestGoSourceReceiveRejectsUnrepresentedChannelStorage`, `TestGoSourceOriginalTCPScratchIntegrity/interpreted`, and `TestGoSourceUnwrapThreeModes/pointer_state_reentry`.

Factors are interpreted-time / 60 s. For rows that reached the 70 s cap, `>1.17x` is the measured lower bound; larger numbers are extrapolated from the scaled controls under this directory and are noted as estimates.

| root | native s | interpreted s | factor | class | peak RSS | survivors | note |
|---|---:|---:|---:|---|---:|---|---|
| `64bit.go` | 0.412 | 1.678 | n/a | OTHER-OWNER | 137461760 | no | Fails fast: `BASHPP-EASSIGN-UNDECLARED: RHS true is not declared`. |
| `abi/fibish.go` | 0.960 | >70 | ~22x | SLOW-CORRECT | 1638400 | no | Scaled sizes 20/25/30 match native; known calibration predicts fib(40) about 22x over 60 s. |
| `abi/fibish_closure.go` | 0.719 | >70 | ~24x | SLOW-CORRECT | 1638400 | no | Scaled closure-recursive sizes 20/25/30 match native and grow sharply by size 30. |
| `abi/uglyfib.go` | 1.307 | >70 | ~25x | SLOW-CORRECT | 1638400 | no | Scaled mutual-recursion sizes 20/25/30 match native and grow sharply by size 30. |
| `atomicload.go` | 0.485 | 8.719 | 0.15x | SLOW-CORRECT | 126943232 | no | Completes inside 60 s with exit 0. |
| `closure.go` | 0.628 | >70 | >1.17x | NONTERMINATING/SEMANTIC | 595968000 | no | Scaled closure controls match but do not reproduce growth; original hits cap in goroutine/closure runtime path. |
| `deferfin.go` | 0.749 | >70 | >1.17x | NONTERMINATING/SEMANTIC | 132169728 | no | Emits `bash++: task failed: exit status 1` before cap; finalizer-shaped scaled controls do not show linear growth. |
| `fixedbugs/issue11256.go` | 0.613 | 15.761 | 0.26x | SLOW-CORRECT | 1595031552 | no | Completes inside 60 s with exit 0. |
| `fixedbugs/issue16249.go` | 0.693 | >70 | >1.17x | SLOW-CORRECT | 1638400 | no | Scaled recursion-depth controls 50/100/200 match native; original is deeper nested recursion. |
| `fixedbugs/issue22781.go` | 0.662 | 10.521 | 0.18x | SLOW-CORRECT | 126582784 | no | Completes inside 60 s with exit 0. |
| `fixedbugs/issue24419.go` | 0.668 | 4.955 | 0.08x | SLOW-CORRECT | 1787904000 | no | Completes inside 60 s with exit 0. |
| `fixedbugs/issue27695.go` | 0.425 | >70 | n/a | OTHER-OWNER | 131399680 | no | Scaled controls fail fast: `gosource: original method M.Run is not supported by dependency transport`. |
| `fixedbugs/issue5493.go` | 0.808 | >70 | >1.17x | NONTERMINATING/SEMANTIC | 130334720 | no | Emits `bash++: task failed: exit status 1`; finalizer callback path does not scale as normal work. |
| `fixedbugs/issue5963.go` | 0.727 | >70 | >1.17x | NONTERMINATING/SEMANTIC | 127844352 | no | Timeout used almost no CPU; init `Goexit` / deferred send / `os.Exit` sequence does not complete. |
| `fixedbugs/issue79186.go` | 1.578 | >70 | ~256x | SLOW-CORRECT | 158531584 | no | Scaled operation counts 1024/8192/32768 match native and grow with work; extrapolating to 256 * 200000 ops is far beyond 60 s. |
| `fixedbugs/issue8039.go` | 1.070 | >70 | n/a | NONTERMINATING/SEMANTIC | 49889280 | no | Scaled sizes 1/2/3 all time out; deferred `copy(s, <-c)` form hangs independent of size. |
| `fixedbugs/issue67255.go` | 0.753 | 34.591 | 0.58x | SLOW-CORRECT | 87228416 | no | Completes inside 60 s with exit 0. |
| `ken/divconst.go` | 0.656 | >70 | >1.17x | SLOW-CORRECT | 125452288 | no | Scaled arithmetic controls match native; simplified controls are startup-dominated, so factor is a conservative lower bound. |
| `ken/modconst.go` | 0.418 | >70 | >1.17x | SLOW-CORRECT | 125878272 | no | Scaled arithmetic controls match native; simplified controls are startup-dominated, so factor is a conservative lower bound. |
| `peano.go` | 0.630 | >70 | ~3x | SLOW-CORRECT | 1638400 | no | Scaled Peano factorial sizes 5/6/7 match native and grow steeply; original max 9 extrapolates beyond 60 s. |
| `stack.go` | 0.285 | >70 | >1.17x | SLOW-CORRECT | 452640768 | no | Scaled recursion controls match native; original full stack/goroutine/defer stress exceeds cap. |
| `stackobj2.go` | 0.564 | 12.067 | 0.20x | SLOW-CORRECT | 204947456 | no | Completes inside 60 s with exit 0. |
| `rangegen.go` | 0.518 | 1.990 | n/a | OTHER-OWNER | 147636224 | no | Fails fast: `gosource: fmt.Fprintf requires a dependency-owned writer; original Write callbacks are unsupported`. Compiled mode was not changed. |

Scaled controls live beside this file, one directory per root and three or more `sizeN/main.go` variants for every original that timed out. They are deliberately outside-corpus programs that preserve the measured mechanism shape without editing or depending on the upstream Go corpus files.
