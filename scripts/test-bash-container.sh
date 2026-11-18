#!/usr/bin/env bash
# Run the complete GNU Bash 5.3 fixture gate from a self-contained OCI image.
# No checkout or fixture mount is visible to the runtime container.
set -euo pipefail

# This is a harness request, not a shell invocation selector. In particular,
# an env-bash shebang may consume BASHY_BASHPP before this script starts.
# Require an explicit request so a lost channel cannot become an OFF green.
case "${BASH53_BASHPP:-}" in
  0|1) ;;
  *) echo 'test-bash-container: require BASH53_BASHPP=0 or 1 (use the Makefile targets)' >&2; exit 2 ;;
esac

ARTIFACT="${BASH53_ARTIFACT:-}"
case "$ARTIFACT" in
  ""|[aA]|[bB]|[cC]) ;;
  *)
    echo "test-bash-container: invalid BASH53_ARTIFACT: $BASH53_ARTIFACT (want A, B, or C)" >&2
    exit 2
    ;;
esac
case "$ARTIFACT" in
  [aA]) ARTIFACT="A" ;;
  [bB]) ARTIFACT="B" ;;
  [cC]) ARTIFACT="C" ;;
esac

TESTEE_BIN="${BASH53_TESTEE_BIN:-}"
if [ -n "$ARTIFACT" ]; then
  if [ "$ARTIFACT" = "A" ] && [ "$BASH53_BASHPP" = "1" ]; then
    echo "test-bash-container: artifact A does not support BASH53_BASHPP=1" >&2
    exit 2
  fi
  if [ -z "$TESTEE_BIN" ]; then
    echo "test-bash-container: explicit BASH53_TESTEE_BIN required when BASH53_ARTIFACT is set" >&2
    exit 2
  fi
  if [ ! -f "$TESTEE_BIN" ]; then
    echo "test-bash-container: testee binary not found: $TESTEE_BIN" >&2
    exit 2
  fi
fi

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"
# shellcheck source=scripts/test-lane-id.sh
. "$REPO/scripts/test-lane-id.sh"
LANE="$(bashy_test_lane "$REPO")" || { echo 'test-bash-container: unsafe BASHY_TEST_LANE' >&2; exit 2; }

GO_CMD=${GO:-}
if [ -z "$GO_CMD" ]; then
  if command -v go >/dev/null 2>&1; then GO_CMD=go
  elif command -v bashy >/dev/null 2>&1; then GO_CMD="$(command -v bashy) go"
  elif [ -x "$HOME/.local/bin/bashy" ]; then GO_CMD="$HOME/.local/bin/bashy go"
  else echo 'test-bash-container: need go or bashy go to build the baked testee' >&2; exit 2
  fi
fi
read -r -a go_cmd <<<"$GO_CMD"

ARCH="${BASH53_ARCH:-$("${go_cmd[@]}" env GOARCH)}"
image_lane_tag="$LANE"
if [ -n "$ARTIFACT" ]; then
  artifact_lower="$(printf '%s' "$ARTIFACT" | tr '[:upper:]' '[:lower:]')"
  image_lane_tag="$LANE-$artifact_lower"
fi
IMAGE="${BASH53_IMAGE:-localhost/bashsharp-bash53:$image_lane_tag-$ARCH}"
OCI="${BASH53_OCI:-bashy podman}"
CONTAINER="bashsharp-bash53-$LANE"
BASE_IMAGE="${BASH53_BASE_IMAGE:-docker.io/library/ubuntu@sha256:33ceb71981b602c1a7443a53469e4dba065f7503eab3078a2d7a57a2ab987517}"

# On macOS, use Bashy's private Podman Machine API socket directly. This keeps
# the hermetic lane independent of unrelated or malformed entries in the
# operator's ~/.ssh/known_hosts while retaining the private-machine boundary.
if [ -z "${CONTAINER_HOST:-}" ] && [ "$(uname -s)" = Darwin ]; then
  machine_socket="$({ $OCI machine inspect bashy 2>/dev/null | sed -n 's/^[[:space:]]*"Path": "\([^"]*api\.sock\)".*/\1/p' | head -1; } || true)"
  if [ -n "$machine_socket" ] && [ -S "$machine_socket" ]; then
    export CONTAINER_HOST="unix://$machine_socket"
  fi
fi

case "$ARCH" in
  amd64|arm64) ;;
  *)
    echo "test-bash-container: unsupported Linux container architecture: $ARCH" >&2
    exit 2
    ;;
esac

echo ">> building self-contained Bash 5.3 conformance image $IMAGE (linux/$ARCH)" >&2
$OCI info >/dev/null
OCI="$OCI" GO="$GO_CMD" SUITE=bash53 ARCH="$ARCH" IMAGE="$IMAGE" BASE_IMAGE="$BASE_IMAGE" SELFCHECK=inventory \
  BASH53_ARTIFACT="$ARTIFACT" \
  BASH53_TESTEE_BIN="$TESTEE_BIN" \
  "${BASHY:-bashy}" scripts/build-conformance-image.sh

echo ">> running all 86 fixtures in the hermetic image" >&2
if [ -n "$ARTIFACT" ]; then
  artifact_lower="$(printf '%s' "$ARTIFACT" | tr '[:upper:]' '[:lower:]')"
  evidence="${BASH53_EVIDENCE:-bin/bash53-evidence-$artifact_lower-$BASH53_BASHPP.json}"
else
  evidence="${BASH53_EVIDENCE:-bin/bash53-evidence-$BASH53_BASHPP.json}"
fi
mkdir -p "$(dirname "$evidence")"
suite_json="$(mktemp)"
trap 'rm -f "$suite_json"' EXIT
image_id="$($OCI image inspect --format '{{.Id}}' "$IMAGE")"
host_os="$(uname -s)"

container_env=(
  -e BASH53_ARCH="$ARCH"
  -e BASH53_RUNNER="hermetic-$LANE-$ARCH"
  -e BASH53_TIMEOUT="${BASH53_TIMEOUT:-60s}"
  -e BASH53_JOBS_TIMEOUT="${BASH53_JOBS_TIMEOUT:-120s}"
  -e BASH53_MEM_KB="${BASH53_MEM_KB:-4194304}"
  -e BASH53_BASHPP="$BASH53_BASHPP"
)
if [ -n "$ARTIFACT" ]; then
  container_env+=(-e BASH53_ARTIFACT="$ARTIFACT")
fi

suite_args=(
  -tests-dir /bash53/tests
  -bash "./bin/bash-linux-$ARCH/bash"
  -bashpp-mode "$BASH53_BASHPP"
)
if [ -n "$ARTIFACT" ]; then
  suite_args+=(-artifact "$ARTIFACT")
  if [ "$ARTIFACT" = "A" ]; then
    suite_args+=(-classic-artifact)
  fi
fi
suite_args+=(-tests "${TESTS:-}" -json)

# --tty supplies a controlling terminal for read/test/vredir. A fresh tmpfs
# prevents fixed upstream names such as /tmp/bash from colliding with host or
# prior-run state. The baked fixture tree and binaries remain immutable.
set +e
$OCI run --rm --platform "linux/$ARCH" \
  --name "$CONTAINER" --label "io.dhnt.test.lane=$LANE" \
  --cpus "${BASHY_TEST_CPUS:-2}" --memory "${BASHY_TEST_MEMORY:-6g}" --pids-limit 4096 \
  --user 1000:1000 \
  --tty \
  --network none \
  --read-only \
  --tmpfs /tmp:rw,exec,mode=1777 \
  --tmpfs /var/tmp:rw,exec,mode=1777 \
  "${container_env[@]}" \
  "$IMAGE" \
  sigdfl "./bin/bash53suite-linux-$ARCH" \
    "${suite_args[@]}" >"$suite_json"
runner_status=$?
set -e
cat "$suite_json"
# --tty merges the runner's stderr log into stdout and turns LF into CRLF, so
# the capture holds log lines around the one JSON report line; keep only it.
report_json="$(mktemp)"
trap 'rm -f "$suite_json" "$report_json"' EXIT
tr -d '\r' <"$suite_json" | grep '^{' | tail -n 1 >"$report_json" || true

compute_sha256() {
  local target="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$target" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$target" | awk '{print $1}'
  else
    echo "test-bash-container: missing sha256sum and shasum" >&2
    exit 2
  fi
}

if [ -z "$ARTIFACT" ]; then
  commit="$(git rev-parse HEAD)"
  sh_pin="$(awk '$1 == "replace" && $2 == "mvdan.cc/sh/v3" { print $5; exit }' go.mod)"
  [ -n "$sh_pin" ] || { echo 'test-bash-container: missing sh pin' >&2; exit 2; }
  evidence_args=(-input "$report_json" -output "$evidence" -bashy-commit "$commit" \
    -sh-pin "$sh_pin" -image-id "$image_id" -mode "$BASH53_BASHPP" \
    -uid 1000 -host-os "$host_os")
else
  testee_sha256="$(compute_sha256 "$TESTEE_BIN")"
  payload_sha256=""
  if [ -f "${TESTEE_BIN}.real" ]; then
    payload_sha256="$(compute_sha256 "${TESTEE_BIN}.real")"
  fi
  source_commit="${BASH53_SOURCE_COMMIT:-}"
  lower_pins="${BASH53_LOWER_PINS:-}"

  evidence_args=(
    -input "$report_json"
    -output "$evidence"
    -artifact "$ARTIFACT"
    -image-id "$image_id"
    -mode "$BASH53_BASHPP"
    -uid 1000
    -host-os "$host_os"
    -testee-sha256 "$testee_sha256"
  )
  if [ -n "$payload_sha256" ]; then
    evidence_args+=(-payload-sha256 "$payload_sha256")
  fi
  if [ -n "$source_commit" ]; then
    evidence_args+=(-source-commit "$source_commit")
  fi
  if [ -n "$lower_pins" ]; then
    evidence_args+=(-lower-pins "$lower_pins")
  fi
fi

if [ -n "${BASH53_BASELINE:-}" ]; then evidence_args+=(-baseline "$BASH53_BASELINE"); fi
if [ "${BASH53_GATE:-0}" = 1 ]; then evidence_args+=(-gate); fi
# The evidence writer reads this host's files: run it with HOST_GO, since GO
# may build in a container (tools/bashy-build/go.sh).
read -r -a host_go_cmd <<<"${HOST_GO:-$GO_CMD}"
"$REPO/bin/bash53evidence" "${evidence_args[@]}"
exit "$runner_status"
