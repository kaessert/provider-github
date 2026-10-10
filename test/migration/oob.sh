#!/usr/bin/env bash
# test/migration/oob.sh -- everything the scenarios do OUTSIDE the provider:
# the setup the provider cannot do for itself, and the cleanup that returns the
# organization to its baseline. Talks to the GitHub REST API only.
#
# Usage:
#   oob.sh prepare <upgrade|adopt>   sweep leftovers, discover the org member,
#                                    take the baseline snapshot, create the
#                                    template repository and the organization
#                                    secrets (upgrade) or seed the objects the
#                                    adoption fixtures name (adopt)
#   oob.sh ensure-environment <repo> <environment> [timeout-seconds]
#                                    wait for <repo>, then create the environment
#   oob.sh wait-branch <repo> <branch> [timeout-seconds]
#                                    wait until <repo> has <branch> with a commit (a
#                                    repository generated from a template gets its content
#                                    asynchronously); exit 3 when the repository itself is
#                                    missing at the timeout, 1 when only the branch is
#   oob.sh seed-protection <rendered-dir> <repo>
#                                    apply the branch protection the rendered Repository
#                                    fixture declares, through the API, after granting the
#                                    team and user access the rule names
#   oob.sh sweep                     delete everything the harness created and
#                                    restore the organization settings recorded
#                                    in the baseline snapshot
#   oob.sh verify-baseline           take a final snapshot and compare it with
#                                    the baseline (timestamps ignored)
#
# The test organization already exists. The organization secrets the SecretAccess
# kinds manage, and the objects the adoption scenarios observe, are created here
# because the provider creates neither.
#
# Everything created carries the pgh-mig- / PGH_MIG_ prefix, and sweep deletes by
# that prefix only. The organization-level settings the fixtures write (the
# description, the Actions policy and its enabled-repository list) are recorded
# in the baseline snapshot BEFORE any change and restored from it.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${HERE}/lib.sh"
# shellcheck source=baseline.sh
. "${HERE}/baseline.sh"

ORG="${MIGRATION_ORG}"
BASELINE="${MIGRATION_WORKDIR}/baseline.json"

# Names of the objects created through the API.
UPGRADE_SECRETS_ACTIONS="PGH_MIG_ACTIONS_SECRET PGH_MIG_ORG_ACTIONS_SECRET"
UPGRADE_SECRETS_DEPENDABOT="PGH_MIG_DEPENDABOT_SECRET PGH_MIG_ORG_DEPENDABOT_SECRET"
ADOPT_REPO="pgh-mig-adopt-repo"
ADOPT_HOOK_URL="${HOOK_URL_PREFIX}/adopt-hook"

need_api() {
  require_tools curl openssl jq base64
  require_credentials
}

# ---------------------------------------------------------------------------
# Discovery
# ---------------------------------------------------------------------------

# discover_member picks the existing organization member the fixtures use (the
# Membership, a team member, a collaborator) unless MIGRATION_MEMBER names one,
# and derives the roles that keep those fixtures free of drift.
discover_member() {
  local login="${MIGRATION_MEMBER:-}" role
  if [ -z "${login}" ]; then
    login="$(gh_list "/orgs/${ORG}/members" '.' | jq -r '[.[] | select(.type == "User")][0].login // empty')"
    [ -n "${login}" ] || die "organization ${ORG} has no user member the fixtures could use; set MIGRATION_MEMBER"
  fi
  role="$(gh_get "/orgs/${ORG}/memberships/${login}" | jq -r '.role // empty')"
  [ -n "${role}" ] || die "${login} is not a member of ${ORG} (set MIGRATION_MEMBER to an existing member)"
  runtime_set MIGRATION_MEMBER "${login}"
  runtime_set MIGRATION_MEMBER_ROLE "${role}"
  # An owner has admin on every repository and is a maintainer of every team
  # they belong to; declaring any other role would make the provider see drift.
  if [ "${role}" = "admin" ]; then
    runtime_set MIGRATION_COLLAB_ROLE admin
    runtime_set MIGRATION_TEAM_ROLE maintainer
  else
    runtime_set MIGRATION_COLLAB_ROLE push
    runtime_set MIGRATION_TEAM_ROLE member
  fi
  log "fixtures use organization member ${login} (role ${role})"
}

# ---------------------------------------------------------------------------
# Creation
# ---------------------------------------------------------------------------

# wait_branch <repo> <branch> [timeout] -- 0 once GET /repos/<org>/<repo>/branches/<branch> answers
# 200 (the branch has a commit); 3 when the repository never appeared, 1 when only the branch did not.
wait_branch() {
  local repo="$1" branch="$2" timeout="${3:-600}" deadline repo_seen=0 status
  deadline=$(($(date +%s) + timeout))
  while :; do
    status="$(gh_status "$(gh_call GET "/repos/${ORG}/${repo}/branches/${branch}")")"
    [ "${status}" != "200" ] || return 0
    if [ "${repo_seen}" -eq 0 ] && [ "$(gh_status "$(gh_call GET "/repos/${ORG}/${repo}")")" = "200" ]; then
      repo_seen=1
    fi
    [ "$(date +%s)" -lt "${deadline}" ] || break
    sleep 5
  done
  if [ "${repo_seen}" -eq 0 ]; then
    warn "repository ${repo} does not exist after ${timeout}s"
    return 3
  fi
  warn "repository ${repo} has no ${branch} branch with a commit after ${timeout}s (GET branches/${branch} answered ${status})"
  return 1
}

create_template_repo() {
  log "creating template repository ${TEMPLATE_REPO}"
  gh_write POST "/orgs/${ORG}/repos" "$(jq -cn --arg n "${TEMPLATE_REPO}" '
    {name: $n, description: "pgh-mig template repository", private: true, auto_init: true, is_template: true}')" >/dev/null
  # GitHub generates the content asynchronously; a repository created from the template before
  # that has no branch, and v0.22.0 meets it in the middle (see baseline.sh).
  wait_branch "${TEMPLATE_REPO}" main 300 \
    || die "GitHub did not generate ${TEMPLATE_REPO}: no commits on main (precondition, not a provider defect)"
  log "${TEMPLATE_REPO} has main with a commit"
}

# seed_protection <rendered-dir> <repo> -- the branch protection the Repository fixture declares,
# applied through the API. The rule names teams and users, and GitHub accepts those only when they
# have push access to the repository: v0.22.0 grants that access in the Update it never reaches, so
# the access the fixture declares is granted first (the same values the baseline would apply).
seed_protection() {
  local dir="$1" repo="$2" branch doc org rule perms kind who role resp status slug
  while IFS=$'\t' read -r _ branch; do
    doc="$(baseline_repo_rule "${dir}" "${repo}" "${branch}")"
    [ -n "${doc}" ] || die "no branch protection rule for ${repo}/${branch} in the rendered fixtures of ${dir}"
    org="$(jq -r '.org' <<<"${doc}")"
    rule="$(jq -c '.rule' <<<"${doc}")"
    perms="$(jq -c '.permissions' <<<"${doc}")"
    while IFS=$'\t' read -r kind who role; do
      [ -n "${kind}" ] || continue
      if [ "${kind}" = team ]; then
        slug="${who}"
        for _ in $(seq 1 60); do
          [ "$(gh_status "$(gh_call GET "/orgs/${org}/teams/${slug}")")" != "200" ] || break
          sleep 5
        done
        gh_write PUT "/orgs/${org}/teams/${slug}/repos/${org}/${repo}" "$(jq -cn --arg p "${role}" '{permission: $p}')" >/dev/null
      else
        resp="$(gh_call PUT "/repos/${org}/${repo}/collaborators/${who}" "$(jq -cn --arg p "${role}" '{permission: $p}')")"
        status="$(gh_status "${resp}")"
        case "${status}" in 2??) ;; *) warn "granting ${who} ${role} on ${repo} answered HTTP ${status}: $(gh_body "${resp}" | head -c 200)" ;; esac
      fi
    done < <(baseline_rule_actors <<<"${perms}")
    gh_write PUT "/repos/${org}/${repo}/branches/${branch}/protection" "$(baseline_protection_body <<<"${rule}")" >/dev/null
    if [ "$(baseline_wants_signatures <<<"${rule}")" = true ]; then
      gh_write POST "/repos/${org}/${repo}/branches/${branch}/protection/required_signatures" >/dev/null
    fi
    log "branch protection of ${repo}/${branch} seeded from the fixture"
  done < <(baseline_protected_repos "${dir}" | awk -F'\t' -v r="${repo}" '$1 == r')
}

# Organization-level Actions settings: the Organization fixture sets the list of
# repositories enabled for Actions, which GitHub only accepts while the policy is
# `selected`. The original policy is in the baseline snapshot and is restored by
# sweep.
ensure_actions_policy_selected() {
  local current
  current="$(gh_get "/orgs/${ORG}/actions/permissions" | jq -r '.enabled_repositories')"
  if [ "${current}" != "selected" ]; then
    log "switching ${ORG} Actions policy ${current} -> selected (restored by sweep)"
    gh_write PUT "/orgs/${ORG}/actions/permissions" '{"enabled_repositories":"selected"}' >/dev/null
  fi
}

repo_id() { gh_get "/repos/${ORG}/$1" | jq -r '.id'; }

# put_org_secret <actions|dependabot> <name> <selected-repository-ids-json>
put_org_secret() {
  local kind="$1" name="$2" ids="$3" pk key_id key sealed
  pk="$(gh_get "/orgs/${ORG}/${kind}/secrets/public-key")"
  key_id="$(printf '%s' "${pk}" | jq -r '.key_id')"
  key="$(printf '%s' "${pk}" | jq -r '.key')"
  sealed="$(openssl rand -hex 16 | tr -d '\n' | go -C "${PROVIDER_ROOT}" run ./test/sealedbox "${key}")" \
    || die "could not encrypt the value of ${kind} secret ${name}"
  gh_write PUT "/orgs/${ORG}/${kind}/secrets/${name}" "$(jq -cn --arg v "${sealed}" --arg k "${key_id}" --argjson ids "${ids}" '
    {encrypted_value: $v, key_id: $k, visibility: "selected", selected_repository_ids: $ids}')" >/dev/null
  log "${kind} secret ${name} ready (visibility selected)"
}

create_upgrade_secrets() {
  local n
  require_tools go
  for n in ${UPGRADE_SECRETS_ACTIONS}; do put_org_secret actions "${n}" '[]'; done
  for n in ${UPGRADE_SECRETS_DEPENDABOT}; do put_org_secret dependabot "${n}" '[]'; done
}

# seed_adoption creates the GitHub objects the Observe-only fixtures name. The
# names and values here are the ones fixtures/adopt declares.
seed_adoption() {
  local rid parent_id member role hook_id
  require_tools go
  load_runtime_env
  member="${MIGRATION_MEMBER:?discover_member must run first}"
  role="${MIGRATION_TEAM_ROLE:-member}"

  log "seeding the adoption targets in ${ORG}"
  gh_write POST "/orgs/${ORG}/repos" "$(jq -cn --arg n "${ADOPT_REPO}" '
    {name: $n, description: "pgh-mig adopted repository", private: true, auto_init: true,
     has_issues: true, has_projects: false, has_wiki: false,
     allow_merge_commit: true, allow_squash_merge: true, allow_rebase_merge: false,
     delete_branch_on_merge: true}')" >/dev/null
  gh_write PUT "/repos/${ORG}/${ADOPT_REPO}/topics" '{"names":["crossplane","pgh-mig"]}' >/dev/null
  rid="$(repo_id "${ADOPT_REPO}")"
  runtime_set MIGRATION_ADOPT_REPO_ID "${rid}"

  parent_id="$(gh_write POST "/orgs/${ORG}/teams" \
    '{"name":"pgh-mig-adopt-parent","description":"pgh-mig adopted parent team","privacy":"closed"}' | jq -r '.id')"
  gh_write POST "/orgs/${ORG}/teams" "$(jq -cn --argjson p "${parent_id}" '
    {name: "pgh-mig-adopt-team", description: "pgh-mig adopted team", privacy: "closed", parent_team_id: $p}')" >/dev/null
  gh_write PUT "/orgs/${ORG}/teams/pgh-mig-adopt-team/memberships/${member}" "$(jq -cn --arg r "${role}" '{role: $r}')" >/dev/null
  gh_write POST "/orgs/${ORG}/teams" \
    '{"name":"pgh-mig-adopt-drift","description":"pgh-mig description on GitHub","privacy":"closed"}' >/dev/null

  gh_write POST "/orgs/${ORG}/actions/variables" "$(jq -cn --argjson r "${rid}" '
    {name: "PGH_MIG_ADOPT_VAR", value: "adopt-value", visibility: "selected", selected_repository_ids: [$r]}')" >/dev/null

  hook_id="$(gh_write POST "/orgs/${ORG}/hooks" "$(jq -cn --arg u "${ADOPT_HOOK_URL}" '
    {name: "web", active: true, events: ["repository"],
     config: {url: $u, content_type: "json", insecure_ssl: "0"}}')" | jq -r '.id')"
  runtime_set MIGRATION_ADOPT_HOOK_ID "${hook_id}"

  gh_write POST "/orgs/${ORG}/actions/runner-groups" "$(jq -cn --argjson r "${rid}" '
    {name: "pgh-mig-adopt-rg", visibility: "selected", selected_repository_ids: [$r],
     allows_public_repositories: false}')" >/dev/null

  put_org_secret actions PGH_MIG_ADOPT_ACTIONS_SECRET "[${rid}]"
  put_org_secret dependabot PGH_MIG_ADOPT_DEPENDABOT_SECRET "[${rid}]"
  log "adoption targets ready (repository ${rid}, webhook ${hook_id})"
}

# ensure_environment waits for a repository to exist and creates a deployment
# environment in it. The ruleset in the pgh-mig-repo-rules fixture requires the
# environment, and the provider creates the repository before it creates the
# ruleset, so this runs beside the apply and the provider's retry then succeeds.
ensure_environment() {
  local repo="$1" env="$2" timeout="${3:-900}" deadline status
  deadline=$(($(date +%s) + timeout))
  while [ "$(date +%s)" -lt "${deadline}" ]; do
    status="$(gh_status "$(gh_call GET "/repos/${ORG}/${repo}")")"
    if [ "${status}" = "200" ]; then
      gh_write PUT "/repos/${ORG}/${repo}/environments/${env}" '{}' >/dev/null
      log "environment ${env} created in ${repo}"
      return 0
    fi
    sleep 10
  done
  warn "repository ${repo} did not appear within ${timeout}s; environment ${env} not created"
  return 1
}

# ---------------------------------------------------------------------------
# Sweep and restore
# ---------------------------------------------------------------------------

sweep_objects() {
  local id name slug rc=0
  for id in $(gh_list "/orgs/${ORG}/hooks" '.' \
    | jq -r --arg p "${HOOK_URL_PREFIX}" '.[] | select((.config.url // "") | startswith($p)) | .id'); do
    gh_delete "/orgs/${ORG}/hooks/${id}" || rc=1
  done
  for id in $(gh_list "/orgs/${ORG}/actions/runner-groups" '.runner_groups' \
    | jq -r --arg p "${PREFIX_LOWER}" '.[] | select(.name | startswith($p)) | .id'); do
    gh_delete "/orgs/${ORG}/actions/runner-groups/${id}" || rc=1
  done
  for name in $(gh_list "/orgs/${ORG}/actions/variables" '.variables' \
    | jq -r --arg p "${PREFIX_UPPER}" '.[] | select(.name | startswith($p)) | .name'); do
    gh_delete "/orgs/${ORG}/actions/variables/${name}" || rc=1
  done
  for kind in actions dependabot; do
    for name in $(gh_list "/orgs/${ORG}/${kind}/secrets" '.secrets' \
      | jq -r --arg p "${PREFIX_UPPER}" '.[] | select(.name | startswith($p)) | .name'); do
      gh_delete "/orgs/${ORG}/${kind}/secrets/${name}" || rc=1
    done
  done
  for slug in $(gh_list "/orgs/${ORG}/teams" '.' \
    | jq -r --arg p "${PREFIX_LOWER}" '.[] | select(.slug | startswith($p)) | .slug'); do
    gh_delete "/orgs/${ORG}/teams/${slug}" || rc=1
  done
  for name in $(gh_list "/orgs/${ORG}/repos?type=all" '.' \
    | jq -r --arg p "${PREFIX_LOWER}" '.[] | select(.name | startswith($p)) | .name'); do
    gh_delete "/repos/${ORG}/${name}" || rc=1
  done
  return "${rc}"
}

# restore_org puts back what the Organization fixture and the Actions policy
# change touched, from the baseline snapshot.
restore_org() {
  [ -f "${BASELINE}" ] || { warn "no baseline snapshot at ${BASELINE}; organization settings not restored"; return 1; }
  local desc policy allowed ids current_desc current_policy current_allowed
  desc="$(jq -r '.org.description' "${BASELINE}")"
  policy="$(jq -r '.org.actions.enabled_repositories // ""' "${BASELINE}")"
  allowed="$(jq -r '.org.actions.allowed_actions // ""' "${BASELINE}")"
  current_desc="$(gh_get "/orgs/${ORG}" | jq -r '.description // ""')"
  if [ "${current_desc}" != "${desc}" ]; then
    log "restoring organization description"
    gh_write PATCH "/orgs/${ORG}" "$(jq -cn --arg d "${desc}" '{description: $d}')" >/dev/null || return 1
  fi
  if [ -n "${policy}" ]; then
    current_policy="$(gh_get "/orgs/${ORG}/actions/permissions" | jq -r '.enabled_repositories')"
    current_allowed="$(gh_get "/orgs/${ORG}/actions/permissions" | jq -r '.allowed_actions // ""')"
    if [ "${current_policy}" != "${policy}" ] || { [ -n "${allowed}" ] && [ "${current_allowed}" != "${allowed}" ]; }; then
      log "restoring Actions policy (${policy}, ${allowed:-unchanged})"
      gh_write PUT "/orgs/${ORG}/actions/permissions" "$(jq -cn --arg p "${policy}" --arg a "${allowed}" '
        {enabled_repositories: $p} + (if $a != "" and $p != "none" then {allowed_actions: $a} else {} end)')" >/dev/null || return 1
    fi
    if [ "${policy}" = "selected" ]; then
      # Only repositories that still exist can be listed; the baseline ones do.
      ids="$(jq -c '[.org.actionsEnabledRepos[].id]' "${BASELINE}")"
      log "restoring the Actions-enabled repository list"
      gh_write PUT "/orgs/${ORG}/actions/permissions/repositories" "$(jq -cn --argjson i "${ids}" '{selected_repository_ids: $i}')" >/dev/null || return 1
    fi
  fi
  return 0
}

cmd_sweep() {
  local stale="${1:-}" rc=0
  need_api
  if [ "${stale}" = "--stale" ]; then
    log "removing leftovers of earlier runs (pgh-mig- / PGH_MIG_ objects)"
  else
    log "sweeping every pgh-mig- / PGH_MIG_ object from ${ORG}"
  fi
  sweep_objects || rc=1
  if [ "${stale}" != "--stale" ]; then
    restore_org || rc=1
  fi
  return "${rc}"
}

cmd_verify_baseline() {
  need_api
  [ -f "${BASELINE}" ] || die "no baseline snapshot at ${BASELINE}"
  load_runtime_env
  local final="${MIGRATION_WORKDIR}/final.json" attempt out=""
  # GitHub lists lag a few seconds behind deletes; retry before calling it a
  # difference.
  for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
    "${HERE}/snapshot.sh" dump "${final}" || { sleep 10; continue; }
    if out="$("${HERE}/snapshot.sh" diff "${BASELINE}" "${final}" --ignore-timestamps)"; then
      printf '%s\n' "${out}"
      return 0
    fi
    log "organization not yet at baseline (attempt ${attempt}/12)"
    sleep 10
  done
  printf '%s\n' "${out}" >&2
  return 1
}

cmd_prepare() {
  local flavour="${1:?usage: oob.sh prepare <upgrade|adopt>}"
  need_api
  mkdir -p "${MIGRATION_WORKDIR}"
  : >"${RUNTIME_ENV}"
  gh_get "/orgs/${ORG}" >/dev/null
  cmd_sweep --stale || warn "some leftovers could not be removed"
  discover_member
  load_runtime_env
  "${HERE}/snapshot.sh" dump "${BASELINE}" || die "could not take the baseline snapshot"
  log "baseline snapshot: ${BASELINE}"
  create_template_repo
  case "${flavour}" in
    upgrade)
      # The Organization fixture sets the Actions-enabled repository list, which
      # GitHub accepts only under the `selected` policy. Adoption is Observe-only
      # and changes no organization setting.
      ensure_actions_policy_selected
      create_upgrade_secrets
      ;;
    adopt) seed_adoption ;;
    *) die "unknown flavour ${flavour} (want upgrade or adopt)" ;;
  esac
}

case "${1:-}" in
  prepare) shift; cmd_prepare "$@" ;;
  ensure-environment)
    shift
    need_api
    ensure_environment "${1:?usage: oob.sh ensure-environment <repo> <environment> [timeout]}" "${2:?environment}" "${3:-900}"
    ;;
  wait-branch)
    shift
    need_api
    wait_branch "${1:?usage: oob.sh wait-branch <repo> <branch> [timeout]}" "${2:?branch}" "${3:-600}"
    ;;
  seed-protection)
    shift
    need_api
    seed_protection "${1:?usage: oob.sh seed-protection <rendered-dir> <repo>}" "${2:?repo}"
    ;;
  sweep) shift; cmd_sweep "$@" ;;
  verify-baseline) shift; cmd_verify_baseline "$@" ;;
  *)
    echo "usage: ${0##*/} prepare <upgrade|adopt> | ensure-environment <repo> <env> [timeout] | wait-branch <repo> <branch> [timeout] | seed-protection <dir> <repo> | sweep | verify-baseline" >&2
    exit 2
    ;;
esac
