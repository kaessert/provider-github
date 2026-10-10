#!/usr/bin/env bash
# test/org-secrets.sh -- disposable organization secrets for the E2E.
#
# ActionsSecretAccess and DependabotSecretAccess manage which repositories can
# use an organization secret, and so does the Organization's secrets field; the
# provider never creates the secret. GitHub
# accepts a secret only as a libsodium sealed box of its value, so this script
# asks test/sealedbox (a standard-library Go helper, no module beyond the
# provider's own) to encrypt a random value and PUTs it through the REST API
# with an installation token minted from the three GitHub App credentials.
#
# Usage:
#   test/org-secrets.sh create   create (or overwrite) the eight secrets, visibility selected,
#                                with the repository SELECTED_REPO as the initial selection
#   test/org-secrets.sh delete   delete the eight secrets; a missing secret is fine
#   test/org-secrets.sh watch    block until the Kubernetes cluster named by the
#                                kubeconfig file in WATCH_KUBECONFIG is gone, then
#                                delete the eight secrets (the net for a run that ends
#                                without reaching the teardown script)
#   test/org-secrets.sh check-actions
#                                fail unless the organization's Actions policy is the
#                                one the Organization update test restores: enabled
#                                repositories "selected", exactly ACTIONS_BASELINE_REPOS
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
#   ACTIONS_BASELINE_REPOS
#                     space-separated repositories the organization has enabled for
#                     Actions when no run is in flight (default: "demo-repository-1
#                     demo-repository-2"); only check-actions reads it
#   KUBECTL           path to the kubectl binary (watch only; default: kubectl)
#   WATCH_KUBECONFIG  kubeconfig file for the cluster to watch (watch only; removed on exit)
#   GITHUB_API_URL    API base (default: https://api.github.com)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FIXTURE_SCRIPT=org-secrets.sh
# shellcheck source=lib/github-app.sh
. "${ROOT}/test/lib/github-app.sh"

# All eight secrets are created with visibility selected: the controllers enforce
# (and write) the repository list only while the visibility is selected, and the
# provider cannot change the visibility itself.
SELECTED_REPO="${SELECTED_REPO:-demo-repository-1}"
ACTIONS_BASELINE_REPOS="${ACTIONS_BASELINE_REPOS:-demo-repository-1 demo-repository-2}"

# The names are the external names the examples carry. Nothing else in the
# organization is ever touched. Every managed resource that names a secret gets a
# secret of its own -- the cluster-scoped and the namespaced SecretAccess examples,
# and the cluster-scoped and namespaced Organization examples -- so that no two
# managed resources write one repository access list: they would rewrite each
# other's list.
ACTIONS_SECRET=PGH_E2E_ACTIONS_SECRET
ACTIONS_SECRET_NS=PGH_E2E_ACTIONS_SECRET_NS
DEPENDABOT_SECRET=PGH_E2E_DEPENDABOT_SECRET
DEPENDABOT_SECRET_NS=PGH_E2E_DEPENDABOT_SECRET_NS
ORG_ACTIONS_SECRET=PGH_E2E_ORG_ACTIONS_SECRET
ORG_ACTIONS_SECRET_NS=PGH_E2E_ORG_ACTIONS_SECRET_NS
ORG_DEPENDABOT_SECRET=PGH_E2E_ORG_DEPENDABOT_SECRET
ORG_DEPENDABOT_SECRET_NS=PGH_E2E_ORG_DEPENDABOT_SECRET_NS

require_env PROVIDER_GITHUB_APP_ID PROVIDER_GITHUB_APP_INSTALLATION_ID PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 E2E_ORG
require_tools curl openssl jq base64

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
  delete_secret "${token}" actions "${ACTIONS_SECRET_NS}" || rc=1
  delete_secret "${token}" dependabot "${DEPENDABOT_SECRET}" || rc=1
  delete_secret "${token}" dependabot "${DEPENDABOT_SECRET_NS}" || rc=1
  delete_secret "${token}" actions "${ORG_ACTIONS_SECRET}" || rc=1
  delete_secret "${token}" actions "${ORG_ACTIONS_SECRET_NS}" || rc=1
  delete_secret "${token}" dependabot "${ORG_DEPENDABOT_SECRET}" || rc=1
  delete_secret "${token}" dependabot "${ORG_DEPENDABOT_SECRET_NS}" || rc=1
  return "${rc}"
}

# check_actions fails unless the organization's Actions policy is the baseline the
# Organization update test puts back: enabled repositories "selected" and exactly
# the repositories in ACTIONS_BASELINE_REPOS. The test lists a disposable
# repository next to them and then lists only them again, so a different starting
# list would be replaced for good rather than restored. Nothing is written.
check_actions() {
  local token resp status body policy have want
  token="$(mint_token)"
  resp="$(gh_api "${token}" GET "/orgs/${E2E_ORG}/actions/permissions")"
  status="$(resp_status "${resp}")"
  body="$(resp_body "${resp}")"
  [ "${status}" = 200 ] || fail "reading the Actions policy of ${E2E_ORG} returned HTTP ${status}: ${body}"
  policy="$(printf '%s' "${body}" | jq -r '.enabled_repositories')"
  [ "${policy}" = selected ] || fail "the Actions policy of ${E2E_ORG} enables ${policy} repositories, not selected ones; the Organization update test would change it for good"
  resp="$(gh_api "${token}" GET "/orgs/${E2E_ORG}/actions/permissions/repositories?per_page=100")"
  status="$(resp_status "${resp}")"
  body="$(resp_body "${resp}")"
  [ "${status}" = 200 ] || fail "listing the repositories enabled for Actions in ${E2E_ORG} returned HTTP ${status}: ${body}"
  have="$(printf '%s' "${body}" | jq -r '[.repositories[].name] | sort | join(" ")')"
  # shellcheck disable=SC2086 -- the list is space-separated on purpose
  want="$(printf '%s\n' ${ACTIONS_BASELINE_REPOS} | sort | tr '\n' ' ' | sed 's/ $//')"
  [ "${have}" = "${want}" ] || fail "${E2E_ORG} has these repositories enabled for Actions: [${have}], not the baseline [${want}]; the Organization update test would change the list for good"
  echo "org-secrets.sh: the Actions policy of ${E2E_ORG} is the baseline (selected: ${have})"
}

case "${1:-}" in
  create)
    token="$(mint_token)"
    create_secret "${token}" actions "${ACTIONS_SECRET}"
    create_secret "${token}" actions "${ACTIONS_SECRET_NS}"
    create_secret "${token}" dependabot "${DEPENDABOT_SECRET}"
    create_secret "${token}" dependabot "${DEPENDABOT_SECRET_NS}"
    create_secret "${token}" actions "${ORG_ACTIONS_SECRET}"
    create_secret "${token}" actions "${ORG_ACTIONS_SECRET_NS}"
    create_secret "${token}" dependabot "${ORG_DEPENDABOT_SECRET}"
    create_secret "${token}" dependabot "${ORG_DEPENDABOT_SECRET_NS}"
    ;;
  delete)
    delete_both
    ;;
  watch)
    watch_cluster_gone
    delete_both
    ;;
  check-actions)
    check_actions
    ;;
  *)
    echo "usage: ${0##*/} create|delete|watch|check-actions" >&2
    exit 2
    ;;
esac
