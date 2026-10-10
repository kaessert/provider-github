#!/usr/bin/env bash
# test/migration/adopt-unsynced.sh -- which objects of the baseline the adoption scenarios (b) and
# (c) adopt, and the objects the baseline created on GitHub but never finished. Sourced by
# adopt-common.sh, validate-fixtures.sh and selftest.sh; not executable on its own. The
# functions that read a file are the only ones that touch the disk: no cluster, no GitHub call.
#
# The baseline provider reports Synced=False for an object whose Observe fails after the create
# went through. v0.22.0 does that for a repository it has created, with its webhook and its ruleset,
# when the default branch is protected by the ruleset and not by classic branch protection
# ("observe failed: branch is not protected"): the repository exists on GitHub and the object is
# Synced=False. Whether an object is Synced therefore says nothing about whether GitHub holds it;
# the snapshot of GitHub taken on the baseline does. The rule:
#
#   * not Synced and not in the GitHub snapshot  -> the baseline never created it: left out;
#   * not Synced and in the GitHub snapshot      -> adopted like the others, listed with the
#                                                   message the baseline reported (the object
#                                                   "completes" under full management: the
#                                                   candidate finishes what the baseline could not);
#   * Synced (Ready or not)                      -> adopted.
#
# An object adopted although it was not Synced may issue exactly one Update under full management
# (INFO, named, attributed to the baseline never finishing it); a second Update, a create, or any
# Update under Observe is a FAIL like any other. A sub-object the baseline's GitHub snapshot does
# not hold (the classic branch protection of that repository) is "GitHub holds none" under Observe,
# and must exist once the one Update has run.
#
# shellcheck shell=bash

# baseline_snapshot_path <rendered-v1-dir> <Kind> <name> -- the path of the GitHub object of a v1
# fixture in a snapshot, as a JSON array; nothing when the kind has none or the fixture is absent.
# The key is the external name of the fixture (its crossplane.io/external-name annotation, else
# its name); a webhook is keyed by its URL.
baseline_snapshot_path() {
  local doc
  doc="$(K="$2" N="$3" yq -o=json -I=0 'select(.kind == strenv(K) and .metadata.name == strenv(N))' "$1"/*.yaml 2>/dev/null | head -n1)"
  [ -n "${doc}" ] || return 0
  jq -c '(.metadata.annotations["crossplane.io/external-name"] // .metadata.name) as $e
    | if .kind == "Repository" then ["repos", $e]
      elif .kind == "Team" then ["teams", $e]
      elif .kind == "OrganizationVariable" then ["variables", $e]
      elif .kind == "OrganizationWebhook" then ["orgHooks", .spec.forProvider.url]
      elif .kind == "RunnerGroup" then ["runnerGroups", $e]
      elif .kind == "ActionsSecretAccess" then ["secrets", "actions", $e]
      elif .kind == "DependabotSecretAccess" then ["secrets", "dependabot", $e]
      elif .kind == "Organization" then ["org"]
      elif .kind == "Membership" then ["membership"]
      else empty end' <<<"${doc}"
}

# baseline_on_github <snapshot> <rendered-v1-dir> <Kind> <name> -- the GitHub snapshot holds the
# object of that v1 fixture.
baseline_on_github() {
  local path
  path="$(baseline_snapshot_path "$2" "$3" "$4")"
  [ -n "${path}" ] || return 1
  jq -e --argjson p "${path}" 'getpath($p) != null' "$1" >/dev/null 2>&1
}

# baseline_excluded <k8s-v1.json> <snapshot> <rendered-v1-dir> -- "Kind/name" of the objects the
# baseline never created on GitHub: not Synced, and absent from the snapshot. An object that is
# Synced exists (it is adopted whether or not it is Ready); one that is not Synced but is on GitHub
# is adopted too (baseline_unsynced_adopted).
baseline_excluded() {
  local kind name
  while IFS=$'\t' read -r kind name; do
    baseline_on_github "$2" "$3" "${kind}" "${name}" || printf '%s/%s\n' "${kind}" "${name}"
  done < <(jq -r '.[] | select(.synced != "True") | [.kind, .name] | @tsv' "$1")
}

# baseline_unsynced_adopted <k8s-v1.json> <snapshot> <rendered-v1-dir> -- "Kind/name: Ready=.. Synced=..
# <message>" of the adopted objects the baseline did not report Synced: it created them on GitHub and
# failed to finish them.
baseline_unsynced_adopted() {
  local kind name line
  while IFS=$'\t' read -r kind name line; do
    baseline_on_github "$2" "$3" "${kind}" "${name}" && printf '%s/%s: %s\n' "${kind}" "${name}" "${line}"
  done < <(jq -r '.[] | select(.synced != "True")
    | [.kind, .name, "Ready=\(.ready) Synced=\(.synced) \(if (.syncedMessage // "") != "" then .syncedMessage else (.readyMessage // "") end)"]
    | map(gsub("[\t\n]"; " ")) | @tsv' "$1")
}

# baseline_names <k8s-v1.json> <snapshot> <rendered-v1-dir> -- the baseline's managed-resource state
# with the external name of an adopted webhook that was not Synced taken from GitHub: the baseline
# may not have recorded the hook ID, and the derivation would otherwise use a placeholder.
baseline_names() {
  local out kind name path id
  out="$(cat "$1")"
  while IFS=$'\t' read -r kind name; do
    [ "${kind}" = OrganizationWebhook ] || continue
    path="$(baseline_snapshot_path "$3" "${kind}" "${name}")"
    [ -n "${path}" ] || continue
    id="$(jq -r --argjson p "${path}" 'getpath($p).id // empty' "$2" 2>/dev/null)"
    [ -n "${id}" ] || continue
    out="$(jq -c --arg n "${name}" --arg id "${id}" 'map(if .kind == "OrganizationWebhook" and .name == $n and .externalName == "" then .externalName = $id else . end)' <<<"${out}")"
  done < <(jq -r '.[] | select(.synced != "True") | [.kind, .name] | @tsv' "$1")
  printf '%s\n' "${out}"
}

# adopt_completing <Kind/name> -- the object was adopted although the baseline did not report it
# Synced (v1-unsynced-adopted.txt).
adopt_completing() {
  local list="${EVIDENCE_DIR}/v1-unsynced-adopted.txt"
  [ -s "${list}" ] && cut -d: -f1 "${list}" | grep -qxF "$1"
}

# adopt_completing_message <Kind/name> -- what the baseline reported for it.
adopt_completing_message() {
  awk -v k="$1" 'index($0, k ": ") == 1 { print substr($0, length(k) + 3); exit }' "${EVIDENCE_DIR}/v1-unsynced-adopted.txt" 2>/dev/null
}

# completing_update_ok <creates> <updates> -- the writes of an object adopted although it was not
# Synced, under full management: exactly one update and no create.
completing_update_ok() {
  [ "$1" -eq 0 ] && [ "$2" -eq 1 ]
}

# nested_none <Kind> <managed resource> <row path> <atProvider.json> <snapshot.json> -- the sub-object
# of the row is empty in what status.atProvider reports and in what GitHub holds. Only the
# sub-objects of a repository that the baseline can leave unfinished are known.
nested_none() {
  local kind="$1" mr="$2" rid="$3" a s
  [ "${kind}" = Repository ] || return 1
  case "${rid}" in
    .spec.forProvider.branchProtectionRules*) a='(.branchProtectionRules // [])'; s='(.branchProtection // {})' ;;
    .spec.forProvider.repositoryRules*) a='(.repositoryRules // [])'; s='(.rulesets // [])' ;;
    .spec.forProvider.webhooks*) a='(.webhooks // [])'; s='(.hooks // [])' ;;
    .spec.forProvider.permissions.teams*) a='(.permissions.teams // [])'; s='(.teams // [])' ;;
    .spec.forProvider.permissions.users*) a='(.permissions.users // [])'; s='(.collaborators // [])' ;;
    .spec.forProvider.permissions) a='((.permissions.teams // []) + (.permissions.users // []))'; s='((.teams // []) + (.collaborators // []))' ;;
    *) return 1 ;;
  esac
  jq -e --arg k "${kind}/${mr}" ".[\$k] | ${a} | length == 0" "$4" >/dev/null 2>&1 \
    && jq -e --arg m "${mr}" ".repos[\$m] | . != null and (${s} | length == 0)" "$5" >/dev/null 2>&1
}

# manifest_declares <manifest.yaml> <row path> -- the manifest declares something at the row's path
# (a non-empty value).
manifest_declares() {
  [ -e "$1" ] || return 1
  yq -o=json -I=0 '.' "$1" | jq -e "[(${2})? | select(. != null and . != [] and . != {})] | length > 0" >/dev/null 2>&1
}

# unsynced_snapshot_filter <phase> -- the jq program that leaves out of a snapshot what the one
# completing update of each such object (full management only) writes: for a repository, the
# sub-objects the baseline snapshot holds none of and its own timestamp; for any other kind, the
# object. Prints nothing when no object is concerned, and leaves out nothing of an object that
# issued more than the one update or a create (its comparison then fails like any other).
unsynced_snapshot_filter() {
  local entry kind name path counts creates updates paths prog="" v1="${EVIDENCE_DIR}/snapshots/v1.json"
  [ "$1" = full ] && [ -s "${EVIDENCE_DIR}/v1-unsynced-adopted.txt" ] || return 0
  while IFS= read -r entry; do
    entry="${entry%%: *}"
    kind="${entry%%/*}"
    name="${entry#*/}"
    path="$(baseline_snapshot_path "${EVIDENCE_DIR}/rendered/v1" "${kind}" "${name}")"
    [ -n "${path}" ] || continue
    counts="$(log_counts "${EVIDENCE_DIR}/window-full.log" "${kind}" "${name}")"
    read -r creates updates _ <<<"${counts}"
    completing_update_ok "${creates}" "${updates}" || continue
    if [ "${kind}" = Repository ]; then
      paths="$(jq -nc --argjson p "${path}" --slurpfile v "${v1}" '
        ($v[0] | getpath($p)) as $r
        | [$p + ["_ts"]] + [("branchProtection", "rulesets", "hooks", "collaborators", "teams") | select(($r[.] // []) | length == 0) | $p + [.]]')"
    else
      paths="[${path}]"
    fi
    prog+="${prog:+ | }delpaths(${paths})"
  done <"${EVIDENCE_DIR}/v1-unsynced-adopted.txt"
  printf '%s' "${prog}"
}

# adopt_assert_zero_write <check name> <snapshot a> <snapshot b> <observe|full> -- the zero-write
# comparison of two snapshots. Under full management it is made over copies without what the one
# completing update of an object adopted although the baseline never finished it writes
# (unsynced_snapshot_filter), and says so. A second update of that object, or a create, is not
# explained and stays in the comparison.
adopt_assert_zero_write() {
  local name="$1" a="$2" b="$3" phase="$4" snaps="${EVIDENCE_DIR}/snapshots" prog entry
  prog="$(unsynced_snapshot_filter "${phase}")"
  if [ -n "${prog}" ]; then
    entry="$(cut -d: -f1 "${EVIDENCE_DIR}/v1-unsynced-adopted.txt" | paste -sd, -)"
    record INFO "${phase}: what the one completing update writes is left out of the comparison of ${a} and ${b}" \
      "the baseline never finished ${entry}; for a repository, the sub-objects the baseline's GitHub snapshot holds none of and its updated_at; IDs and every other value stay compared"
    jq "${prog}" "${snaps}/${a}.json" >"${snaps}/${a}-filtered.json"
    jq "${prog}" "${snaps}/${b}.json" >"${snaps}/${b}-filtered.json"
    assert_snapshots_identical "${name}" "${a}-filtered" "${b}-filtered"
  else
    assert_snapshots_identical "${name}" "${a}" "${b}"
  fi
}
