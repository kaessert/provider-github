#!/usr/bin/env bash
# test/lib/github-app.sh -- shared helpers for the scripts that create and remove
# the disposable GitHub objects the E2E run needs (test/org-secrets.sh,
# test/repo-fixtures.sh). Source it; it defines functions only and runs nothing.
#
# The caller sets ROOT (the provider directory) before sourcing and the three
# GitHub App credentials in the environment. API defaults to the public API.

API="${GITHUB_API_URL:-https://api.github.com}"

# fail prints a message naming the calling script and exits.
fail() {
  echo "${FIXTURE_SCRIPT:-github-app.sh}: ERROR: $*" >&2
  exit 1
}

# require_env fails unless every named variable is non-empty.
require_env() {
  local var
  for var in "$@"; do
    [ -n "${!var:-}" ] || fail "${var} is not set"
  done
}

# require_tools fails unless every named command is installed.
require_tools() {
  local tool
  for tool in "$@"; do
    command -v "${tool}" >/dev/null 2>&1 || fail "${tool} is required but is not installed"
  done
}

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

# gh_api <token> <method> <path> [curl-args...] -- prints the HTTP status on the
# last line and the response body before it.
gh_api() {
  local token="$1" method="$2" path="$3"
  shift 3
  curl -sS --max-time 30 -X "${method}" -w '\n%{http_code}' \
    -H "Accept: application/vnd.github+json" -H "Authorization: Bearer ${token}" \
    "$@" "${API}${path}"
}

# resp_status and resp_body split a gh_api response.
resp_status() { printf '%s' "${1##*$'\n'}"; }
resp_body() { printf '%s' "${1%$'\n'*}"; }

# watch_cluster_gone blocks until the Kubernetes cluster named by the kubeconfig
# file in WATCH_KUBECONFIG is gone (the net for a run that ends without reaching
# the teardown script). The probe reads a private kubeconfig that the caller
# wrote before it detached: the test runner deletes the one it hands the setup
# step as soon as that step returns, and the ambient fallback can be a different
# cluster. It also pins the cluster by the UID of its kube-system namespace, so a
# cluster that later reuses the address does not count. The kubeconfig file is
# removed when the caller exits.
watch_cluster_gone() {
  local kube="${KUBECTL:-kubectl}" uid misses deadline
  [ -s "${WATCH_KUBECONFIG:-}" ] || fail "watch: WATCH_KUBECONFIG must name a readable kubeconfig"
  trap 'rm -f "${WATCH_KUBECONFIG}"' EXIT
  probe() { "${kube}" --kubeconfig "${WATCH_KUBECONFIG}" --request-timeout=10s get namespace kube-system -o 'jsonpath={.metadata.uid}' 2>/dev/null || true; }
  uid="$(probe)"
  [ -n "${uid}" ] || fail "watch: the cluster does not answer, so it cannot be watched"
  echo "${FIXTURE_SCRIPT}: watching the cluster whose kube-system namespace is ${uid}"
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
  echo "${FIXTURE_SCRIPT}: cluster gone (or deadline reached); removing the disposable objects"
}
