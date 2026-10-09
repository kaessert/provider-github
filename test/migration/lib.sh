#!/usr/bin/env bash
# test/migration/lib.sh -- shared helpers for the v0.22.0 -> candidate migration
# validation harness. Sourced by every other script in this directory; it is
# not meant to be executed.
#
# This harness is NOT part of the end-to-end suite. Nothing in the Makefile's
# e2e targets, the example manifests or the CI workflows reaches it; it runs
# only through `make validate.migration` (see README.md).
#
# Environment contract (every name is checked up front and the first missing one
# is reported by name):
#
#   PROVIDER_GITHUB_APP_ID               GitHub App ID
#   PROVIDER_GITHUB_APP_INSTALLATION_ID  installation ID of the App on the test org
#   PROVIDER_GITHUB_APP_PRIVATE_KEY_B64  the App's PEM private key, base64-encoded
#
# Optional (defaults in brackets):
#
#   MIGRATION_ORG              test organization login [pgh-test]
#   MIGRATION_MEMBER           existing org member the fixtures use [first member found]
#   MIGRATION_FORK_SOURCE      public owner/repo the fork fixture forks [actions/hello-world-docker-action]
#   MIGRATION_WORKDIR          evidence + scratch directory [${TMPDIR:-/tmp}/provider-github-migration]
#   MIGRATION_POLL             provider --poll interval [60s]
#   MIGRATION_SETTLE_POLLS     poll cycles the candidate must run before the
#                              post-upgrade snapshot is taken [4]
#   MIGRATION_MIN_RATE_BUDGET  GitHub requests that must be left in the hour for
#                              the adoption scenario to start, 0 = no check [3500]
#   MIGRATION_READY_TIMEOUT    seconds to wait for fixtures to become Ready [1500]
#   MIGRATION_BASELINE_REPO    where the baseline tag is fetched from
#   MIGRATION_BASELINE_REF     baseline tag [v0.22.0]
#   MIGRATION_BASELINE_DIR     an existing checkout of the baseline tag (skips the fetch)
#   MIGRATION_FIXTURES_DIR     fixture directory under test [fixtures/ next to this file]
#   MIGRATION_CANDIDATE_DIR    the candidate tree [the provider root containing this directory]
#   MIGRATION_REQUIRE_BASELINE_READY  1 = fail when a fixture is not Ready on the baseline [0]
#   MIGRATION_KEEP_V1_FLAGS    1 = keep the baseline runtime flags across the upgrade [0]
#   MIGRATION_API_URL          GitHub API base [https://api.github.com]
#   MIGRATION_GITHUB_TOKEN     a ready installation token (self-test only; skips minting)
#
# The cluster is never created under a name this harness chose: the cluster
# scenarios require KIND_CLUSTER_NAME and KUBECONFIG from the launcher that owns
# the cluster lifecycle, and refuse to run without them.

# shellcheck disable=SC2034  # the sourcing scripts use these
MIGRATION_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROVIDER_ROOT="$(cd "${MIGRATION_DIR}/../.." && pwd)"
FIXTURES_DIR="${MIGRATION_FIXTURES_DIR:-${MIGRATION_DIR}/fixtures}"

MIGRATION_ORG="${MIGRATION_ORG:-pgh-test}"
MIGRATION_API_URL="${MIGRATION_API_URL:-https://api.github.com}"
MIGRATION_FORK_SOURCE="${MIGRATION_FORK_SOURCE:-actions/hello-world-docker-action}"
MIGRATION_WORKDIR="${MIGRATION_WORKDIR:-${TMPDIR:-/tmp}/provider-github-migration}"
MIGRATION_POLL="${MIGRATION_POLL:-60s}"
MIGRATION_SETTLE_POLLS="${MIGRATION_SETTLE_POLLS:-4}"
MIGRATION_READY_TIMEOUT="${MIGRATION_READY_TIMEOUT:-1500}"
MIGRATION_BASELINE_REPO="${MIGRATION_BASELINE_REPO:-https://github.com/provider-github/provider-github.git}"
MIGRATION_BASELINE_REF="${MIGRATION_BASELINE_REF:-v0.22.0}"
MIGRATION_CANDIDATE_DIR="${MIGRATION_CANDIDATE_DIR:-${PROVIDER_ROOT}}"
MIGRATION_REQUIRE_BASELINE_READY="${MIGRATION_REQUIRE_BASELINE_READY:-0}"
MIGRATION_KEEP_V1_FLAGS="${MIGRATION_KEEP_V1_FLAGS:-0}"

# Every GitHub object the harness creates carries one of these two prefixes (the
# lower-case one for repositories, teams, runner groups and webhook URLs, the
# upper-case one for variables and secrets). The snapshot and the cleanup sweep
# key on them and on nothing else, so objects of other tests are never read
# into a comparison and never deleted.
PREFIX_LOWER="pgh-mig-"
PREFIX_UPPER="PGH_MIG_"
HOOK_URL_PREFIX="https://example.com/pgh-mig"
TEMPLATE_REPO="pgh-mig-template"

KUBECTL_BIN="${KUBECTL:-kubectl}"

# ---------------------------------------------------------------------------
# Logging and results
# ---------------------------------------------------------------------------

log() { printf '%s [migration] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
warn() { printf '%s [migration] WARN: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() {
  printf '%s [migration] ERROR: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2
  exit 1
}

# RESULTS_FILE collects one TSV row per assertion: status, name, detail.
RESULTS_FILE=""
SCENARIO=""
EVIDENCE_DIR=""

init_scenario() {
  SCENARIO="$1"
  EVIDENCE_DIR="${MIGRATION_WORKDIR}/${SCENARIO}"
  mkdir -p "${EVIDENCE_DIR}"
  RESULTS_FILE="${EVIDENCE_DIR}/results.tsv"
  : >"${RESULTS_FILE}"
  log "scenario ${SCENARIO}: evidence in ${EVIDENCE_DIR}"
}

record() { # record PASS|FAIL|INFO|WARN <name> [detail]
  local status="$1" name="$2" detail="${3:-}"
  printf '%s\t%s\t%s\n' "${status}" "${name}" "${detail//$'\n'/ }" >>"${RESULTS_FILE}"
  case "${status}" in
    PASS) log "PASS ${name}" ;;
    FAIL) log "FAIL ${name}${detail:+: ${detail}}" ;;
    *) log "${status} ${name}${detail:+: ${detail}}" ;;
  esac
}

assert_true() { # assert_true <name> <command...>
  local name="$1"
  shift
  if "$@"; then record PASS "${name}"; else record FAIL "${name}"; fi
}

# summarize prints the result table and returns non-zero when any assertion
# failed.
summarize() {
  local fails
  fails="$(grep -c '^FAIL' "${RESULTS_FILE}" || true)"
  {
    echo "== ${SCENARIO}: $(grep -c '^PASS' "${RESULTS_FILE}" || true) passed, ${fails} failed"
    column -t -s $'\t' "${RESULTS_FILE}" 2>/dev/null || cat "${RESULTS_FILE}"
  } | tee "${EVIDENCE_DIR}/summary.txt" >&2
  [ "${fails}" -eq 0 ]
}

# ---------------------------------------------------------------------------
# Preconditions
# ---------------------------------------------------------------------------

require_env() { # require_env VAR...
  local var
  for var in "$@"; do
    [ -n "${!var:-}" ] || die "${var} is not set"
  done
}

require_tools() { # require_tools tool...
  local tool
  for tool in "$@"; do
    command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required but is not installed"
  done
}

require_credentials() {
  require_env PROVIDER_GITHUB_APP_ID PROVIDER_GITHUB_APP_INSTALLATION_ID PROVIDER_GITHUB_APP_PRIVATE_KEY_B64
  printf '%s' "${PROVIDER_GITHUB_APP_PRIVATE_KEY_B64}" | base64 -d >/dev/null 2>&1 \
    || die "PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 is not valid base64"
}

# require_cluster_env refuses to run without a cluster name and kubeconfig
# handed in by the launcher. The harness never picks either itself: a default
# name would be shared with other runs, and one run's teardown would delete
# another's cluster.
require_cluster_env() {
  [ -n "${KIND_CLUSTER_NAME:-}" ] \
    || die "KIND_CLUSTER_NAME is not set: the cluster comes from the launcher that owns the cluster lifecycle; this harness never creates one under a name of its own"
  [ -n "${KUBECONFIG:-}" ] \
    || die "KUBECONFIG is not set: the launcher provides the kubeconfig of the cluster it created"
}

# ---------------------------------------------------------------------------
# GitHub REST API (GitHub App installation token)
# ---------------------------------------------------------------------------

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

# mint_token prints an installation token, cached for 50 minutes in the work
# directory so command substitutions in subshells do not mint one per call.
mint_token() {
  if [ -n "${MIGRATION_GITHUB_TOKEN:-}" ]; then
    printf '%s' "${MIGRATION_GITHUB_TOKEN}"
    return 0
  fi
  local cache="${MIGRATION_WORKDIR}/.token" now age=999999
  now="$(date +%s)"
  mkdir -p "${MIGRATION_WORKDIR}"
  if [ -s "${cache}" ]; then
    age=$((now - $(stat -c %Y "${cache}" 2>/dev/null || echo 0)))
  fi
  if [ "${age}" -lt 3000 ]; then
    cat "${cache}"
    return 0
  fi
  local keyfile header payload signature jwt token
  keyfile="$(mktemp)"
  chmod 600 "${keyfile}"
  printf '%s' "${PROVIDER_GITHUB_APP_PRIVATE_KEY_B64}" | base64 -d >"${keyfile}" 2>/dev/null \
    || { rm -f "${keyfile}"; die "PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 is not valid base64"; }
  header="$(printf '{"alg":"RS256","typ":"JWT"}' | b64url)"
  payload="$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' "$((now - 60))" "$((now + 540))" "${PROVIDER_GITHUB_APP_ID}" | b64url)"
  signature="$(printf '%s.%s' "${header}" "${payload}" | openssl dgst -sha256 -sign "${keyfile}" | b64url)" \
    || { rm -f "${keyfile}"; die "could not sign the App JWT"; }
  rm -f "${keyfile}"
  jwt="${header}.${payload}.${signature}"
  token="$(curl -sS --max-time 30 -X POST -H "Accept: application/vnd.github+json" \
    -H "Authorization: Bearer ${jwt}" \
    "${MIGRATION_API_URL}/app/installations/${PROVIDER_GITHUB_APP_INSTALLATION_ID}/access_tokens" | jq -r '.token // empty')"
  [ -n "${token}" ] || die "could not mint an installation token for App ${PROVIDER_GITHUB_APP_ID}, installation ${PROVIDER_GITHUB_APP_INSTALLATION_ID}"
  (umask 077 && printf '%s' "${token}" >"${cache}")
  printf '%s' "${token}"
}

# gh_call <METHOD> <path> [json-body]
# Prints the response body, then a final line holding the HTTP status. The
# caller splits them with gh_status / gh_body.
gh_call() {
  local method="$1" path="$2" body="${3:-}" token
  token="$(mint_token)"
  if [ -n "${body}" ]; then
    printf '%s' "${body}" | curl -sS --max-time 60 -X "${method}" -w '\n%{http_code}' \
      -H "Accept: application/vnd.github+json" -H "X-GitHub-Api-Version: 2022-11-28" \
      -H "Authorization: Bearer ${token}" -H "Content-Type: application/json" \
      --data @- "${MIGRATION_API_URL}${path}"
  else
    curl -sS --max-time 60 -X "${method}" -w '\n%{http_code}' \
      -H "Accept: application/vnd.github+json" -H "X-GitHub-Api-Version: 2022-11-28" \
      -H "Authorization: Bearer ${token}" \
      "${MIGRATION_API_URL}${path}"
  fi
}
gh_status() { printf '%s' "${1##*$'\n'}"; }
gh_body() { printf '%s' "${1%$'\n'*}"; }

# gh_get <path> prints the body of a 2xx response and dies otherwise.
gh_get() {
  local resp status
  resp="$(gh_call GET "$1")"
  status="$(gh_status "${resp}")"
  case "${status}" in
    2??) gh_body "${resp}" ;;
    *) die "GET $1 returned HTTP ${status}: $(gh_body "${resp}" | head -c 300)" ;;
  esac
}

# gh_get_opt <path> prints the body of a 2xx response, prints nothing for a 404
# and dies on anything else.
gh_get_opt() {
  local resp status
  resp="$(gh_call GET "$1")"
  status="$(gh_status "${resp}")"
  case "${status}" in
    2??) gh_body "${resp}" ;;
    404) return 0 ;;
    *) die "GET $1 returned HTTP ${status}: $(gh_body "${resp}" | head -c 300)" ;;
  esac
}

# gh_write <METHOD> <path> [json-body] -- dies unless the status is 2xx.
gh_write() {
  local resp status
  resp="$(gh_call "$@")"
  status="$(gh_status "${resp}")"
  case "${status}" in
    2??) gh_body "${resp}" ;;
    *) die "$1 $2 returned HTTP ${status}: $(gh_body "${resp}" | head -c 400)" ;;
  esac
}

# gh_delete <path> -- a missing object (404) is fine; any other non-2xx is not.
gh_delete() {
  local resp status
  resp="$(gh_call DELETE "$1")"
  status="$(gh_status "${resp}")"
  case "${status}" in
    2??) return 0 ;;
    404) return 0 ;;
    *) warn "DELETE $1 returned HTTP ${status}: $(gh_body "${resp}" | head -c 300)"; return 1 ;;
  esac
}

# gh_list <path> <jq-extractor>
# Pages through a list endpoint (per_page=100) and prints one JSON array. The
# extractor maps one page's body to that page's array (".": the body is the
# array; ".repositories": the array sits under a key).
gh_list() {
  local path="$1" extractor="$2" sep="?" page=1 body items count out="[]"
  case "${path}" in *\?*) sep="&" ;; esac
  while :; do
    body="$(gh_get "${path}${sep}per_page=100&page=${page}")"
    items="$(printf '%s' "${body}" | jq -c "${extractor}")"
    count="$(printf '%s' "${items}" | jq 'length')"
    out="$(jq -cn --argjson a "${out}" --argjson b "${items}" '$a + $b')"
    [ "${count}" -ge 100 ] || break
    page=$((page + 1))
    [ "${page}" -le 50 ] || die "pagination of ${path} did not end after 50 pages"
  done
  printf '%s' "${out}"
}

# require_rate_budget -- the long scenarios poll dozens of objects for about an hour
# and share the installation's 5000 requests an hour with everything else on the
# test organization. Asking GitHub for the budget is free; refusing to start with
# less than MIGRATION_MIN_RATE_BUDGET left (0 disables the check) beats failing
# half way with 403s.
require_rate_budget() {
  local min="${MIGRATION_MIN_RATE_BUDGET:-3500}" left reset
  [ "${min}" -gt 0 ] || return 0
  local body
  body="$(gh_get /rate_limit)" || die "cannot read the GitHub rate limit"
  left="$(printf '%s' "${body}" | jq -r '.resources.core.remaining // 0')"
  reset="$(printf '%s' "${body}" | jq -r '.resources.core.reset // 0')"
  if [ "${left}" -lt "${min}" ]; then
    die "only ${left} GitHub requests are left in this hour (resets at $(date -u -d "@${reset}" +%H:%M:%SZ 2>/dev/null || echo "${reset}")); the scenario needs about ${min}. Wait, or set MIGRATION_MIN_RATE_BUDGET=0 to run anyway"
  fi
  log "GitHub request budget: ${left} left (needs ${min})"
}

# ---------------------------------------------------------------------------
# Run-time values the fixtures need
# ---------------------------------------------------------------------------

# load_runtime_env sources the values oob.sh discovered (member login and role,
# adoption object IDs). Fixtures are rendered from these.
RUNTIME_ENV="${MIGRATION_WORKDIR}/runtime.env"
load_runtime_env() {
  if [ -f "${RUNTIME_ENV}" ]; then
    # shellcheck source=/dev/null
    . "${RUNTIME_ENV}"
  fi
}

runtime_set() { # runtime_set NAME VALUE -- persists a value for later scripts
  mkdir -p "${MIGRATION_WORKDIR}"
  touch "${RUNTIME_ENV}"
  sed -i "/^export $1=/d" "${RUNTIME_ENV}"
  printf 'export %s=%q\n' "$1" "$2" >>"${RUNTIME_ENV}"
}

# render <in> <out> substitutes the __PLACEHOLDER__ tokens in a fixture.
render() {
  local in="$1" out="$2"
  load_runtime_env
  local fork_owner="${MIGRATION_FORK_SOURCE%%/*}" fork_repo="${MIGRATION_FORK_SOURCE##*/}"
  sed \
    -e "s|__ORG__|${MIGRATION_ORG}|g" \
    -e "s|__MEMBER__|${MIGRATION_MEMBER:-octocat}|g" \
    -e "s|__MEMBER_ROLE__|${MIGRATION_MEMBER_ROLE:-member}|g" \
    -e "s|__COLLAB_ROLE__|${MIGRATION_COLLAB_ROLE:-push}|g" \
    -e "s|__TEAM_ROLE__|${MIGRATION_TEAM_ROLE:-member}|g" \
    -e "s|__FORK_OWNER__|${fork_owner}|g" \
    -e "s|__FORK_REPO__|${fork_repo}|g" \
    -e "s|__ADOPT_HOOK_ID__|${MIGRATION_ADOPT_HOOK_ID:-1}|g" \
    "${in}" >"${out}"
}

# ---------------------------------------------------------------------------
# Generic waiting
# ---------------------------------------------------------------------------

# wait_until <timeout-seconds> <interval-seconds> <command...>
wait_until() {
  local timeout="$1" interval="$2" deadline
  shift 2
  deadline=$(($(date +%s) + timeout))
  while ! "$@"; do
    [ "$(date +%s)" -lt "${deadline}" ] || return 1
    sleep "${interval}"
  done
}

# poll_seconds converts a duration such as 15s or 1m into seconds.
poll_seconds() {
  case "$1" in
    *m) echo $((${1%m} * 60)) ;;
    *s) echo "${1%s}" ;;
    *) echo "$1" ;;
  esac
}
