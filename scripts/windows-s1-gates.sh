#!/usr/bin/env bash
# Run with a Windows-built bashy.exe; ROOT contains current sibling snapshots.
# Each command keeps its own exit status and raw log. Zero-test JSON logs
# must be treated as coverage gaps, never as passing test invocations.
set -u
ROOT=${1:?usage: bashy.exe windows-s1-gates.sh ROOT}
cd "$ROOT" || exit 2
ROOT=$PWD
# Environment values passed to Go tests must also be native Windows paths.
if [[ "$ROOT" == /[a-zA-Z]/* ]]; then
    ROOT="${ROOT:1:1}:/${ROOT:3}"
fi
mkdir -p evidence installed
LOGS=$ROOT/evidence
export GOMAXPROCS=4 BASHY_HINTS=off
export BASHY_BIN=$ROOT/installed/bashy.exe
export BASH_ENGINE_BIN=$ROOT/bashy/bin/bash.exe
export BASHY=$BASHY_BIN
export BASHSHARP_CORPUS=$ROOT/bashsharp-tests/tests/bashsharp
failed=0
run() {
    name=$1 repo=$2
    shift 2
    cd "$ROOT/$repo" || return 2
    printf '%s\tSTART\t' "$name" >> "$LOGS/status.tsv"
    printf '%q ' "$@" >> "$LOGS/status.tsv"
    printf '\n' >> "$LOGS/status.tsv"
    "$@" > "$LOGS/$name.log" 2>&1
    rc=$?
    printf '%s\t%s\n' "$name" "$rc" >> "$LOGS/status.tsv"
    printf '%s exit=%s\n' "$name" "$rc"
    if [ "$rc" -ne 0 ]; then failed=1; fi
    if [ "${1:-}" = go ] && [ "${2:-}" = test ]; then
        passed=0 test_failed=0 skipped=0
        while IFS= read -r line; do
            case "$line" in
                *'"Test":'*)
                    case "$line" in
                        *'"Action":"pass"'*) passed=$((passed + 1)) ;;
                        *'"Action":"fail"'*) test_failed=$((test_failed + 1)) ;;
                        *'"Action":"skip"'*) skipped=$((skipped + 1)) ;;
                    esac ;;
            esac
        done < "$LOGS/$name.log"
        printf '%s\twindows\t%s\t%s\t%s\n' "$name" "$passed" "$test_failed" "$skipped" >> "$LOGS/test-counts.tsv"
        if [ "$((passed + test_failed))" -eq 0 ]; then
            printf '%s\tGAP: no completed tests (skip-only is not execution credit)\n' "$name" >> "$LOGS/status.tsv"
            failed=1
        fi
    fi
}
run toolchain bashy go version
run shell-build bashy go build -o bin/bash.exe ./cmd/bash
run product-install bashy go build -o "$BASHY_BIN" ./cmd/bashy
run sharp-build bashsharp go build ./...
run sharp-vet bashsharp go vet ./...
run sharp-all bashsharp go test -json ./... -count=1 -timeout=10m
run syntax sh go test -json ./syntax -run TestBashPP -count=1 -timeout=5m
run runtime sh go test -json -tags full ./interp ./lower -run 'TestBashPP|TestSharp|Test.*Fence|Test.*Contract|TestGoSourceCaptureClassicBashPPDeepCopy|TestGoSourceNativeFunctionThreeModes|TestCompiledCaptureAcrossSharedBackendReset' -count=1 -timeout=15m
run product bashy go test -json ./internal/agentos -run 'TestContract|TestDagDispatchBashpp|TestManifestRowsRegistered|TestTranspileRegisteredCLIDispatch|TestPosixWireExecCarriesNoDecoratorWiring' -count=1 -timeout=10m
BASHY=$BASH_ENGINE_BIN run posix bashsharp-tests "$BASHY_BIN" tools/startsites/classify.sh --posix-gate
run validate bashsharp-tests "$BASHY_BIN" tools/bashsharp/validate.sh
run tamper bashsharp-tests "$BASHY_BIN" tools/bashsharp/tamper-tests.sh
run acceptance bashsharp-tests "$BASHY_BIN" tools/bashsharp/acceptance.sh
run sharp-lowering bashsharp-tests "$BASHY_BIN" tools/bashsharp/lowering.sh
run lower-validate bashsharp-tests "$BASHY_BIN" tools/lowering/validate.sh
run lower-differential bashsharp-tests ruby tools/lowering/differential.rb --go go --artifacts "$LOGS/differential"
run decorators bashsharp-tests ruby tools/decorators/acceptance.rb --require-supported
run agentic bashsharp-tests ruby tools/agentic/acceptance.rb
run polyglot bashsharp-tests "$BASHY_BIN" tools/polyglot-gate.sh
run opencode bashsharp-tests "$BASHY_BIN" tools/opencode-fixture.sh "$ROOT/bashsharp-tests/tests/typescript-workspaces/opencode.bpp"
run python-package bashsharp-tests "$BASHY_BIN" tools/python-package-gate.sh
for language in python typescript rust c go text; do
    run "dag-$language" bashy "$BASHY_BIN" "scripts/dag-$language-examples-smoke.sh"
done
run manifests bashy "$BASHY_BIN" scripts/manifest-examples-smoke.sh
printf 'DONE\n' >> "$LOGS/status.tsv"
# Test-event counts include subtests. Inspect all raw logs for named gaps.
exit "$failed"
