# test/migration/upgrade-compare.sh -- the comparisons of the in-place upgrade
# scenario that need a judgement, kept apart from the driver so the self-test
# can run them against files. Sourced; not executable on its own.
#
# What the upgrade must not do is change an object the baseline had already
# reconciled. An object the baseline never reconciled is another matter: the
# candidate finishing that work is not a change to a reconciled object.
#
#   * an OrganizationVariable that is not Synced on the baseline was never created on GitHub;
#     the candidate creates it, so it is left out of the GitHub comparison;
#   * a Repository that is listed as not settled on the baseline exists on GitHub (the
#     baseline created it) but its settings and branch protection were never applied; the
#     candidate's first Update applies them. Its numeric IDs (the repository, its webhooks,
#     rulesets, collaborators and team access) stay in the comparison; its settings, its
#     branch protection and its own timestamp are reported as INFO.
#
# Every object that was Synced and Ready on the baseline stays under the strict assertions.
#
# shellcheck shell=bash

# upgrade_reset_run_state -- removes what an earlier run left in EVIDENCE_DIR (the default
# workdir is reused by every run) and that the comparisons below read or append to: the list of
# objects the baseline did not settle, the provider logs the write attribution searches, the
# narrowed snapshots, the per-repository deltas and the report tables. Without it a baseline that
# settles would still be narrowed by the previous run's list, and an old log's Update lines would
# be counted as this run's writes. Called once, before anything else is written.
upgrade_reset_run_state() {
  rm -f "${EVIDENCE_DIR}/baseline-not-ready.txt" "${EVIDENCE_DIR}/after-upgrade-not-ready.txt" \
    "${EVIDENCE_DIR}/k8s-regressions.txt" "${EVIDENCE_DIR}/k8s-v2-reread.json" "${EVIDENCE_DIR}/report-tables.md" \
    "${EVIDENCE_DIR}"/provider-*.log "${EVIDENCE_DIR}"/unreconciled-*-delta.txt "${EVIDENCE_DIR}"/snapshots/*-filtered.json
}

# upgrade_unreconciled <kind> <baseline-not-ready.txt> <k8s-v1.json> -- the GitHub names (external
# names) of the objects of one kind that are listed as not settled on the baseline.
upgrade_unreconciled() {
  [ -s "$2" ] || return 0
  grep "^$1/" "$2" | cut -d: -f1 | cut -d/ -f2 \
    | while read -r n; do jq -r --arg k "$1" --arg n "${n}" '.[] | select(.kind == $k and .name == $n) | .externalName' "$3"; done
}

# upgrade_filter_snapshot <in.json> <out.json> <variables> <repositories> -- drops the named
# organization variables altogether, and the settings, branch protection and timestamp of the
# named repositories. Both names lists are space separated.
upgrade_filter_snapshot() {
  jq --arg v "$3" --arg r "$4" '
    ($v | split(" ") | map(select(length > 0))) as $vars
    | ($r | split(" ") | map(select(length > 0))) as $repos
    | .variables |= with_entries(select(.key as $k | ($vars | index($k)) | not))
    | .repos |= with_entries(if (.key as $k | $repos | index($k)) then .value |= del(.settings, .branchProtection, ._ts) else . end)' \
    "$1" >"$2"
}

# upgrade_repo_delta <before.json> <after.json> <repository> -- what the candidate's reconciliation
# changed on a repository the baseline never reconciled.
upgrade_repo_delta() {
  diff -u <(jq -S --arg r "$3" '.repos[$r] | {settings, branchProtection}' "$1") \
          <(jq -S --arg r "$3" '.repos[$r] | {settings, branchProtection}' "$2")
}

# upgrade_prepare_snapshots -- chooses the two snapshots the assertions compare. Reads
# EVIDENCE_DIR/baseline-not-ready.txt, k8s-v1.json and snapshots/after-v1.json, after-v2.json;
# sets SNAP_BEFORE and SNAP_AFTER (names under snapshots/) and records what it left out.
upgrade_prepare_snapshots() {
  local snaps="${EVIDENCE_DIR}/snapshots" notready="${EVIDENCE_DIR}/baseline-not-ready.txt" vars repos r delta
  # shellcheck disable=SC2034  # read by the driver and the self-test
  SNAP_BEFORE="after-v1"
  # shellcheck disable=SC2034
  SNAP_AFTER="after-v2"
  [ -s "${notready}" ] || return 0
  vars="$(upgrade_unreconciled OrganizationVariable "${notready}" "${EVIDENCE_DIR}/k8s-v1.json" | paste -sd' ')"
  repos="$(upgrade_unreconciled Repository "${notready}" "${EVIDENCE_DIR}/k8s-v1.json" | paste -sd' ')"
  [ -n "${vars}${repos}" ] || return 0
  upgrade_filter_snapshot "${snaps}/after-v1.json" "${snaps}/after-v1-filtered.json" "${vars}" "${repos}"
  upgrade_filter_snapshot "${snaps}/after-v2.json" "${snaps}/after-v2-filtered.json" "${vars}" "${repos}"
  # shellcheck disable=SC2034  # read by the driver and the self-test
  SNAP_BEFORE="after-v1-filtered"
  # shellcheck disable=SC2034
  SNAP_AFTER="after-v2-filtered"
  [ -z "${vars}" ] || record INFO "organization variables the baseline never applied are left out of the GitHub comparison (the candidate created them)" "${vars}"
  for r in ${repos}; do
    delta="${EVIDENCE_DIR}/unreconciled-${r}-delta.txt"
    upgrade_repo_delta "${snaps}/after-v1.json" "${snaps}/after-v2.json" "${r}" >"${delta}"
    record INFO "repository ${r}: the candidate completed a reconciliation the baseline never finished (settings and branch protection; its IDs are still compared)" \
      "$(grep -c '^[-+][^-+]' "${delta}" || true) differing line(s), see $(basename "${delta}")"
  done
}

# upgrade_k8s_compare <k8s-v1.json> <k8s-v2.json> [final] -- one "Kind/name<TAB>finding" line per managed
# resource the upgrade deleted, recreated, renamed or regressed. An object that was Synced and Ready
# on the baseline and is Synced with Ready=False and a "drift:" Ready message after the upgrade is
# mid-correction (the provider reports drift it is about to fix): it is reported as
# "drift-pending" instead, for the caller to read again after one poll. With "final" there is no
# such tolerance.
upgrade_k8s_compare() {
  jq -rn --arg final "${3:-}" --slurpfile b "$1" --slurpfile a "$2" '
    ($b[0] | map({key: "\(.kind)/\(.name)", value: .}) | from_entries) as $B
    | ($a[0] | map({key: "\(.kind)/\(.name)", value: .}) | from_entries) as $A
    | $B | to_entries[] | .key as $k | .value as $v
    | if ($A[$k] | not) then "\($k)\tdeleted"
      elif $A[$k].uid != $v.uid then "\($k)\trecreated"
      elif $A[$k].externalName != $v.externalName then "\($k)\texternal-name \($v.externalName) -> \($A[$k].externalName)"
      elif ($v.ready == "True" and $v.synced == "True") and ($A[$k].ready != "True" or $A[$k].synced != "True") then
        if $final != "final" and $A[$k].synced == "True" and $A[$k].ready == "False" and (($A[$k].readyMessage // "") | startswith("drift:"))
          then "\($k)\tdrift-pending: Ready=False Synced=True \($A[$k].readyMessage)"
          else "\($k)\tregressed: Ready=\($A[$k].ready) Synced=\($A[$k].synced) \($A[$k].syncedMessage)"
        end
      else empty end'
}

# upgrade_update_lines <kind> <name> <group> <log>... -- how many times the provider logged an update or
# a creation of one managed resource.
upgrade_update_lines() {
  local kind="$1" name="$2" group="$3"
  shift 3
  [ "$#" -gt 0 ] || { echo 0; return 0; }
  cat "$@" 2>/dev/null \
    | grep -F "\"managed/$(printf '%s' "${kind}" | tr '[:upper:]' '[:lower:]').${group}\"" \
    | grep -E "\"request\": *\{[^}]*\"name\": *\"${name}\"" \
    | grep -cE 'Successfully requested (update|creation)' || true
}

# upgrade_ts_moves <before.json> <after.json> -- "<Kind>\t<object>\t<path>\t<before>\t<after>" for every
# timestamp that moved between two snapshots. The kind is the managed resource kind that owns the
# top-level snapshot entry (a repository's webhook timestamp is filed under the repository).
upgrade_ts_moves() {
  jq -rn --slurpfile b "$1" --slurpfile a "$2" '
    def tsmap: [tostream | select(length == 2 and (.[0] | any(. == "_ts")))
                | {key: (.[0] | map(tostring) | join("\u001f")), value: {p: .[0], v: .[1]}}] | from_entries;
    ($b[0] | tsmap) as $B | ($a[0] | tsmap) as $A
    | (($B | keys) + ($A | keys) | unique)[]
    | . as $k | ($B[$k].v) as $x | ($A[$k].v) as $y
    | select($x != $y)
    | ($A[$k].p // $B[$k].p) as $p
    | [ ({"org": "Organization", "repos": "Repository", "teams": "Team", "variables": "OrganizationVariable",
          "orgHooks": "OrganizationWebhook", "runnerGroups": "RunnerGroup"}[$p[0]]
         // (if $p[0] == "secrets" then (if $p[1] == "actions" then "ActionsSecretAccess" else "DependabotSecretAccess" end) else $p[0] end)),
        (if $p[0] == "org" then "-" elif $p[0] == "secrets" then $p[2] else $p[1] end),
        ($p | map(tostring) | join("/")), ($x // "-"), ($y // "-") ]
    | @tsv'
}

# upgrade_write_attribution <before.json> <after.json> <k8s-v2.json> <group> <report-tables.md> <candidate log>...
# -- tells a provider write from any other updated_at move: a move on an object whose managed resource
# has a create or update line in the candidate's log can be the provider's; a move on one without cannot, and was
# made by somebody else.
upgrade_write_attribution() {
  local before="$1" after="$2" k8s="$3" group="$4" out="$5" kind obj path x y mr n verdict rows="" provider=0 other=0 unmatched=0
  shift 5
  while IFS=$'\t' read -r kind obj path x y; do
    [ -n "${kind}" ] || continue
    mr="$(jq -r --arg k "${kind}" --arg o "${obj}" '[.[] | select(.kind == $k and (.name == $o or .externalName == $o or $o == "-"))][0].name // ""' "${k8s}")"
    if [ -z "${mr}" ]; then
      verdict="unmatched: no ${kind} managed resource carries the external name ${obj}"
      unmatched=$((unmatched + 1))
    else
      n="$(upgrade_update_lines "${kind}" "${mr}" "${group}" "$@")"
      if [ "${n}" -gt 0 ]; then
        verdict="provider write: ${n} create/update line(s) for ${kind}/${mr}"
        provider=$((provider + 1))
      else
        verdict="NOT a provider write: no create/update line for ${kind}/${mr}"
        other=$((other + 1))
      fi
    fi
    rows+="| ${path} | ${x} | ${y} | ${verdict} |"$'\n'
  done < <(upgrade_ts_moves "${before}" "${after}")
  {
    echo
    echo "## updated_at moves across the upgrade"
    echo
    echo "${provider} on objects the provider wrote, ${other} on objects it did not, ${unmatched} unmatched. An object with no create or update line in the"
    echo "candidate's log was written by something else; a timestamp move on one is not the provider's."
    echo
    if [ -n "${rows}" ]; then
      echo "| Snapshot path | Before | After | Attribution |"
      echo "|---|---|---|---|"
      printf '%s' "${rows}"
    else
      echo "No timestamp moved."
    fi
  } >>"${out}"
  # shellcheck disable=SC2034  # read by the driver
  UPGRADE_TS_PROVIDER="${provider}" UPGRADE_TS_OTHER="${other}" UPGRADE_TS_UNMATCHED="${unmatched}"
}
