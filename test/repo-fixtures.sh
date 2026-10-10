#!/usr/bin/env bash
# test/repo-fixtures.sh -- disposable repositories for the E2E.
#
# Two update tests need a repository the provider cannot make for itself, and the
# GitHub App the suite runs as has no write access to repository contents, so
# neither can be seeded with a commit. Both are forks of public repositories,
# which GitHub fills with their commits and branches:
#
#   TEMPLATE_REPO   (pgh-e2e-template)   a fork of octocat/Hello-World with all of
#                   its branches (master, octocat-patch-1, test), marked as a
#                   template. The repository-branches examples are created from it
#                   with every branch, which is what gives a Repository more than
#                   one branch to protect and to make the default.
#   WORKFLOWS_REPO  (pgh-e2e-workflows)  a fork of actions/hello-world-docker-action
#                   that carries .github/workflows/ci.yml on main. The RunnerGroup
#                   examples restrict a group to that workflow, which GitHub
#                   accepts only for a workflow file that exists. The Organization
#                   examples list the repository as enabled for Actions for a
#                   moment; deleting it takes it off that list again.
#
# Usage:
#   test/repo-fixtures.sh create   (re)create both repositories and wait until their
#                                  branches and workflow file are visible
#   test/repo-fixtures.sh delete   delete both; a missing repository is fine
#   test/repo-fixtures.sh watch    block until the Kubernetes cluster named by the
#                                  kubeconfig file in WATCH_KUBECONFIG is gone, then
#                                  delete both (the net for a run that ends without
#                                  reaching the teardown script)
#
# Required environment:
#   PROVIDER_GITHUB_APP_ID               GitHub App ID
#   PROVIDER_GITHUB_APP_INSTALLATION_ID  installation ID of the App on the test org
#   PROVIDER_GITHUB_APP_PRIVATE_KEY_B64  the App's PEM private key, base64-encoded
#   E2E_ORG                              the test organization login
#
# Optional environment:
#   TEMPLATE_SOURCE   public owner/repo forked as the template
#                     (default: octocat/Hello-World)
#   WORKFLOWS_SOURCE  public owner/repo forked for the workflow file
#                     (default: actions/hello-world-docker-action)
#   FIXTURE_WAIT      seconds to wait for a fork to appear or disappear (default 240)
#   KUBECTL           path to the kubectl binary (watch only; default: kubectl)
#   WATCH_KUBECONFIG  kubeconfig file for the cluster to watch (watch only; removed on exit)
#   GITHUB_API_URL    API base (default: https://api.github.com)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FIXTURE_SCRIPT=repo-fixtures.sh
# shellcheck source=lib/github-app.sh
. "${ROOT}/test/lib/github-app.sh"

# The names are the ones the examples carry. Nothing else in the organization is
# ever touched.
TEMPLATE_REPO=pgh-e2e-template
WORKFLOWS_REPO=pgh-e2e-workflows
TEMPLATE_SOURCE="${TEMPLATE_SOURCE:-octocat/Hello-World}"
WORKFLOWS_SOURCE="${WORKFLOWS_SOURCE:-actions/hello-world-docker-action}"
FIXTURE_WAIT="${FIXTURE_WAIT:-240}"

require_env PROVIDER_GITHUB_APP_ID PROVIDER_GITHUB_APP_INSTALLATION_ID PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 E2E_ORG
require_tools curl openssl jq base64

# repo_status prints the HTTP status of reading the repository.
repo_status() {
  local token="$1" repo="$2"
  resp_status "$(gh_api "${token}" GET "/repos/${E2E_ORG}/${repo}")"
}

# path_status prints the HTTP status of reading a path under the repository.
path_status() {
  local token="$1" repo="$2" path="$3"
  resp_status "$(gh_api "${token}" GET "/repos/${E2E_ORG}/${repo}/${path}")"
}

# wait_for polls a command until it prints the wanted status or FIXTURE_WAIT runs out.
wait_for() {
  local what="$1" want="$2"
  shift 2
  local deadline got
  deadline=$(( $(date +%s) + FIXTURE_WAIT ))
  while :; do
    got="$("$@")"
    [ "${got}" = "${want}" ] && return 0
    [ "$(date +%s)" -lt "${deadline}" ] || fail "${what}: still HTTP ${got} after ${FIXTURE_WAIT}s, wanted ${want}"
    sleep 5
  done
}

delete_repo() {
  local token="$1" repo="$2" resp status
  resp="$(gh_api "${token}" DELETE "/repos/${E2E_ORG}/${repo}")"
  status="$(resp_status "${resp}")"
  case "${status}" in
    204) echo "repo-fixtures.sh: repository ${repo} deleted from ${E2E_ORG}" ;;
    404) echo "repo-fixtures.sh: repository ${repo} already absent from ${E2E_ORG}" ;;
    *) echo "repo-fixtures.sh: ERROR: deleting repository ${repo} returned HTTP ${status}: $(resp_body "${resp}")" >&2; return 1 ;;
  esac
}

delete_all() {
  local token rc=0
  token="$(mint_token)"
  delete_repo "${token}" "${TEMPLATE_REPO}" || rc=1
  delete_repo "${token}" "${WORKFLOWS_REPO}" || rc=1
  return "${rc}"
}

# fork_repo <token> <source owner/repo> <name> <default-branch-only> forks a public
# repository into the organization under name, replacing a leftover of an earlier
# run, and waits until the fork can be read.
fork_repo() {
  local token="$1" source="$2" name="$3" default_only="$4" resp status
  if [ "$(repo_status "${token}" "${name}")" = 200 ]; then
    echo "repo-fixtures.sh: replacing a leftover repository ${name}"
    delete_repo "${token}" "${name}"
    wait_for "repository ${name} to disappear" 404 repo_status "${token}" "${name}"
  fi
  resp="$(jq -n --arg o "${E2E_ORG}" --arg n "${name}" --argjson d "${default_only}" '{organization: $o, name: $n, default_branch_only: $d}' \
    | gh_api "${token}" POST "/repos/${source}/forks" -H "Content-Type: application/json" --data @-)"
  status="$(resp_status "${resp}")"
  [ "${status}" = 202 ] || fail "forking ${source} as ${name} returned HTTP ${status}: $(resp_body "${resp}")"
  wait_for "repository ${name}" 200 repo_status "${token}" "${name}"
}

create_all() {
  local token resp status
  token="$(mint_token)"

  fork_repo "${token}" "${TEMPLATE_SOURCE}" "${TEMPLATE_REPO}" false
  wait_for "branch master of ${TEMPLATE_REPO}" 200 path_status "${token}" "${TEMPLATE_REPO}" branches/master
  wait_for "branch test of ${TEMPLATE_REPO}" 200 path_status "${token}" "${TEMPLATE_REPO}" branches/test
  resp="$(printf '{"is_template":true}' | gh_api "${token}" PATCH "/repos/${E2E_ORG}/${TEMPLATE_REPO}" -H "Content-Type: application/json" --data @-)"
  status="$(resp_status "${resp}")"
  [ "${status}" = 200 ] || fail "marking ${TEMPLATE_REPO} as a template returned HTTP ${status}: $(resp_body "${resp}")"
  echo "repo-fixtures.sh: template repository ${TEMPLATE_REPO} ready in ${E2E_ORG} (fork of ${TEMPLATE_SOURCE}, all branches)"

  fork_repo "${token}" "${WORKFLOWS_SOURCE}" "${WORKFLOWS_REPO}" true
  wait_for "workflow file of ${WORKFLOWS_REPO}" 200 path_status "${token}" "${WORKFLOWS_REPO}" contents/.github/workflows/ci.yml
  echo "repo-fixtures.sh: workflow repository ${WORKFLOWS_REPO} ready in ${E2E_ORG} (fork of ${WORKFLOWS_SOURCE})"
}

case "${1:-}" in
  create)
    create_all
    ;;
  delete)
    delete_all
    ;;
  watch)
    watch_cluster_gone
    delete_all
    ;;
  *)
    echo "usage: ${0##*/} create|delete|watch" >&2
    exit 2
    ;;
esac
