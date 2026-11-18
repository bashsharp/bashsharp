#!/usr/bin/env bash
# Stage the existing immutable harness, explicit testee and verified fixtures.
set -euo pipefail
repo="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo"
[[ ${SUITE:-bash53} == bash53 ]] || { echo 'only the GNU Bash 5.3 suite is supported here' >&2; exit 2; }
[[ ${BASH53_ARTIFACT:-} == B ]] || { echo 'require BASH53_ARTIFACT=B' >&2; exit 2; }
testee=${BASH53_TESTEE_BIN:?explicit testee required}
[[ -f $testee && -x bin/bash53suite ]] || { echo 'build the shell and test tools first' >&2; exit 2; }
[[ -d external/bash-5.3/tests ]] || { echo 'run bashy dag test-bash-fixtures first' >&2; exit 2; }
arch=${ARCH:-amd64}
image=${IMAGE:?explicit conformance image name required}
base=${BASE_IMAGE:?locked official base image required}
oci=${OCI:-bashy oci}
context=$(mktemp -d)
trap 'rm -rf "$context"' EXIT
mkdir -p "$context/bin/bash-linux-$arch" "$context/bash53"
cp "$testee" "$context/bin/bash-linux-$arch/bash"
chmod 0755 "$context/bin/bash-linux-$arch/bash"
if [[ -f ${testee}.real ]]; then
  cp "${testee}.real" "$context/bin/bash-linux-$arch/bash.real"
  chmod 0755 "$context/bin/bash-linux-$arch/bash.real"
fi
cp bin/bash53suite "$context/bin/bash53suite-linux-$arch"
cp -R external/bash-5.3/. "$context/bash53/"
rm -f "$context/bash53/tests/recho" "$context/bash53/tests/zecho" "$context/bash53/tests/xcase" "$context/bash53/tests/printenv"
cp chunks.json "$context/chunks.json"
cp tools/bash53-container/Containerfile.k8s "$context/Containerfile"
$oci build --platform "linux/$arch" --build-arg "BASE_IMAGE=$base" -t "$image" -f "$context/Containerfile" "$context"
inventory=$($oci run --rm --network none --platform "linux/$arch" "$image" sigdfl "./bin/bash53suite-linux-$arch" -tests-dir /bash53/tests -list)
count=$(printf '%s\n' "$inventory" | tr -d '\r' | awk 'NF { n++ } END { print n+0 }')
[[ $count == 86 ]] || { echo "baked fixture inventory is $count, expected 86" >&2; exit 2; }
printf 'Baked fixture inventory: %s\n' "$count"
