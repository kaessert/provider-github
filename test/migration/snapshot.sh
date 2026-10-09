#!/usr/bin/env bash
# test/migration/snapshot.sh -- GitHub state snapshot and diff for the migration
# scenarios. This is the evidence for "no recreate, no write": it dumps the
# numeric ID and every managed setting of everything the fixtures touch, and
# its diff mode reports any difference between two dumps.
#
# Usage:
#   snapshot.sh dump <out.json>                     read GitHub, write a snapshot
#   snapshot.sh diff <a.json> <b.json> [--ignore-timestamps]
#                                                   exit 0 when identical, 1 otherwise
#   snapshot.sh ids  <snapshot.json>                list "<object> <id>" lines
#
# dump issues GET requests only. It reads, for the organization MIGRATION_ORG:
#
#   org           id, description, Actions policy, the Actions-enabled repository list
#   membership    role and state of MIGRATION_MEMBER (when set)
#   repos         every repository whose name starts with pgh-mig-: settings,
#                 topics, fork/template origin, collaborators, team access,
#                 webhooks (the secret is masked by GitHub, so presence shows),
#                 branch protection of every protected branch, rulesets,
#                 environments (recorded as the string "unreadable" when the
#                 installation is refused with 403: the App can create an
#                 environment but not list them)
#   teams         every team whose slug starts with pgh-mig-: settings, parent,
#                 members with their team role
#   variables     every PGH_MIG_ organization variable, with its selected repositories
#   secrets       every PGH_MIG_ organization Actions and Dependabot secret
#                 (metadata only; GitHub never returns the value), with its
#                 selected repositories
#   orgHooks      every organization webhook whose URL starts with
#                 https://example.com/pgh-mig
#   runnerGroups  every runner group whose name starts with pgh-mig-
#
# Anything outside those prefixes is not read into the comparison, so other
# tests running against the same organization cannot make a diff noisy.
#
# Timestamps (updated_at and the like) are filed under "_ts" keys. A diff
# compares them by default -- a changed updated_at on an object whose settings
# did not change is itself evidence of a write -- and --ignore-timestamps drops
# them for comparisons that legitimately touch an object (restoring the
# organization description at cleanup).
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${HERE}/lib.sh"

ORG="${MIGRATION_ORG}"

# strip_urls removes the *url fields GitHub echoes on most objects: they carry no
# setting and embed the API host.
STRIP_URLS='walk(if type == "object" then with_entries(select(.key | (. == "url" or endswith("_url") or . == "_links") | not)) else . end)'

snapshot_org() {
  local org actions repos="[]" policy
  org="$(gh_get "/orgs/${ORG}")"
  actions="$(gh_get "/orgs/${ORG}/actions/permissions")"
  policy="$(printf '%s' "${actions}" | jq -r '.enabled_repositories // ""')"
  if [ "${policy}" = "selected" ]; then
    repos="$(gh_list "/orgs/${ORG}/actions/permissions/repositories" '.repositories' \
      | jq -c 'map({id, name}) | sort_by(.name)')"
  fi
  jq -cn --argjson org "${org}" --argjson actions "${actions}" --argjson repos "${repos}" '
    {
      id: $org.id,
      login: $org.login,
      description: ($org.description // ""),
      actions: {
        enabled_repositories: ($actions.enabled_repositories // null),
        allowed_actions: ($actions.allowed_actions // null)
      },
      actionsEnabledRepos: $repos,
      _ts: {updated_at: ($org.updated_at // null)}
    }'
}

snapshot_membership() {
  [ -n "${MIGRATION_MEMBER:-}" ] || { echo 'null'; return; }
  local m
  m="$(gh_get_opt "/orgs/${ORG}/memberships/${MIGRATION_MEMBER}")"
  if [ -z "${m}" ]; then echo 'null'; return; fi
  printf '%s' "${m}" | jq -c '{login: .user.login, id: .user.id, role, state}'
}

# snapshot_environments prints the repository's environments as a JSON array, or
# the string "unreadable" when the installation may not list them: the App can
# create an environment (administration) but listing needs a permission it does
# not hold, so GitHub answers 403 and nothing about environments can be compared.
snapshot_environments() { # snapshot_environments <repo>
  local resp status
  resp="$(gh_call GET "/repos/${ORG}/$1/environments")"
  status="$(gh_status "${resp}")"
  case "${status}" in
    2??) gh_body "${resp}" | jq -c '[(.environments // [])[] | {id, name}] | sort_by(.name)' 2>/dev/null || echo '[]' ;;
    403) echo '"unreadable"' ;;
    404) echo '[]' ;;
    *) die "GET /repos/${ORG}/$1/environments returned HTTP ${status}: $(gh_body "${resp}" | head -c 300)" ;;
  esac
}

snapshot_repo() { # snapshot_repo <name>
  local name="$1" repo collabs teams hooks branches protections="{}" rulesets="[]" envs b prot ids id one
  repo="$(gh_get "/repos/${ORG}/${name}")"
  collabs="$(gh_list "/repos/${ORG}/${name}/collaborators?affiliation=direct" '.' \
    | jq -c 'map({login, id, role_name}) | sort_by(.login)')"
  teams="$(gh_list "/repos/${ORG}/${name}/teams" '.' | jq -c 'map({slug, id, permission}) | sort_by(.slug)')"
  hooks="$(gh_list "/repos/${ORG}/${name}/hooks" '.' \
    | jq -c "map({id, name, active, hookUrl: .config.url, events: (.events | sort), config, _ts: {updated_at}}) | sort_by(.id) | ${STRIP_URLS}")"
  branches="$(gh_list "/repos/${ORG}/${name}/branches?protected=true" '.' | jq -r '.[].name')"
  for b in ${branches}; do
    prot="$(gh_get_opt "/repos/${ORG}/${name}/branches/${b}/protection")"
    [ -n "${prot}" ] || continue
    protections="$(jq -cn --argjson a "${protections}" --arg b "${b}" --argjson p "$(printf '%s' "${prot}" | jq -c "${STRIP_URLS}")" '$a + {($b): $p}')"
  done
  rulesets="$(gh_list "/repos/${ORG}/${name}/rulesets?includes_parents=false" '.' | jq -c '[.[] | {id, name}]')"
  if [ "$(printf '%s' "${rulesets}" | jq 'length')" -gt 0 ]; then
    ids="$(printf '%s' "${rulesets}" | jq -r '.[].id')"
    rulesets="[]"
    for id in ${ids}; do
      one="$(gh_get "/repos/${ORG}/${name}/rulesets/${id}" \
        | jq -c "{id, name, target, enforcement, bypass_actors, conditions, rules, _ts: {updated_at}} | ${STRIP_URLS}")"
      rulesets="$(jq -cn --argjson a "${rulesets}" --argjson b "${one}" '$a + [$b]')"
    done
    rulesets="$(printf '%s' "${rulesets}" | jq -c 'sort_by(.name)')"
  fi
  envs="$(snapshot_environments "${name}")"
  jq -cn --argjson repo "${repo}" --argjson collabs "${collabs}" --argjson teams "${teams}" \
    --argjson hooks "${hooks}" --argjson protections "${protections}" --argjson rulesets "${rulesets}" \
    --argjson envs "${envs}" '
    {
      id: $repo.id,
      name: $repo.name,
      settings: {
        private: $repo.private,
        visibility: $repo.visibility,
        archived: $repo.archived,
        is_template: $repo.is_template,
        description: ($repo.description // ""),
        default_branch: $repo.default_branch,
        topics: (($repo.topics // []) | sort),
        fork: $repo.fork,
        parent: ($repo.parent.full_name // null),
        template_repository: ($repo.template_repository.full_name // null),
        allow_merge_commit: $repo.allow_merge_commit,
        allow_squash_merge: $repo.allow_squash_merge,
        allow_rebase_merge: $repo.allow_rebase_merge,
        allow_auto_merge: $repo.allow_auto_merge,
        allow_update_branch: $repo.allow_update_branch,
        delete_branch_on_merge: $repo.delete_branch_on_merge,
        has_issues: $repo.has_issues,
        has_projects: $repo.has_projects,
        has_wiki: $repo.has_wiki,
        has_discussions: $repo.has_discussions,
        merge_commit_title: $repo.merge_commit_title,
        merge_commit_message: $repo.merge_commit_message,
        squash_merge_commit_title: $repo.squash_merge_commit_title,
        squash_merge_commit_message: $repo.squash_merge_commit_message
      },
      collaborators: $collabs,
      teams: $teams,
      hooks: $hooks,
      branchProtection: $protections,
      rulesets: $rulesets,
      environments: $envs,
      _ts: {updated_at: $repo.updated_at}
    }'
}

snapshot_repos() {
  local names out="{}" name one
  names="$(gh_list "/orgs/${ORG}/repos?type=all" '.' | jq -r --arg p "${PREFIX_LOWER}" '.[] | select(.name | startswith($p)) | .name' | sort)"
  for name in ${names}; do
    one="$(snapshot_repo "${name}")"
    out="$(jq -cn --argjson a "${out}" --arg n "${name}" --argjson o "${one}" '$a + {($n): $o}')"
  done
  printf '%s' "${out}"
}

snapshot_teams() {
  local slugs out="{}" slug team members roles m role one
  slugs="$(gh_list "/orgs/${ORG}/teams" '.' | jq -r --arg p "${PREFIX_LOWER}" '.[] | select(.slug | startswith($p)) | .slug' | sort)"
  for slug in ${slugs}; do
    team="$(gh_get "/orgs/${ORG}/teams/${slug}")"
    members="$(gh_list "/orgs/${ORG}/teams/${slug}/members" '.' | jq -r '.[].login' | sort)"
    roles="[]"
    for m in ${members}; do
      role="$(gh_get_opt "/orgs/${ORG}/teams/${slug}/memberships/${m}" | jq -r '.role // "unknown"')"
      roles="$(jq -cn --argjson a "${roles}" --arg l "${m}" --arg r "${role}" '$a + [{login: $l, role: $r}]')"
    done
    one="$(jq -cn --argjson t "${team}" --argjson members "${roles}" '
      {
        id: $t.id, name: $t.name, slug: $t.slug,
        description: ($t.description // ""),
        privacy: $t.privacy,
        notification_setting: ($t.notification_setting // null),
        permission: ($t.permission // null),
        parent: (if $t.parent then {id: $t.parent.id, slug: $t.parent.slug} else null end),
        members: $members,
        _ts: {updated_at: ($t.updated_at // null)}
      }')"
    out="$(jq -cn --argjson a "${out}" --arg n "${slug}" --argjson o "${one}" '$a + {($n): $o}')"
  done
  printf '%s' "${out}"
}

snapshot_variables() {
  local vars out="{}" name one sel
  vars="$(gh_list "/orgs/${ORG}/actions/variables" '.variables' \
    | jq -c --arg p "${PREFIX_UPPER}" '[.[] | select(.name | startswith($p))]')"
  for name in $(printf '%s' "${vars}" | jq -r '.[].name' | sort); do
    sel="[]"
    if [ "$(printf '%s' "${vars}" | jq -r --arg n "${name}" '.[] | select(.name == $n) | .visibility')" = "selected" ]; then
      sel="$(gh_list "/orgs/${ORG}/actions/variables/${name}/repositories" '.repositories' | jq -c 'map({id, name}) | sort_by(.name)')"
    fi
    one="$(printf '%s' "${vars}" | jq -c --arg n "${name}" --argjson sel "${sel}" '
      .[] | select(.name == $n) | {name, value, visibility, selectedRepositories: $sel, _ts: {created_at, updated_at}}')"
    out="$(jq -cn --argjson a "${out}" --arg n "${name}" --argjson o "${one}" '$a + {($n): $o}')"
  done
  printf '%s' "${out}"
}

snapshot_secrets() { # snapshot_secrets actions|dependabot
  local kind="$1" secrets out="{}" name one sel
  secrets="$(gh_list "/orgs/${ORG}/${kind}/secrets" '.secrets' \
    | jq -c --arg p "${PREFIX_UPPER}" '[.[] | select(.name | startswith($p))]')"
  for name in $(printf '%s' "${secrets}" | jq -r '.[].name' | sort); do
    sel="[]"
    if [ "$(printf '%s' "${secrets}" | jq -r --arg n "${name}" '.[] | select(.name == $n) | .visibility')" = "selected" ]; then
      sel="$(gh_list "/orgs/${ORG}/${kind}/secrets/${name}/repositories" '.repositories' | jq -c 'map({id, name}) | sort_by(.name)')"
    fi
    one="$(printf '%s' "${secrets}" | jq -c --arg n "${name}" --argjson sel "${sel}" '
      .[] | select(.name == $n) | {name, visibility, selectedRepositories: $sel, _ts: {created_at, updated_at}}')"
    out="$(jq -cn --argjson a "${out}" --arg n "${name}" --argjson o "${one}" '$a + {($n): $o}')"
  done
  printf '%s' "${out}"
}

snapshot_org_hooks() {
  gh_list "/orgs/${ORG}/hooks" '.' \
    | jq -c --arg p "${HOOK_URL_PREFIX}" '
        [.[] | select((.config.url // "") | startswith($p))
             | {id, active, events: (.events | sort), config, _ts: {updated_at}}]
        | map({(.config.url): .}) | add // {}'
}

snapshot_runner_groups() {
  local groups out="{}" id name one sel
  groups="$(gh_list "/orgs/${ORG}/actions/runner-groups" '.runner_groups' \
    | jq -c --arg p "${PREFIX_LOWER}" '[.[] | select(.name | startswith($p))]')"
  for id in $(printf '%s' "${groups}" | jq -r 'sort_by(.name) | .[].id'); do
    name="$(printf '%s' "${groups}" | jq -r --argjson i "${id}" '.[] | select(.id == $i) | .name')"
    sel="[]"
    if [ "$(printf '%s' "${groups}" | jq -r --argjson i "${id}" '.[] | select(.id == $i) | .visibility')" = "selected" ]; then
      sel="$(gh_list "/orgs/${ORG}/actions/runner-groups/${id}/repositories" '.repositories' | jq -c 'map({id, name}) | sort_by(.name)')"
    fi
    one="$(printf '%s' "${groups}" | jq -c --argjson i "${id}" --argjson sel "${sel}" '
      .[] | select(.id == $i) | {id, name, visibility, allows_public_repositories,
        restricted_to_workflows, selected_workflows: ((.selected_workflows // []) | sort),
        selectedRepositories: $sel}')"
    out="$(jq -cn --argjson a "${out}" --arg n "${name}" --argjson o "${one}" '$a + {($n): $o}')"
  done
  printf '%s' "${out}"
}

cmd_dump() {
  local out="${1:?usage: snapshot.sh dump <out.json>}"
  require_tools curl jq openssl base64
  require_credentials
  mkdir -p "$(dirname "${out}")"
  load_runtime_env
  log "snapshot of ${ORG} -> ${out}"
  jq -n \
    --argjson org "$(snapshot_org)" \
    --argjson membership "$(snapshot_membership)" \
    --argjson repos "$(snapshot_repos)" \
    --argjson teams "$(snapshot_teams)" \
    --argjson variables "$(snapshot_variables)" \
    --argjson actionsSecrets "$(snapshot_secrets actions)" \
    --argjson dependabotSecrets "$(snapshot_secrets dependabot)" \
    --argjson orgHooks "$(snapshot_org_hooks)" \
    --argjson runnerGroups "$(snapshot_runner_groups)" '
    {org: $org, membership: $membership, repos: $repos, teams: $teams,
     variables: $variables,
     secrets: {actions: $actionsSecrets, dependabot: $dependabotSecrets},
     orgHooks: $orgHooks, runnerGroups: $runnerGroups}' >"${out}.tmp" \
    && mv "${out}.tmp" "${out}"
}

# normalized <file> [ignore-timestamps] -- canonical form used for comparison.
normalized() {
  if [ "${2:-}" = "ignore" ]; then
    jq -S 'walk(if type == "object" then del(._ts) else . end)' "$1"
  else
    jq -S . "$1"
  fi
}

# ids_of prints "<path> <id>" for every object that carries a numeric id.
ids_of() {
  jq -r '
    [paths(type == "object" and has("id") and (.id | type) == "number")] as $ps
    | $ps[] as $p
    | "\($p | map(tostring) | join("/")) \(getpath($p).id)"' "$1" | sort
}

cmd_diff() {
  local a="${1:?usage: snapshot.sh diff <a.json> <b.json> [--ignore-timestamps]}" b="${2:?usage: snapshot.sh diff <a.json> <b.json> [--ignore-timestamps]}" mode="${3:-}"
  [ -f "${a}" ] || die "snapshot ${a} not found"
  [ -f "${b}" ] || die "snapshot ${b} not found"
  local ignore=""
  [ "${mode}" = "--ignore-timestamps" ] && ignore="ignore"
  local rc=0 d
  d="$(diff -u <(ids_of "${a}") <(ids_of "${b}") || true)"
  if [ -n "${d}" ]; then
    echo "== numeric IDs differ (an object was created, deleted or recreated)"
    printf '%s\n' "${d}"
    rc=1
  fi
  d="$(diff -u <(normalized "${a}" "${ignore}") <(normalized "${b}" "${ignore}") || true)"
  if [ -n "${d}" ]; then
    echo "== settings${ignore:+ (timestamps ignored)} differ"
    printf '%s\n' "${d}"
    rc=1
  fi
  [ "${rc}" -eq 0 ] && echo "snapshots identical${ignore:+ (timestamps ignored)}"
  return "${rc}"
}

case "${1:-}" in
  dump) shift; cmd_dump "$@" ;;
  diff) shift; cmd_diff "$@" ;;
  ids) shift; ids_of "${1:?usage: snapshot.sh ids <snapshot.json>}" ;;
  *) echo "usage: ${0##*/} dump <out.json> | diff <a.json> <b.json> [--ignore-timestamps] | ids <snapshot.json>" >&2; exit 2 ;;
esac
