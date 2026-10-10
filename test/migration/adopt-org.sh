#!/usr/bin/env bash
# test/migration/adopt-org.sh -- the Organization of the adoption scenarios (b) and (c), and the
# external writer that resets it. Sourced by adopt-common.sh; not executable on its own.
#
# Something outside the cluster, and outside this harness, writes the test organization back to the
# values it held before the harness changed it (its description and its Actions-enabled
# repositories) about every five minutes. The provider is not the writer: under Observe it issues
# no Organization write at all. What the provider does see is an Organization whose GitHub side is
# the baseline again, and it reports it the way it reports any drift: Ready=False with
#
#   drift: actions.enabledRepos: GitHub has [<the baseline's enabled repositories>], spec declares [...]
#
# so the harness separates that one signature from every other Organization state, and from every
# other kind:
#
#   * a sampler reads the organization's description and Actions-enabled repositories (two GETs a
#     sample) into org-samples.log for the whole adoption window;
#   * an Organization that is Ready=False with the signature, while a sample of the window shows the
#     baseline values, is an external reset (a WARN naming the sample), not a failure;
#   * the zero-write snapshot comparison leaves the Organization's own values out only when the
#     provider's log shows no Organization Update in the window and a sample shows the baseline
#     values; under full management an Organization Update is the provider correcting the reset
#     (INFO) only when a sample within the poll before it shows the baseline values, else a FAIL.
#
# shellcheck shell=bash

# The Organization the fixtures manage (the name of the managed resource).
ADOPT_ORG_NAME="pgh-mig-org"
# RFC 3339 start of the window the checks of a phase read the samples from; the phases set it.
ADOPT_WINDOW_SINCE=""

# The organization's values before the harness changed anything, and the Ready message prefix that
# reports them. Empty ORG_RESET_PREFIX turns the whole classification off.
ORG_BASE_DESC=""
ORG_BASE_REPOS="[]"
ORG_RESET_PREFIX=""

# org_baseline_load [baseline.json] -- reads the organization's baseline values from the snapshot
# oob.sh took before it changed the organization.
org_baseline_load() {
  local f="${1:-${MIGRATION_WORKDIR}/baseline.json}"
  ORG_BASE_DESC="" ORG_BASE_REPOS="[]" ORG_RESET_PREFIX=""
  if [ ! -s "${f}" ]; then
    warn "no baseline snapshot at ${f}: an external reset of the organization cannot be told from drift"
    return 0
  fi
  ORG_BASE_DESC="$(jq -r '.org.description // ""' "${f}")"
  ORG_BASE_REPOS="$(jq -c '[.org.actionsEnabledRepos[]?.name] | sort' "${f}")"
  [ "${ORG_BASE_REPOS}" != "[]" ] \
    || { warn "the baseline organization has no Actions-enabled repositories: its reset has no signature"; return 0; }
  ORG_RESET_PREFIX="$(jq -r '"drift: actions.enabledRepos: GitHub has [" + ([.org.actionsEnabledRepos[]?.name] | sort | join(" ")) + "]"' "${f}")"
}

# The jq form of org_reset_match, for filters over mr_state output: orgreset($sig).
# shellcheck disable=SC2016,SC2034 # read by adopt-common.sh
ORG_RESET_JQ='def orgreset($sig): .kind == "Organization" and .synced == "True" and .ready == "False" and $sig != "" and ((.readyMessage // "") | startswith($sig));'

# org_reset_match <kind> <ready> <synced> <ready message> -- true for an Organization that is
# Synced, Ready=False, and reports the drift of the baseline's enabled repositories.
org_reset_match() {
  [ -n "${ORG_RESET_PREFIX}" ] && [ "$1" = Organization ] && [ "$2" = False ] && [ "$3" = True ] \
    && [[ "$4" == "${ORG_RESET_PREFIX}"* ]]
}

# --- the sampler ---------------------------------------------------------------------

# org_sample_once -- "<timestamp>\t<description>\t<enabled repositories, JSON>" for the organization
# now: two GETs. Returns non-zero, printing nothing, when GitHub cannot be read.
org_sample_once() {
  local body desc resp repos
  body="$(gh_get "/orgs/${MIGRATION_ORG}")" || return 1
  desc="$(printf '%s' "${body}" | jq -r '(.description // "") | gsub("[\t\n]"; " ")')" || return 1
  resp="$(gh_call GET "/orgs/${MIGRATION_ORG}/actions/permissions/repositories?per_page=100")" || return 1
  case "$(gh_status "${resp}")" in
    2??) repos="$(gh_body "${resp}" | jq -c '[.repositories[]?.name] | sort')" || return 1 ;;
    409) repos='[]' ;; # the policy is not `selected`: there is no list
    *) return 1 ;;
  esac
  printf '%s\t%s\t%s\n' "$(now_utc)" "${desc}" "${repos}"
}

# org_sampler_start -- samples the organization in the background until the scenario's cleanup
# kills it. A sample that cannot be read is skipped. The interval is a third of the poll period by
# default, so that a reset that lives for less than a poll is still seen.
org_sampler_start() {
  local out="${EVIDENCE_DIR}/org-samples.log" interval
  interval="${MIGRATION_ORG_SAMPLE_INTERVAL:-$(($(poll_seconds "${MIGRATION_POLL}") / 3))}"
  [ "${interval}" -ge 5 ] || interval=5
  : >"${out}"
  (
    while :; do
      (org_sample_once >>"${out}") 2>/dev/null
      sleep "${interval}"
    done
  ) &
  BACKGROUND_PIDS+=("$!")
  log "sampling the organization into org-samples.log every ${interval}s"
}

# org_baseline_samples [from] [to] -- the samples (RFC 3339 bounds, both inclusive, either may be
# empty) whose description and enabled repositories are the baseline's.
org_baseline_samples() {
  local samples="${EVIDENCE_DIR}/org-samples.log"
  [ -n "${ORG_RESET_PREFIX}" ] && [ -s "${samples}" ] || return 0
  jq -Rr --arg d "${ORG_BASE_DESC}" --argjson r "${ORG_BASE_REPOS}" --arg from "${1:-}" --arg to "${2:-}" '
    split("\t") | select(length == 3)
    | select(.[1] == $d and ((.[2] | fromjson?) == $r))
    | select(($from == "" or .[0] >= $from) and ($to == "" or .[0] <= $to))
    | join("\t")' "${samples}"
}

# org_reset_sample [from] -- the latest sample since <from> that shows the baseline values; non-zero
# when there is none.
org_reset_sample() {
  local s
  s="$(org_baseline_samples "${1:-}" | tail -n 1)"
  [ -n "${s}" ] || return 1
  printf '%s' "${s//$'\t'/ | }"
}

# --- the provider's Organization writes ------------------------------------------------

# log_update_times <log> <Kind> <name> [group] -- the timestamps of the provider's update lines for
# one managed resource.
log_update_times() {
  local group="${4:-${ADOPT_GROUP}}"
  grep -F "\"managed/$(printf '%s' "$2" | tr 'A-Z' 'a-z').${group}\"" "$1" 2>/dev/null \
    | grep -E "\"request\": *\{[^}]*\"name\": *\"$3\"" \
    | grep 'Successfully requested update' | cut -f1 || true
}

# org_attribute_updates <log> <name> -- "<attributed> <unattributed> [times of the unattributed]":
# an Organization update is attributed to the external reset when a sample within the poll before it
# shows the baseline values (the Organization was reset, and the provider corrected it).
org_attribute_updates() {
  local poll t from ok=0 bad=0 badlist=""
  poll="$(poll_seconds "${MIGRATION_POLL}")"
  while IFS= read -r t; do
    [ -n "${t}" ] || continue
    from="$(date -u -d "${t} - ${poll} seconds" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null)" || from="${t}"
    if [ -n "$(org_baseline_samples "${from}" "${t}")" ]; then
      ok=$((ok + 1))
    else
      bad=$((bad + 1))
      badlist+="${t} "
    fi
  done < <(log_update_times "$1" Organization "$2")
  printf '%s %s %s' "${ok}" "${bad}" "${badlist% }"
}

# --- the snapshot comparisons --------------------------------------------------------

# org_filter_snapshot <in.json> <out.json> -- the snapshot without the organization's own values:
# its description, its Actions settings and enabled repositories, and its timestamp.
org_filter_snapshot() {
  jq 'del(.org.description, .org.actions, .org.actionsEnabledRepos, .org._ts)' "$1" >"$2"
}

# org_comparison_exempt <observe|full> -- true when the organization's own values may be left out of
# the zero-write comparison of the phase: a sample of the window shows the baseline values (the
# external writer was at work) and the provider wrote nothing to the Organization, or (under full
# management) every write it made was a correction attributed to the reset.
org_comparison_exempt() {
  local log="${EVIDENCE_DIR}/window-$1.log" ok bad _
  [ -n "$(org_baseline_samples "${ADOPT_WINDOW_SINCE}")" ] || return 1
  case "$1" in
    observe) [ -z "$(log_update_times "${log}" Organization "${ADOPT_ORG_NAME}")" ] ;;
    full)
      read -r ok bad _ <<<"$(org_attribute_updates "${log}" "${ADOPT_ORG_NAME}")"
      [ "${bad}" -eq 0 ]
      ;;
    *) return 1 ;;
  esac
}

# adopt_assert_zero_write <check name> <snapshot a> <snapshot b> <observe|full> -- the zero-write
# comparison of two snapshots; when org_comparison_exempt holds it is made over copies without the
# organization's own values, and says so.
adopt_assert_zero_write() {
  local name="$1" a="$2" b="$3" phase="$4" snaps="${EVIDENCE_DIR}/snapshots"
  if org_comparison_exempt "${phase}"; then
    org_filter_snapshot "${snaps}/${a}.json" "${snaps}/${a}-org-filtered.json"
    org_filter_snapshot "${snaps}/${b}.json" "${snaps}/${b}-org-filtered.json"
    record INFO "${phase}: the organization's description, Actions settings and updated_at are left out of the comparison of ${a} and ${b}" \
      "a sample of the window shows the baseline values (the external reset) and the provider's log has no Organization write that the reset does not explain; latest sample: $(org_reset_sample "${ADOPT_WINDOW_SINCE}")"
    assert_snapshots_identical "${name}" "${a}-org-filtered" "${b}-org-filtered"
  else
    assert_snapshots_identical "${name}" "${a}" "${b}"
  fi
}
