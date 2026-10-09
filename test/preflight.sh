#!/usr/bin/env bash
# test/preflight.sh -- credential and test-organization checks for e2e-preflight.
#
# Fails in seconds, before any cluster or image exists, when the GitHub App
# credentials are missing or unusable. It mints an installation token and reads
# the test organization, so an expired key or an App that is no longer installed
# on the organization is reported here and not 30 minutes into an uptest run.
#
# Required environment:
#   PROVIDER_GITHUB_APP_ID               GitHub App ID
#   PROVIDER_GITHUB_APP_INSTALLATION_ID  installation ID of the App on the test org
#   PROVIDER_GITHUB_APP_PRIVATE_KEY_B64  the App's PEM private key, base64-encoded
#   E2E_ORG                              the test organization login
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

for var in PROVIDER_GITHUB_APP_ID PROVIDER_GITHUB_APP_INSTALLATION_ID PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 E2E_ORG; do
  [ -n "${!var:-}" ] || fail "${var} is not set. Set it as an env var or in ../.env"
done
for tool in curl openssl jq base64; do
  command -v "${tool}" >/dev/null 2>&1 || fail "${tool} is required by the e2e credential check but is not installed"
done

# The examples name the organization literally, so a different E2E_ORG would
# silently exercise another organization than the one the credentials were
# checked against.
stray="$(grep -rhE '^[[:space:]]+org:[[:space:]]' "${ROOT}/examples" --include='*.yaml' \
  | sed -E 's/^[[:space:]]+org:[[:space:]]*//; s/[[:space:]]+$//' | sort -u | grep -vxF "${E2E_ORG}" || true)"
[ -z "${stray}" ] || fail "examples name organization(s) other than E2E_ORG=${E2E_ORG}: $(echo "${stray}" | tr '\n' ' ')"

keyfile="$(mktemp)"
trap 'rm -f "${keyfile}"' EXIT
chmod 600 "${keyfile}"
printf '%s' "${PROVIDER_GITHUB_APP_PRIVATE_KEY_B64}" | base64 -d > "${keyfile}" 2>/dev/null \
  || fail "PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 is not valid base64"
openssl pkey -in "${keyfile}" -noout 2>/dev/null \
  || fail "PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 does not decode to a PEM private key"

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
now="$(date +%s)"
header="$(printf '{"alg":"RS256","typ":"JWT"}' | b64url)"
payload="$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' "$((now - 60))" "$((now + 540))" "${PROVIDER_GITHUB_APP_ID}" | b64url)"
signature="$(printf '%s.%s' "${header}" "${payload}" | openssl dgst -sha256 -sign "${keyfile}" | b64url)"
jwt="${header}.${payload}.${signature}"

api() {
  curl -sS --max-time 30 -H "Accept: application/vnd.github+json" -H "Authorization: Bearer $1" "$2"
}

token="$(curl -sS --max-time 30 -X POST -H "Accept: application/vnd.github+json" -H "Authorization: Bearer ${jwt}" \
  "https://api.github.com/app/installations/${PROVIDER_GITHUB_APP_INSTALLATION_ID}/access_tokens" | jq -r '.token // empty')"
[ -n "${token}" ] || fail "could not mint an installation token for App ${PROVIDER_GITHUB_APP_ID}, installation ${PROVIDER_GITHUB_APP_INSTALLATION_ID} (wrong ID, revoked key or uninstalled App)"

login="$(api "${token}" "https://api.github.com/orgs/${E2E_ORG}" | jq -r '.login // empty')"
[ "${login}" = "${E2E_ORG}" ] || fail "the installation cannot read organization ${E2E_ORG}"

echo "e2e-preflight: GitHub App credentials OK (installation reads organization ${E2E_ORG})"
