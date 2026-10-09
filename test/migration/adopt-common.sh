#!/usr/bin/env bash
# test/migration/adopt-common.sh -- the body shared by scenarios (b) and (c),
# Observe-only adoption. Sourced by scenario-adopt-cluster.sh and
# scenario-adopt-namespaced.sh; not executable on its own.
#
# The setup step creates, through the GitHub API, one object of every kind the
# provider can adopt (oob.sh, seed-adoption). The candidate provider is then
# given managed resources with managementPolicies [Observe] that name them.
# Observe-only adoption must:
#
#   * resolve every object (Synced), and report Ready for every one whose
#     declared values match GitHub;
#   * fill status.atProvider from GitHub, including the id and the fields the
#     object did not declare (the relaxed schema lets Observe-only objects leave
#     create-time fields out);
#   * write nothing: the GitHub snapshot after adoption equals the one before,
#     including the object whose declared description differs from GitHub;
#   * leave GitHub alone when the managed resources are deleted.
#
# This file holds two flows:
#
#   run_adopt       the API-seeded Observe-only adoption above (scenario (c))
#   run_adopt_full  the full adoption flow (scenario (b)): the baseline provider
#                   creates every v1 fixture, the objects are orphaned and the
#                   baseline removed, the candidate adopts each one with a NEW
#                   Observe-only managed resource derived from its v1 fixture,
#                   the same objects are then switched to full management, and
#                   one deliberate change per kind is made. See run_adopt_full.
#
# shellcheck shell=bash

# shellcheck source=adopt-derive.sh
. "${MIGRATION_DIR}/adopt-derive.sh"

# run_adopt <cluster|namespaced>
run_adopt() {
  local scope="$1" group fixtures ns_args=() crd_scope
  case "${scope}" in
    cluster) group="${GROUP_CLUSTER}"; crd_scope=cluster ;;
    namespaced) group="${GROUP_NAMESPACED}"; ns_args=(-n default); crd_scope=all ;;
    *) die "unknown adoption scope ${scope}" ;;
  esac
  fixtures="${FIXTURES_DIR}/adopt/${scope}"

  scenario_begin "adopt-${scope}"
  use_candidate_tools
  build_tree "${MIGRATION_CANDIDATE_DIR}" "${CANDIDATE_VERSION_LABEL}"

  "${MIGRATION_DIR}/oob.sh" prepare adopt || die "out-of-band setup failed"
  load_runtime_env
  render_fixtures "${fixtures}" "${EVIDENCE_DIR}/rendered/adopt"

  cluster_up
  load_package "${MIGRATION_CANDIDATE_DIR}" candidate "${DIGEST_CANDIDATE}"
  install_provider "${MIGRATION_CANDIDATE_DIR}" v2 "${DIGEST_CANDIDATE}" --debug "--poll=${MIGRATION_POLL}"
  mapfile -t crds < <(cluster_crds "${crd_scope}")
  wait_provider "${DIGEST_CANDIDATE}" "${crds[@]}"
  apply_provider_config all
  record PASS "candidate provider is Healthy with the ${scope} CRDs"

  take_snapshot seeded

  log "applying the Observe-only ${scope} adoption fixtures"
  apply_fixtures "${EVIDENCE_DIR}/rendered/adopt"

  # Every object must be Synced; every one except the drift probe must be Ready.
  if wait_until "${MIGRATION_READY_TIMEOUT}" 10 adoption_settled "${group}"; then
    record PASS "every adopted object is Synced, and Ready unless it is the drift probe"
  else
    not_settled "${group}" >"${EVIDENCE_DIR}/adopt-not-settled.txt"
    record FAIL "every adopted object is Synced, and Ready unless it is the drift probe" \
      "$(head -3 "${EVIDENCE_DIR}/adopt-not-settled.txt" | tr '\n' ';')"
  fi
  settle_pause
  mr_state "${group}" >"${EVIDENCE_DIR}/k8s-adopted.json"
  capture_provider_logs adopt
  log_findings adopt

  adoption_assertions "${group}" ${ns_args[@]+"${ns_args[@]}"}

  take_snapshot adopted
  assert_snapshots_identical "no write: GitHub is identical before and after adoption (IDs, settings, timestamps)" seeded adopted

  # Deleting an Observe-only managed resource must not touch the GitHub object.
  delete_managed_resources "${group}" 300 || record WARN "deleting the adopted managed resources needed forced finalizer removal"
  sleep 20
  take_snapshot deleted
  assert_snapshots_identical "deleting the Observe-only managed resources leaves GitHub untouched" seeded deleted
  exit 0
}

# adoption_settled <group> -- Synced for all, Ready for all but the drift probe.
adoption_settled() {
  local state
  state="$(mr_state "$1")"
  [ "$(printf '%s' "${state}" | jq 'length')" -ge 11 ] || return 1
  [ "$(printf '%s' "${state}" | jq '[.[] | select(.synced != "True" or (.ready != "True" and .name != "pgh-mig-adopt-drift"))] | length')" -eq 0 ]
}

# adoption_assertions <group> [-n namespace]
adoption_assertions() {
  local group="$1" bad kind name path want got plural org_desc
  shift
  local nsargs=("$@")

  record INFO "drift probe pgh-mig-adopt-drift" "$(jq -r '.[] | select(.name == "pgh-mig-adopt-drift") | "Ready=\(.ready) Synced=\(.synced) \(.syncedMessage)"' "${EVIDENCE_DIR}/k8s-adopted.json")"

  # status.atProvider.id is filled on the kinds that carry the external name.
  bad="$(jq -r '.[] | select(.kind | IN("Organization","Membership","Team","Repository","OrganizationVariable","ActionsSecretAccess","DependabotSecretAccess"))
    | select(.atProviderId == null or .atProviderId == "") | "\(.kind)/\(.name)"' "${EVIDENCE_DIR}/k8s-adopted.json")"
  if [ -z "${bad}" ]; then
    record PASS "status.atProvider.id is filled on the seven kinds that gain it"
  else
    record FAIL "status.atProvider.id is filled on the seven kinds that gain it" "$(printf '%s' "${bad}" | head -3 | tr '\n' ';')"
  fi

  # The numeric IDs of the webhook and the runner group are GitHub's.
  want="${MIGRATION_ADOPT_HOOK_ID:-}"
  got="$(jq -r '.[] | select(.kind == "OrganizationWebhook") | .atProviderId' "${EVIDENCE_DIR}/k8s-adopted.json")"
  if [ -n "${want}" ] && [ "${got}" = "${want}" ]; then
    record PASS "OrganizationWebhook status.atProvider.id is the GitHub hook ID (${want})"
  else
    record FAIL "OrganizationWebhook status.atProvider.id is the GitHub hook ID" "want ${want}, got ${got}"
  fi
  want="$(jq -r '.runnerGroups["pgh-mig-adopt-rg"].id' "${EVIDENCE_DIR}/snapshots/seeded.json")"
  got="$(jq -r '.[] | select(.kind == "RunnerGroup") | .atProviderId' "${EVIDENCE_DIR}/k8s-adopted.json")"
  if [ "${got}" = "${want}" ]; then
    record PASS "RunnerGroup status.atProvider.id is the GitHub runner group ID (${want})"
  else
    record FAIL "RunnerGroup status.atProvider.id is the GitHub runner group ID" "want ${want}, got ${got}"
  fi

  # The mirror reports GitHub's values.
  while IFS=$'\t' read -r kind name path want; do
    case "${kind}" in '' | \#*) continue ;; esac
    want="${want//__MEMBER_ROLE__/${MIGRATION_MEMBER_ROLE:-member}}"
    plural="$(plural_of "${kind}")"
    got="$(kc get "${plural}.${group}" "${name}" ${nsargs[@]+"${nsargs[@]}"} -o json 2>/dev/null | jq -c ".status.atProvider | ${path}" 2>/dev/null)"
    if [ "${got}" = "$(printf '%s' "${want}" | jq -c .)" ]; then
      record PASS "status.atProvider mirror ${kind}/${name} ${path} = ${want}"
    else
      record FAIL "status.atProvider mirror ${kind}/${name} ${path}" "want ${want}, got ${got:-nothing}"
    fi
  done <"${MIGRATION_DIR}/expect-adopt-mirror.tsv"

  # The Organization is adopted without a declared description; the mirror reports
  # the one the organization had before the run.
  org_desc="$(jq -r '.org.description' "${MIGRATION_WORKDIR}/baseline.json")"
  got="$(kc get "organizations.${group}" pgh-mig-adopt-org ${nsargs[@]+"${nsargs[@]}"} -o json 2>/dev/null | jq -r '.status.atProvider.description // ""')"
  if [ "${got}" = "${org_desc}" ]; then
    record PASS "status.atProvider mirror Organization/pgh-mig-adopt-org .description = the organization's own"
  else
    record FAIL "status.atProvider mirror Organization/pgh-mig-adopt-org .description" "want '${org_desc}', got '${got}'"
  fi
}

# ===========================================================================
# The full adoption flow (scenario (b))
# ===========================================================================
#
#   1. The baseline provider (v0.22.0, built from the tag) creates every fixtures/v1
#      object. Wait until they are Synced and Ready; snapshot GitHub (v1).
#   2. Every v1 managed resource gets deletionPolicy Orphan and is deleted: GitHub
#      must be unchanged (orphaned). The baseline Provider is removed and the
#      candidate installed.
#   3. For every v1 object a NEW cluster-scoped managed resource is derived
#      (adopt-derive.sh): managementPolicies [Observe], the external name of the
#      live baseline object, the create-time fields the candidate lets an
#      Observe-only object omit left out. A drift probe (a declared description that
#      differs from GitHub's) is added. Assert: Synced, Ready, status.atProvider.id
#      against the external name, status.atProvider against the snapshot for every
#      nested sub-object (expect-adopt-nested.tsv), and zero writes (the GitHub
#      snapshot is identical, the provider log shows no create or update).
#   4. The same objects are switched to full management with the v1 forProvider.
#      Assert Synced and Ready (ResourceUpToDate) with zero writes across the poll
#      cycles that follow, then one deliberate spec change per kind
#      (expect-adopt-change.tsv): exactly that change on GitHub, and nothing else.
#
# report.md carries the per-object and per-nested-sub-object tables of steps 3 and 4.

ADOPT_NESTED_TABLE="${MIGRATION_ADOPT_NESTED_TABLE:-${MIGRATION_DIR}/expect-adopt-nested.tsv}"
ADOPT_CHANGE_TABLE="${MIGRATION_ADOPT_CHANGE_TABLE:-${MIGRATION_DIR}/expect-adopt-change.tsv}"

now_utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# mr_atprovider <group> -- {"Kind/name": status.atProvider, ...} of every managed resource.
mr_atprovider() {
  local out
  out="$(kc get "$(resource_list "$1")" -A -o json 2>/dev/null)" || { echo '{}'; return 0; }
  printf '%s' "${out}" | jq -c '[.items[] | {key: "\(.kind)/\(.metadata.name)", value: (.status.atProvider // {})}] | from_entries'
}

# capture_window_logs <label> <since RFC3339> -- the provider log from <since> on.
capture_window_logs() {
  local out="${EVIDENCE_DIR}/window-$1.log" pod
  : >"${out}"
  for pod in $(kc -n "${CROSSPLANE_NS}" get pods -l "pkg.crossplane.io/provider=${PROVIDER_NAME}" -o name 2>/dev/null); do
    kc -n "${CROSSPLANE_NS}" logs "${pod}" --all-containers --since-time="$2" >>"${out}" 2>&1 || true
  done
}

# log_counts <log> <Kind> <name> -- "<creates> <updates> <reconciles>" the provider's
# debug log records for one managed resource. A reconcile is the positive control:
# a count of zero writes means something only if the object was reconciled.
log_counts() {
  local log="$1" lines
  lines="$(grep -F "\"managed/$(printf '%s' "$2" | tr 'A-Z' 'a-z').organizations.github.crossplane.io\"" "${log}" \
    | grep -F "\"request\": {\"name\":\"$3\"}" || true)"
  printf '%s %s %s' \
    "$(printf '%s\n' "${lines}" | grep -c 'Successfully requested creation')" \
    "$(printf '%s\n' "${lines}" | grep -c 'Successfully requested update')" \
    "$(printf '%s\n' "${lines}" | grep -c 'Reconciling')"
}

provider_pods_gone() {
  [ "$(kc -n "${CROSSPLANE_NS}" get pods -l "pkg.crossplane.io/provider=${PROVIDER_NAME}" -o name 2>/dev/null | wc -l)" -eq 0 ]
}

crd_gone() { ! kc get "crd/organizations.${GROUP_CLUSTER}" >/dev/null 2>&1; }

# adopt_settled <group> <count> -- <count> adopted objects, each Synced and Ready; the
# drift probe, when present, only Synced (it is Ready=False by design).
adopt_settled() {
  local state
  state="$(mr_state "$1")"
  [ "$(printf '%s' "${state}" | jq --arg p "${ADOPT_PROBE_NAME}" '[.[] | select(.name != $p)] | length')" -eq "$2" ] || return 1
  [ "$(printf '%s' "${state}" | jq --arg p "${ADOPT_PROBE_NAME}" '[.[] | select(.synced != "True" or (.ready != "True" and .name != $p))] | length')" -eq 0 ]
}

# drop_excluded <dir> -- removes the manifests of the objects the baseline never got Ready.
drop_excluded() {
  local entry kind name
  while IFS= read -r entry; do
    [ -n "${entry}" ] || continue
    kind="${entry%%/*}"
    name="${entry#*/}"
    rm -f "$1"/*-"$(printf '%s' "${kind}" | tr 'A-Z' 'a-z')-${name}.yaml"
  done <"${EVIDENCE_DIR}/v1-excluded.txt"
}

run_adopt_full() {
  local scope="$1"
  [ "${scope}" = cluster ] || die "run_adopt_full: only the cluster scope is derived from the v1 fixtures"

  scenario_begin "adopt-${scope}"
  # The evidence directory is reused between runs: start the tables empty.
  rm -f "${EVIDENCE_DIR}/report-tables.md"
  : >"${EVIDENCE_DIR}/table-mr.tsv"
  : >"${EVIDENCE_DIR}/table-nested.tsv"
  : >"${EVIDENCE_DIR}/table-change.tsv"
  use_candidate_tools
  require_rate_budget
  fetch_baseline
  assert_baseline_crds
  build_tree "${BASELINE_DIR}" "${BASELINE_VERSION_LABEL}"
  build_tree "${MIGRATION_CANDIDATE_DIR}" "${CANDIDATE_VERSION_LABEL}"

  # The v1 fixtures need what the upgrade scenario sets up: the template repository,
  # the Actions policy `selected` and the organization secrets the SecretAccess kinds manage.
  "${MIGRATION_DIR}/oob.sh" prepare upgrade || die "out-of-band setup failed"
  load_runtime_env
  render_fixtures "${FIXTURES_DIR}/v1" "${EVIDENCE_DIR}/rendered/v1"

  cluster_up
  load_package "${BASELINE_DIR}" baseline "${DIGEST_BASELINE}"
  load_package "${MIGRATION_CANDIDATE_DIR}" candidate "${DIGEST_CANDIDATE}"

  adopt_baseline_phase
  adopt_orphan_phase
  adopt_observe_phase
  adopt_full_phase
  adopt_change_phase
  exit 0
}

# --- steps 1 and 2: the baseline creates, the objects are orphaned ----------------

adopt_baseline_phase() {
  install_provider "${BASELINE_DIR}" v1 "${DIGEST_BASELINE}" --debug "--poll=${MIGRATION_POLL}" \
    --enable-management-policies --enable-external-secret-stores
  mapfile -t crds < <(cluster_crds cluster)
  wait_provider "${DIGEST_BASELINE}" "${crds[@]}"
  apply_provider_config cluster
  record PASS "baseline provider ${MIGRATION_BASELINE_REF} is Healthy"

  log "applying the v0.22.0 fixtures"
  apply_fixtures "${EVIDENCE_DIR}/rendered/v1"
  "${MIGRATION_DIR}/oob.sh" ensure-environment pgh-mig-repo-rules pgh-mig-env 900 >>"${EVIDENCE_DIR}/oob-env.log" 2>&1 &
  BACKGROUND_PIDS+=("$!")

  if wait_settled "${GROUP_CLUSTER}" "${MIGRATION_READY_TIMEOUT}"; then
    record PASS "the baseline created every v1 fixture: all Synced and Ready"
  else
    not_settled "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/baseline-not-ready.txt"
    if [ "${MIGRATION_REQUIRE_BASELINE_READY}" = "1" ]; then
      record FAIL "the baseline created every v1 fixture: all Synced and Ready" "$(head -3 "${EVIDENCE_DIR}/baseline-not-ready.txt" | tr '\n' ';')"
      exit 1
    fi
    record WARN "some v1 fixtures are not Ready on the baseline; GitHub does not hold them, so they are left out of the adoption" \
      "$(wc -l <"${EVIDENCE_DIR}/baseline-not-ready.txt") object(s), see baseline-not-ready.txt"
  fi
  # Two cycles are enough for the baseline to finish late initialisation; the zero-write
  # windows of the adoption phases use MIGRATION_SETTLE_POLLS.
  settle_pause 2
  mr_state "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/k8s-v1.json"
  jq -r '.[] | select(.synced != "True" or .ready != "True") | "\(.kind)/\(.name)"' "${EVIDENCE_DIR}/k8s-v1.json" >"${EVIDENCE_DIR}/v1-excluded.txt"
  take_snapshot v1
  capture_provider_logs baseline
}

adopt_orphan_phase() {
  local kind name
  log "orphaning the v0.22.0 managed resources"
  while IFS=$'\t' read -r kind name; do
    kc patch "$(plural_of "${kind}").${GROUP_CLUSTER}" "${name}" --type merge -p '{"spec":{"deletionPolicy":"Orphan"}}' \
      </dev/null >>"${EVIDENCE_DIR}/orphan.log" 2>&1 || warn "could not set deletionPolicy Orphan on ${kind}/${name}"
  done < <(jq -r '.[] | [.kind, .name] | @tsv' "${EVIDENCE_DIR}/k8s-v1.json")
  local not_orphan
  not_orphan="$(mr_state "${GROUP_CLUSTER}" | jq -r '.[] | select(.deletionPolicy != "Orphan") | "\(.kind)/\(.name)"')"
  if [ -n "${not_orphan}" ]; then
    # Deleting these would delete their GitHub objects: stop before any delete.
    record FAIL "every v0.22.0 managed resource carries deletionPolicy Orphan before it is deleted" "$(printf '%s' "${not_orphan}" | head -3 | tr '\n' ';')"
    exit 1
  fi
  record PASS "every v0.22.0 managed resource carries deletionPolicy Orphan before it is deleted"

  delete_managed_resources "${GROUP_CLUSTER}" 600 || record WARN "deleting the v0.22.0 managed resources needed forced finalizer removal"
  take_snapshot orphaned
  assert_snapshots_identical "orphaning writes nothing: GitHub is identical before and after the v0.22.0 managed resources were deleted (IDs, settings, timestamps)" v1 orphaned

  remove_provider
  wait_until 180 3 provider_pods_gone || warn "the baseline provider pods are still present"
  if crd_gone; then
    record INFO "the baseline's CRDs after its Provider was removed" "removed with the Provider"
  elif wait_until 120 3 crd_gone; then
    record INFO "the baseline's CRDs after its Provider was removed" "removed with the Provider (after a delay)"
  else
    record INFO "the baseline's CRDs after its Provider was removed" "kept; the candidate replaces them"
  fi
}

# --- step 3: Observe-only adoption ---------------------------------------------------

adopt_observe_phase() {
  local since
  install_provider "${MIGRATION_CANDIDATE_DIR}" v2 "${DIGEST_CANDIDATE}" --debug "--poll=${MIGRATION_POLL}"
  mapfile -t crds < <(cluster_crds cluster)
  wait_provider "${DIGEST_CANDIDATE}" "${crds[@]}"
  # The baseline's ProviderConfig may have gone with its CRD.
  apply_provider_config cluster
  kc apply -f "${EVIDENCE_DIR}/rendered/v1/00-prerequisites.yaml" >>"${EVIDENCE_DIR}/apply.log" 2>&1 || warn "applying the prerequisite Secrets failed"
  record PASS "the candidate provider is Healthy with the cluster CRDs"

  derive_adoption observe "${EVIDENCE_DIR}/rendered/v1" "${EVIDENCE_DIR}/k8s-v1.json" "${EVIDENCE_DIR}/rendered/observe"
  derive_adoption full "${EVIDENCE_DIR}/rendered/v1" "${EVIDENCE_DIR}/k8s-v1.json" "${EVIDENCE_DIR}/rendered/full"
  drop_excluded "${EVIDENCE_DIR}/rendered/observe"
  drop_excluded "${EVIDENCE_DIR}/rendered/full"
  derive_probe "${EVIDENCE_DIR}/rendered/observe"
  ADOPT_COUNT="$(find "${EVIDENCE_DIR}/rendered/full" -name '*.yaml' | wc -l)"
  [ "${ADOPT_COUNT}" -gt 0 ] || die "no v1 object was Ready on the baseline: nothing to adopt"

  log "applying ${ADOPT_COUNT} Observe-only adoption manifests (and the drift probe)"
  since="$(now_utc)"
  apply_fixtures "${EVIDENCE_DIR}/rendered/observe"
  if wait_until "${MIGRATION_READY_TIMEOUT}" 10 adopt_settled "${GROUP_CLUSTER}" "${ADOPT_COUNT}"; then
    record PASS "every adopted object is Synced and Ready (the drift probe is Synced)"
  else
    record FAIL "every adopted object is Synced and Ready (the drift probe is Synced)" \
      "$(not_settled "${GROUP_CLUSTER}" | head -3 | tr '\n' ';')"
  fi
  settle_pause
  mr_state "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/k8s-observe.json"
  mr_atprovider "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/atprovider-observe.json"
  capture_window_logs observe "${since}"
  capture_provider_logs adopt
  log_findings adopt
  take_snapshot adopted

  assert_snapshots_identical "no write: GitHub is identical before and after Observe-only adoption (IDs, settings, timestamps)" orphaned adopted
  adopt_new_objects
  adopt_check_state observe
  adopt_check_probe
  adopt_check_nested observe
  write_adopt_tables

  # An Observe-only object that is deleted leaves GitHub alone; the probe watched a team
  # that the fully managed objects manage next.
  kc delete "$(plural_of Team).${GROUP_CLUSTER}" "${ADOPT_PROBE_NAME}" --ignore-not-found --wait=true --timeout=120s >/dev/null 2>&1 || true
}

# The adopting objects are new: none carries the UID of a v1 object.
adopt_new_objects() {
  local reused
  reused="$(jq -rn --slurpfile o "${EVIDENCE_DIR}/k8s-observe.json" --slurpfile v "${EVIDENCE_DIR}/k8s-v1.json" '
    ($v[0] | map(.uid)) as $old | $o[0][] | select(.uid as $u | $old | index($u)) | "\(.kind)/\(.name)"')"
  if [ -z "${reused}" ]; then
    record PASS "every adopting managed resource is new (no UID of a v0.22.0 object is reused)"
  else
    record FAIL "every adopting managed resource is new (no UID of a v0.22.0 object is reused)" "$(printf '%s' "${reused}" | head -3 | tr '\n' ';')"
  fi
}

# --- the checks shared by steps 3 and 4 ---------------------------------------------

# adopt_check_state <observe|full> -- reads k8s-<phase>.json, atprovider-<phase>.json,
# window-<phase>.log and the snapshot of the phase (adopted | full); one row per
# managed resource in the per-object table; PASS or FAIL for the aggregate checks.
adopt_check_state() {
  local phase="$1" state snap log kind name ready synced id ext idcheck gid counts creates updates recs
  local bad_state="" bad_id="" bad_writes="" bad_idle="" total=0
  state="${EVIDENCE_DIR}/k8s-${phase}.json"
  log="${EVIDENCE_DIR}/window-${phase}.log"
  snap="${EVIDENCE_DIR}/snapshots/$([ "${phase}" = observe ] && echo adopted || echo "${phase}").json"
  while IFS=$'\t' read -r kind name ready synced id ext; do
    [ "${name}" = "${ADOPT_PROBE_NAME}" ] && continue
    total=$((total + 1))
    case "${kind}" in
      OrganizationWebhook)
        if [ "${id}" = "${ext}" ] && jq -e --argjson i "${id}" '[.orgHooks[] | select(.id == $i)] | length == 1' "${snap}" >/dev/null 2>&1; then
          idcheck="ok: the GitHub hook ID"
        else idcheck="MISMATCH"; fi ;;
      RunnerGroup)
        gid="$(jq -r --arg n "${ext}" '.runnerGroups[$n].id // "-"' "${snap}")"
        if [ "${gid}" != "-" ] && [ "${id}" = "${gid}" ]; then idcheck="ok: the GitHub runner group ID"; else idcheck="MISMATCH (GitHub ${gid})"; fi ;;
      *)
        if [ "${id}" != "-" ] && [ "${id}" = "${ext}" ]; then idcheck="ok: equals the external name"; else idcheck="MISMATCH"; fi ;;
    esac
    counts="$(log_counts "${log}" "${kind}" "${name}")"
    read -r creates updates recs <<<"${counts}"
    [ "${ready}" = True ] && [ "${synced}" = True ] || bad_state+="${kind}/${name} Ready=${ready} Synced=${synced}; "
    case "${idcheck}" in MISMATCH*) bad_id+="${kind}/${name} id=${id} external-name=${ext}; " ;; esac
    { [ "${creates}" -eq 0 ] && [ "${updates}" -eq 0 ]; } || bad_writes+="${kind}/${name} created ${creates} updated ${updates}; "
    [ "${recs}" -gt 0 ] || bad_idle+="${kind}/${name}; "
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${phase}" "${kind}" "${name}" "${ready}" "${synced}" "${id}" "${ext}" \
      "${idcheck}" "${creates}" "${updates}" "${recs}" >>"${EVIDENCE_DIR}/table-mr.tsv"
  done < <(jq -r '.[] | [.kind, .name, .ready, .synced, ((.atProviderId // "-") | tostring), (.externalName | if . == "" then "-" else . end)] | @tsv' "${state}")

  if [ -z "${bad_state}" ] && [ "${total}" -gt 0 ]; then
    record PASS "${phase}: every managed resource is Synced=True and Ready=True (${total} objects)"
  else
    record FAIL "${phase}: every managed resource is Synced=True and Ready=True" "${bad_state:-no managed resource found}"
  fi
  if [ -z "${bad_id}" ]; then
    record PASS "${phase}: status.atProvider.id equals the external name; the webhook and runner group IDs are GitHub's"
  else
    record FAIL "${phase}: status.atProvider.id equals the external name; the webhook and runner group IDs are GitHub's" "${bad_id}"
  fi
  if [ -n "${bad_idle}" ]; then
    record FAIL "${phase}: the provider log shows every managed resource reconciled (positive control for the write counts)" "${bad_idle}"
  else
    record PASS "${phase}: the provider log shows every managed resource reconciled (positive control for the write counts)"
  fi
  if [ -z "${bad_writes}" ]; then
    record PASS "${phase}: no managed resource issued a create or an update (provider debug log)"
  else
    record FAIL "${phase}: no managed resource issued a create or an update (provider debug log)" "${bad_writes}"
  fi
}

# adopt_check_probe -- the drift probe reports the difference and writes nothing.
adopt_check_probe() {
  local state="${EVIDENCE_DIR}/k8s-observe.json" atprov="${EVIDENCE_DIR}/atprovider-observe.json" row want got counts
  row="$(jq -r --arg n "${ADOPT_PROBE_NAME}" '.[] | select(.name == $n) | "Ready=\(.ready) Synced=\(.synced)"' "${state}")"
  if [ -z "${row}" ]; then
    record INFO "drift probe ${ADOPT_PROBE_NAME}" "not adopted (its source team is not in the set)"
    return 0
  fi
  if jq -e --arg n "${ADOPT_PROBE_NAME}" '.[] | select(.name == $n) | .synced == "True" and .ready != "True"' "${state}" >/dev/null; then
    record PASS "the drift probe is Synced and not Ready: an Observe-only object reports a difference it does not write (${row})"
  else
    record FAIL "the drift probe is Synced and not Ready: an Observe-only object reports a difference it does not write" "${row}"
  fi
  want="$(jq -c --arg t "${ADOPT_PROBE_SOURCE#*/}" '.teams[$t].description' "${EVIDENCE_DIR}/snapshots/adopted.json")"
  got="$(jq -c --arg n "Team/${ADOPT_PROBE_NAME}" '.[$n].description' "${atprov}")"
  if [ "${got}" = "${want}" ]; then
    record PASS "the drift probe's status.atProvider.description is GitHub's, not the declared one (${want})"
  else
    record FAIL "the drift probe's status.atProvider.description is GitHub's, not the declared one" "want ${want}, got ${got}"
  fi
  counts="$(log_counts "${EVIDENCE_DIR}/window-observe.log" Team "${ADOPT_PROBE_NAME}")"
  if [ "${counts%% *}" = 0 ] && [ "$(cut -d' ' -f2 <<<"${counts}")" = 0 ]; then
    record PASS "the drift probe issued no update (provider debug log)"
  else
    record FAIL "the drift probe issued no update (provider debug log)" "creates/updates/reconciles: ${counts}"
  fi
}

# adopt_check_nested <observe|full> -- every row of expect-adopt-nested.tsv: what
# status.atProvider reports against what the GitHub snapshot holds.
adopt_check_nested() {
  local phase="$1" atprov snap kind mr rid cond a s av sv verdict detail
  local pass=0 fail=0 nm=0 ex=0 skip=0 not_mirrored=""
  atprov="${EVIDENCE_DIR}/atprovider-${phase}.json"
  snap="${EVIDENCE_DIR}/snapshots/$([ "${phase}" = observe ] && echo adopted || echo "${phase}").json"
  while IFS=$'\t' read -r kind mr rid cond a s; do
    detail=""
    if [ "${a}" = "-" ]; then
      verdict=EXEMPT
      detail="${s}"
    elif ! jq -e --arg k "${kind}/${mr}" 'has($k)' "${atprov}" >/dev/null 2>&1; then
      verdict=SKIPPED
      detail="not adopted: the object was not Ready on the baseline"
    else
      av="$(eval_expr "${a}" "${atprov}" ".[\"${kind}/${mr}\"]")"
      sv="$(eval_expr "${s}" "${snap}")"
      verdict="$(nested_verdict "${cond}" "${phase}" "${av}" "${sv}")"
      case "${verdict}" in
        PASS) detail="${av:0:90}" ;;
        NOT-MIRRORED) detail="visibility is not declared, so the list is not mirrored; GitHub holds ${sv:0:80}" ;;
        *) detail="atProvider ${av:0:100}; GitHub ${sv:0:100}" ;;
      esac
    fi
    case "${verdict}" in
      PASS) pass=$((pass + 1)) ;;
      FAIL) fail=$((fail + 1))
        record FAIL "${phase}: status.atProvider mirrors GitHub: ${kind}/${mr} ${rid#.spec.forProvider}" "${detail}" ;;
      NOT-MIRRORED) nm=$((nm + 1)); not_mirrored+="${kind}/${mr} ${rid#.spec.forProvider}; " ;;
      EXEMPT) ex=$((ex + 1)) ;;
      SKIPPED) skip=$((skip + 1)) ;;
    esac
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "${phase}" "${kind}" "${mr}" "${rid#.spec.forProvider}" "${verdict}" "${detail}" >>"${EVIDENCE_DIR}/table-nested.tsv"
  done < <(expectation_rows "${ADOPT_NESTED_TABLE}")
  if [ "${fail}" -eq 0 ] && [ "${pass}" -gt 0 ]; then
    record PASS "${phase}: status.atProvider mirrors the GitHub snapshot for every nested sub-object and adopted setting (${pass} compared, ${ex} without a GitHub value, ${skip} skipped)"
  elif [ "${fail}" -eq 0 ]; then
    record FAIL "${phase}: status.atProvider mirrors the GitHub snapshot for every nested sub-object and adopted setting" "nothing was compared"
  fi
  [ "${nm}" -eq 0 ] || record INFO "${phase}: lists the observation fills only while the spec declares visibility selected (omitted on an Observe-only object)" "${not_mirrored}"
}

# write_adopt_tables -- the tables of report.md, rewritten after every phase so that a
# run that stops early still reports what it measured.
write_adopt_tables() {
  local out="${EVIDENCE_DIR}/report-tables.md"
  {
    echo
    echo "## Per managed resource: Ready/Synced, atProvider.id against the external name, writes observed"
    echo
    echo "Writes are the creates and updates the provider's debug log records for the object over the phase's window; reconciles is the positive control."
    echo
    echo "| Phase | Kind | Object | Ready | Synced | atProvider.id | external-name | id check | creates | updates | reconciles |"
    echo "|---|---|---|---|---|---|---|---|---|---|---|"
    awk -F'\t' '{ for (i = 1; i <= NF; i++) gsub(/\|/, "\\|", $i); printf "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11 }' "${EVIDENCE_DIR}/table-mr.tsv"
    echo
    echo "## Per nested sub-object: status.atProvider against the GitHub snapshot"
    echo
    echo "| Phase | Kind | Object | Sub-object | Result | Detail |"
    echo "|---|---|---|---|---|---|"
    awk -F'\t' '{ for (i = 1; i <= NF; i++) gsub(/\|/, "\\|", $i); printf "| %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6 }' "${EVIDENCE_DIR}/table-nested.tsv"
    if [ -s "${EVIDENCE_DIR}/table-change.tsv" ]; then
      echo
      echo "## Deliberate change per kind"
      echo
      echo "| Kind | Object | Change | In status.atProvider | On GitHub | updates |"
      echo "|---|---|---|---|---|---|"
      awk -F'\t' '{ for (i = 1; i <= NF; i++) gsub(/\|/, "\\|", $i); printf "| %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6 }' "${EVIDENCE_DIR}/table-change.tsv"
    fi
  } >"${out}"
}

# --- step 4: full management --------------------------------------------------------

adopt_full_phase() {
  local since bad
  log "switching the adopted objects to full management"
  since="$(now_utc)"
  apply_fixtures "${EVIDENCE_DIR}/rendered/full"
  if wait_until "${MIGRATION_READY_TIMEOUT}" 10 adopt_settled "${GROUP_CLUSTER}" "${ADOPT_COUNT}"; then
    record PASS "every object is Synced and Ready under full management"
  else
    record FAIL "every object is Synced and Ready under full management" "$(not_settled "${GROUP_CLUSTER}" | head -3 | tr '\n' ';')"
  fi
  settle_pause
  mr_state "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/k8s-full.json"
  mr_atprovider "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/atprovider-full.json"
  capture_window_logs full "${since}"
  capture_provider_logs full
  log_findings full
  take_snapshot full

  bad="$(jq -r '.[] | select(.managementPolicies != ["*"]) | "\(.kind)/\(.name) \(.managementPolicies)"' "${EVIDENCE_DIR}/k8s-full.json")"
  if [ -z "${bad}" ]; then
    record PASS "every object runs under the default management policies"
  else
    record FAIL "every object runs under the default management policies" "$(printf '%s' "${bad}" | head -3 | tr '\n' ';')"
  fi
  assert_snapshots_identical "full management with a matching forProvider writes nothing: GitHub is identical from adoption through ${MIGRATION_SETTLE_POLLS} poll cycles (IDs, settings, timestamps)" adopted full
  adopt_check_state full
  adopt_check_nested full
  write_adopt_tables
}

# --- step 4: one deliberate change per kind ------------------------------------------

# adopt_changes_reflected -- every patched object reports the change and is Synced and Ready.
adopt_changes_reflected() {
  local atprov kind mr check got
  atprov="$(mr_atprovider "${GROUP_CLUSTER}")"
  [ "$(mr_state "${GROUP_CLUSTER}" | jq '[.[] | select(.synced != "True" or .ready != "True")] | length')" -eq 0 ] || return 1
  while IFS=$'\t' read -r kind mr check; do
    got="$(printf '%s' "${atprov}" | jq -cS ".[\"${kind}/${mr}\"] | (${check})" 2>/dev/null)"
    [ "${got}" = true ] || return 1
  done <"${EVIDENCE_DIR}/changes-applied.tsv"
}

adopt_change_phase() {
  local kind mr patch check allowed required note since applied=0 waived="" skipped="" counts creates updates recs
  local changed_kinds=" " reflected on_github bad_unexpected bad_rewrite bad_other bad_unchanged_writes="" lacking_update=""
  : >"${EVIDENCE_DIR}/changes-applied.tsv"
  : >"${EVIDENCE_DIR}/changes-allowed.txt"
  since="$(now_utc)"
  log "making one deliberate change per kind"
  while IFS=$'\t' read -r kind mr patch check allowed required note; do
    if [ "${patch}" = "-" ]; then
      waived+="${kind}/${mr} (${note}); "
      continue
    fi
    if ! jq -e --arg k "${kind}/${mr}" 'any(.[]; "\(.kind)/\(.name)" == $k)' "${EVIDENCE_DIR}/k8s-full.json" >/dev/null 2>&1; then
      skipped+="${kind}/${mr}; "
      continue
    fi
    if kc patch "$(plural_of "${kind}").${GROUP_CLUSTER}" "${mr}" --type merge -p "${patch}" </dev/null >>"${EVIDENCE_DIR}/change.log" 2>&1; then
      printf '%s\t%s\t%s\n' "${kind}" "${mr}" "${check}" >>"${EVIDENCE_DIR}/changes-applied.tsv"
      printf '%s\n' "${allowed}" >>"${EVIDENCE_DIR}/changes-allowed.txt"
      applied=$((applied + 1))
    else
      record FAIL "the deliberate change to ${kind}/${mr} is accepted by the API server" "$(tail -1 "${EVIDENCE_DIR}/change.log")"
    fi
  done < <(expectation_rows "${ADOPT_CHANGE_TABLE}")
  [ -z "${waived}" ] || record INFO "kinds with no deliberate change" "${waived}"
  [ -z "${skipped}" ] || record INFO "deliberate changes skipped: the object was not adopted" "${skipped}"
  [ "${applied}" -gt 0 ] || { record FAIL "at least one deliberate change was made" "none applied"; return 0; }

  if wait_until 900 10 adopt_changes_reflected; then
    record PASS "every deliberate change is in status.atProvider and every object is Synced and Ready again (ResourceUpToDate)"
  else
    record FAIL "every deliberate change is in status.atProvider and every object is Synced and Ready again (ResourceUpToDate)" \
      "$(not_settled "${GROUP_CLUSTER}" | head -3 | tr '\n' ';')"
  fi
  # Two more cycles: a second write caused by the first would show in the snapshot.
  sleep "$(($(poll_seconds "${MIGRATION_POLL}") * 2 + 15))"
  mr_state "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/k8s-change.json"
  mr_atprovider "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/atprovider-change.json"
  capture_window_logs change "${since}"
  capture_provider_logs change
  take_snapshot changed

  # GitHub: exactly the intended change.
  snapshot_changed_paths "${EVIDENCE_DIR}/snapshots/full.json" "${EVIDENCE_DIR}/snapshots/changed.json" >"${EVIDENCE_DIR}/changed-paths.tsv"
  classify_changes "${EVIDENCE_DIR}/changed-paths.tsv" "${EVIDENCE_DIR}/changes-allowed.txt" >"${EVIDENCE_DIR}/changed-classified.txt"
  bad_unexpected="$(grep '^UNEXPECTED ' "${EVIDENCE_DIR}/changed-classified.txt" | head -5 | cut -d' ' -f2- | tr '\n' ';')"
  bad_rewrite="$(grep '^REWRITE ' "${EVIDENCE_DIR}/changed-classified.txt" | head -5 | cut -d' ' -f2- | tr '\n' ';')"
  if [ -z "${bad_unexpected}" ]; then
    record PASS "the deliberate changes changed nothing else on GitHub ($(wc -l <"${EVIDENCE_DIR}/changed-paths.tsv") values differ, all intended)"
  else
    record FAIL "the deliberate changes changed nothing else on GitHub" "${bad_unexpected}"
  fi
  [ -z "${bad_rewrite}" ] || record WARN "an update rewrote an object whose values did not change (a timestamp moved)" "${bad_rewrite}"

  while IFS=$'\t' read -r kind mr patch check allowed required note; do
    [ "${patch}" != "-" ] || continue
    grep -qxF "${kind}"$'\t'"${mr}"$'\t'"${check}" "${EVIDENCE_DIR}/changes-applied.tsv" || continue
    changed_kinds+="${kind}/${mr} "
    [ "$(eval_expr "${check}" "${EVIDENCE_DIR}/atprovider-change.json" ".[\"${kind}/${mr}\"]")" = true ] && reflected=yes || reflected=no
    if cut -f1 "${EVIDENCE_DIR}/changed-paths.tsv" | grep -Eq "${required}"; then on_github="yes: $(cut -f1 "${EVIDENCE_DIR}/changed-paths.tsv" | grep -E "${required}" | head -1)"; else on_github="NO"; fi
    counts="$(log_counts "${EVIDENCE_DIR}/window-change.log" "${kind}" "${mr}")"
    read -r creates updates recs <<<"${counts}"
    [ "${on_github%%:*}" = yes ] || record FAIL "the deliberate change to ${kind}/${mr} reached GitHub" "no changed value matches ${required}"
    [ "${updates}" -ge 1 ] || lacking_update+="${kind}/${mr}; "
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "${kind}" "${mr}" "${patch:0:90}" "${reflected}" "${on_github}" "${updates}" >>"${EVIDENCE_DIR}/table-change.tsv"
  done < <(expectation_rows "${ADOPT_CHANGE_TABLE}")
  if [ -z "${lacking_update}" ]; then
    record PASS "every changed object issued an update (provider debug log)"
  else
    record FAIL "every changed object issued an update (provider debug log)" "${lacking_update}"
  fi

  # Everything that was not changed stayed quiet.
  while IFS=$'\t' read -r kind mr _; do
    case "${changed_kinds}" in *" ${kind}/${mr} "*) continue ;; esac
    counts="$(log_counts "${EVIDENCE_DIR}/window-change.log" "${kind}" "${mr}")"
    read -r creates updates recs <<<"${counts}"
    { [ "${creates}" -eq 0 ] && [ "${updates}" -eq 0 ]; } || bad_unchanged_writes+="${kind}/${mr} created ${creates} updated ${updates}; "
  done < <(jq -r '.[] | [.kind, .name] | @tsv' "${EVIDENCE_DIR}/k8s-change.json")
  if [ -z "${bad_unchanged_writes}" ]; then
    record PASS "no object that was not changed issued a create or an update"
  else
    record FAIL "no object that was not changed issued a create or an update" "${bad_unchanged_writes}"
  fi
  bad_other="$(jq -r '.[] | select(.synced != "True" or .ready != "True") | "\(.kind)/\(.name) Ready=\(.ready) Synced=\(.synced)"' "${EVIDENCE_DIR}/k8s-change.json" | head -3 | tr '\n' ';')"
  [ -z "${bad_other}" ] || record FAIL "every object is Synced and Ready after the changes settled" "${bad_other}"
  write_adopt_tables
}
