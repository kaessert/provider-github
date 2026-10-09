#!/usr/bin/env bash
# preflight_test.sh -- offline tests for test/preflight.sh.
#
# Covers the three ways the credential check fails before it ever reaches the
# GitHub API: a missing variable, an examples/ tree that names another
# organization than E2E_ORG, and a private key that is not a PEM. None of them
# needs network access.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/test/preflight.sh"
failures=0

check() {
  local name="$1" want="$2" out rc
  shift 2
  out="$("$@" 2>&1)"
  rc=$?
  if [ "${rc}" -eq 0 ] || ! grep -qF -- "${want}" <<<"${out}"; then
    echo "FAIL: ${name}: rc=${rc}, want non-zero with '${want}'; got: ${out}"
    failures=$((failures + 1))
  else
    echo "ok: ${name}"
  fi
}

baseenv=(PROVIDER_GITHUB_APP_ID=1 PROVIDER_GITHUB_APP_INSTALLATION_ID=2
  "PROVIDER_GITHUB_APP_PRIVATE_KEY_B64=$(printf 'not a key' | base64 | tr -d '\n')"
  E2E_ORG=pgh-test)

check "missing app id fails" "PROVIDER_GITHUB_APP_ID is not set" \
  env -u PROVIDER_GITHUB_APP_ID "${baseenv[@]:1}" "${SCRIPT}"

check "missing org fails" "E2E_ORG is not set" \
  env -u E2E_ORG "${baseenv[@]:0:3}" "${SCRIPT}"

check "org other than the examples fails" "other than E2E_ORG" \
  env "${baseenv[@]}" E2E_ORG=some-other-org "${SCRIPT}"

check "non-PEM key fails" "does not decode to a PEM private key" \
  env "${baseenv[@]}" "${SCRIPT}"

[ "${failures}" -eq 0 ]
