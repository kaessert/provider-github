#!/usr/bin/env bash
# test/org-secrets.sh -- disposable organization secrets for the SecretAccess E2E.
#
# ActionsSecretAccess and DependabotSecretAccess manage which repositories can
# use an organization secret; the provider never creates the secret. GitHub
# accepts a secret only as a libsodium sealed box of its value, so this script
# asks test/sealedbox (a standard-library Go helper, no module beyond the
# provider's own) to encrypt a random value and PUTs it through the REST API
# with an installation token minted from the three GitHub App credentials.
#
# Usage:
#   test/org-secrets.sh create   create (or overwrite) both secrets, visibility selected,
#                                with the repository SELECTED_REPO as the initial selection
#   test/org-secrets.sh delete   delete both secrets; a missing secret is fine
#   test/org-secrets.sh watch    block until the Kubernetes cluster named by the
#                                kubeconfig file in WATCH_KUBECONFIG is gone, then
#                                delete both secrets (the net for a run that ends
#                                without reaching the teardown script)
#
# Required environment:
#   PROVIDER_GITHUB_APP_ID               GitHub App ID
#   PROVIDER_GITHUB_APP_INSTALLATION_ID  installation ID of the App on the test org
#   PROVIDER_GITHUB_APP_PRIVATE_KEY_B64  the App's PEM private key, base64-encoded
#   E2E_ORG                              the test organization login
#
# Optional environment:
#   SELECTED_REPO     existing repository of E2E_ORG the secrets are initially
#                     shared with (default: demo-repository-1)
#   KUBECTL           path to the kubectl binary (watch only; default: kubectl)
#   WATCH_KUBECONFIG  kubeconfig file for the cluster to watch (watch only; removed on exit)
#   GITHUB_API_URL    API base (default: https://api.github.com)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API="${GITHUB_API_URL:-https://api.github.com}"

# Both secrets are created with visibility selected: the SecretAccess controllers
# enforce (and write) the repository list only while the visibility is selected,
# and the provider cannot change the visibility itself.
SELECTED_REPO="${SELECTED_REPO:-demo-repository-1}"

# The names are the external names the secret-access examples carry. Nothing
# else in the organization is ever touched.
ACTIONS_SECRET=PGH_E2E_ACTIONS_SECRET
DEPENDABOT_SECRET=PGH_E2E_DEPENDABOT_SECRET

fail() {
  echo "org-secrets.sh: ERROR: $*" >&2
  exit 1
}

for var in PROVIDER_GITHUB_APP_ID PROVIDER_GITHUB_APP_INSTALLATION_ID PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 E2E_ORG; do
  [ -n "${!var:-}" ] || fail "${var} is not set"
done
for tool in curl openssl jq base64; do
  command -v "${tool}" >/dev/null 2>&1 || fail "${tool} is required but is not installed"
done

# mint_token prints a fresh installation token. Called once per operation: a
# watcher can outlive the one-hour lifetime of a token minted at create time.
mint_token() {
  local keyfile now header payload signature jwt token
  keyfile="$(mktemp)"
  chmod 600 "${keyfile}"
  printf '%s' "${PROVIDER_GITHUB_APP_PRIVATE_KEY_B64}" | base64 -d > "${keyfile}" 2>/dev/null \
    || { rm -f "${keyfile}"; fail "PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 is not valid base64"; }
  b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
  now="$(date +%s)"
  header="$(printf '{"alg":"RS256","typ":"JWT"}' | b64url)"
  payload="$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' "$((now - 60))" "$((now + 540))" "${PROVIDER_GITHUB_APP_ID}" | b64url)"
  signature="$(printf '%s.%s' "${header}" "${payload}" | openssl dgst -sha256 -sign "${keyfile}" | b64url)" \
    || { rm -f "${keyfile}"; fail "could not sign the App JWT"; }
  rm -f "${keyfile}"
  jwt="${header}.${payload}.${signature}"
  token="$(curl -sS --max-time 30 -X POST -H "Accept: application/vnd.github+json" -H "Authorization: Bearer ${jwt}" \
    "${API}/app/installations/${PROVIDER_GITHUB_APP_INSTALLATION_ID}/access_tokens" | jq -r '.token // empty')"
  [ -n "${token}" ] || fail "could not mint an installation token"
  printf '%s' "${token}"
}

# gh_api <token> <method> <path> [json-body-on-stdin] -- prints the HTTP status
# on the last line and the response body before it.
gh_api() {
  local token="$1" method="$2" path="$3"
  shift 3
  curl -sS --max-time 30 -X "${method}" -w '\n%{http_code}' \
    -H "Accept: application/vnd.github+json" -H "Authorization: Bearer ${token}" \
    "$@" "${API}${path}"
}

create_secret() {
  local token="$1" kind="$2" name="$3" resp status body key_id key sealed repo_id
  resp="$(gh_api "${token}" GET "/repos/${E2E_ORG}/${SELECTED_REPO}")"
  status="${resp##*$'\n'}"
  body="${resp%$'\n'*}"
  [ "${status}" = 200 ] || fail "reading repository ${E2E_ORG}/${SELECTED_REPO} returned HTTP ${status}: ${body}"
  repo_id="$(printf '%s' "${body}" | jq -r '.id')"
  resp="$(gh_api "${token}" GET "/orgs/${E2E_ORG}/${kind}/secrets/public-key")"
  status="${resp##*$'\n'}"
  body="${resp%$'\n'*}"
  [ "${status}" = 200 ] || fail "reading the ${kind} public key of ${E2E_ORG} returned HTTP ${status}: ${body}"
  key_id="$(printf '%s' "${body}" | jq -r '.key_id')"
  key="$(printf '%s' "${body}" | jq -r '.key')"
  sealed="$(openssl rand -hex 16 | tr -d '\n' | go -C "${ROOT}" run ./test/sealedbox "${key}")" \
    || fail "could not encrypt the ${kind} secret value"
  resp="$(jq -n --arg v "${sealed}" --arg k "${key_id}" --argjson r "${repo_id}" '{encrypted_value: $v, key_id: $k, visibility: "selected", selected_repository_ids: [$r]}' \
    | gh_api "${token}" PUT "/orgs/${E2E_ORG}/${kind}/secrets/${name}" -H "Content-Type: application/json" --data @-)"
  status="${resp##*$'\n'}"
  case "${status}" in
    201|204) echo "org-secrets.sh: ${kind} secret ${name} ready in ${E2E_ORG} (HTTP ${status})" ;;
    *) fail "creating ${kind} secret ${name} returned HTTP ${status}: ${resp%$'\n'*}" ;;
  esac
}

delete_secret() {
  local token="$1" kind="$2" name="$3" resp status
  resp="$(gh_api "${token}" DELETE "/orgs/${E2E_ORG}/${kind}/secrets/${name}")"
  status="${resp##*$'\n'}"
  case "${status}" in
    204) echo "org-secrets.sh: ${kind} secret ${name} deleted from ${E2E_ORG}" ;;
    404) echo "org-secrets.sh: ${kind} secret ${name} already absent from ${E2E_ORG}" ;;
    *) echo "org-secrets.sh: ERROR: deleting ${kind} secret ${name} returned HTTP ${status}: ${resp%$'\n'*}" >&2; return 1 ;;
  esac
}

delete_both() {
  local token rc=0
  token="$(mint_token)"
  delete_secret "${token}" actions "${ACTIONS_SECRET}" || rc=1
  delete_secret "${token}" dependabot "${DEPENDABOT_SECRET}" || rc=1
  return "${rc}"
}

case "${1:-}" in
  create)
    token="$(mint_token)"
    create_secret "${token}" actions "${ACTIONS_SECRET}"
    create_secret "${token}" dependabot "${DEPENDABOT_SECRET}"
    ;;
  delete)
    delete_both
    ;;
  watch)
    KUBECTL="${KUBECTL:-kubectl}"
    # The probe reads a private kubeconfig that the caller wrote before it
    # detached (WATCH_KUBECONFIG): the test runner deletes the one it hands the
    # setup step as soon as that step returns, and the ambient fallback can be a
    # different cluster. It also pins the cluster by the UID of its kube-system
    # namespace, so a cluster that later reuses the address does not count.
    [ -s "${WATCH_KUBECONFIG:-}" ] || fail "watch: WATCH_KUBECONFIG must name a readable kubeconfig"
    trap 'rm -f "${WATCH_KUBECONFIG}"' EXIT
    probe() { "${KUBECTL}" --kubeconfig "${WATCH_KUBECONFIG}" --request-timeout=10s get namespace kube-system -o 'jsonpath={.metadata.uid}' 2>/dev/null || true; }
    uid="$(probe)"
    [ -n "${uid}" ] || fail "watch: the cluster does not answer, so it cannot be watched"
    echo "org-secrets.sh: watching the cluster whose kube-system namespace is ${uid}"
    # Six consecutive probes that do not return this cluster's UID, ten seconds
    # apart, mean the cluster is gone rather than briefly unreachable. The
    # deadline bounds a watcher whose cluster outlives any plausible run.
    misses=0
    deadline=$(( $(date +%s) + 21600 ))
    while [ "${misses}" -lt 6 ] && [ "$(date +%s)" -lt "${deadline}" ]; do
      if [ "$(probe)" = "${uid}" ]; then
        misses=0
      else
        misses=$((misses + 1))
      fi
      sleep 10
    done
    echo "org-secrets.sh: cluster gone (or deadline reached); removing the disposable secrets"
    delete_both
    ;;
  *)
    echo "usage: ${0##*/} create|delete|watch" >&2
    exit 2
    ;;
esac
