#!/usr/bin/env bash
# test/migration/scenario-upgrade.sh -- scenario (a): in-place upgrade.
#
# A cluster runs the baseline provider (the upstream tag, built from source) and
# manages the full v0.22.0 fixture set against the test organization. The
# Provider is then pointed at the candidate build. The upgrade must not
# recreate and must not write:
#
#   * no managed resource is deleted or recreated (UIDs, external names);
#   * every fixture that was Ready is Ready and Synced again;
#   * the GitHub snapshot taken after the upgrade equals the one taken before
#     it -- same numeric IDs, same settings, same timestamps;
#   * the new status.atProvider.id is populated on the seven kinds that gain it
#     and the numeric IDs of OrganizationWebhook and RunnerGroup are unchanged.
#
# What happens to the fields the move to crossplane-runtime v2 removes
# (spec.publishConnectionDetailsTo, spec.providerRef) is RECORDED, not asserted.
#
# Setup: test organization secrets and the template repository (oob.sh).
# Cleanup: every exit path deletes the managed resources, removes the provider,
# sweeps GitHub and compares it with the baseline snapshot (scenario.sh).
set -uo pipefail

MIGRATION_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scenario.sh
. "${MIGRATION_DIR}/scenario.sh"
# shellcheck source=upgrade-compare.sh
. "${MIGRATION_DIR}/upgrade-compare.sh"

scenario_begin upgrade
upgrade_reset_run_state
use_candidate_tools

# --- build both sides --------------------------------------------------------
fetch_baseline
assert_baseline_crds
build_tree "${BASELINE_DIR}" "${BASELINE_VERSION_LABEL}"
build_tree "${MIGRATION_CANDIDATE_DIR}" "${CANDIDATE_VERSION_LABEL}"

# --- out-of-band setup -------------------------------------------------------
if ! "${MIGRATION_DIR}/oob.sh" prepare upgrade; then
  record FAIL "the out-of-band setup completed (organization snapshot, template repository with a main branch, secrets)" "oob.sh prepare upgrade failed, see the log above"
  exit 1
fi
load_runtime_env
render_fixtures "${FIXTURES_DIR}/v1" "${EVIDENCE_DIR}/rendered/v1"

# --- cluster: baseline -------------------------------------------------------
cluster_up
load_package "${BASELINE_DIR}" baseline "${DIGEST_BASELINE}"
load_package "${MIGRATION_CANDIDATE_DIR}" candidate "${DIGEST_CANDIDATE}"

POLL_FLAG="--poll=${MIGRATION_POLL}"
BASELINE_ARGS=(--debug "${POLL_FLAG}" --enable-management-policies --enable-external-secret-stores)
CANDIDATE_ARGS=(--debug "${POLL_FLAG}")

install_provider "${BASELINE_DIR}" v1 "${DIGEST_BASELINE}" "${BASELINE_ARGS[@]}"
mapfile -t BASELINE_CRDS < <(cluster_crds cluster)
wait_provider "${DIGEST_BASELINE}" "${BASELINE_CRDS[@]}"
apply_provider_config cluster
record PASS "baseline provider ${MIGRATION_BASELINE_REF} is Healthy"

# --- baseline state ----------------------------------------------------------
log "applying the v0.22.0 fixtures"
apply_fixtures "${EVIDENCE_DIR}/rendered/v1"
"${MIGRATION_DIR}/oob.sh" ensure-environment pgh-mig-repo-rules pgh-mig-env 900 >>"${EVIDENCE_DIR}/oob-env.log" 2>&1 &
BACKGROUND_PIDS+=("$!")

# The Repository fixtures that declare branch protection: wait for GitHub to generate their default
# branch and, when v0.22.0 is stuck on the unprotected branch, seed the declared protection (baseline.sh).
baseline_settle_protected_repos "${EVIDENCE_DIR}/rendered/v1" "${GROUP_CLUSTER}" \
  || record WARN "a Repository with declared branch protection is not Synced on the baseline" "see the not-Ready list below"

if wait_settled "${GROUP_CLUSTER}" "${MIGRATION_READY_TIMEOUT}"; then
  record PASS "every v1 fixture is Synced and Ready on the baseline"
else
  not_settled "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/baseline-not-ready.txt"
  if [ "${MIGRATION_REQUIRE_BASELINE_READY}" = "1" ]; then
    record FAIL "every v1 fixture is Synced and Ready on the baseline" "$(head -3 "${EVIDENCE_DIR}/baseline-not-ready.txt" | tr '\n' ';')"
    exit 1
  fi
  record WARN "some v1 fixtures are not Ready on the baseline; they are excluded from the Ready-after-upgrade check" \
    "$(wc -l <"${EVIDENCE_DIR}/baseline-not-ready.txt") object(s), see baseline-not-ready.txt"
fi

settle_pause
mr_state "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/k8s-v1.json"
take_snapshot after-v1

# What the E1-removed fields look like before the upgrade.
secret_exists() { # secret_exists <secret> -- hash of a Secret's data, or "absent"
  local json
  json="$(kc -n "${CROSSPLANE_NS}" get secret "$1" -o json 2>/dev/null)" || { echo absent; return 0; }
  printf '%s' "${json}" | jq -S '.data' | sha256sum | cut -d' ' -f1
}
WCS_BEFORE="$(secret_exists pgh-mig-hook-wcs-conn)"
ESS_BEFORE="$(secret_exists pgh-mig-hook-ess-conn)"
ESSWCS_BEFORE="$(secret_exists pgh-mig-hook-ess-wcs-conn)"
record INFO "connection secrets before the upgrade (writeConnectionSecretToRef / publishConnectionDetailsTo / both)" \
  "wcs=${WCS_BEFORE:0:12} ess=${ESS_BEFORE:0:12} ess-wcs=${ESSWCS_BEFORE:0:12}"
capture_provider_logs baseline

# --- upgrade -----------------------------------------------------------------
log "upgrading the Provider to the candidate build"
if [ "${MIGRATION_KEEP_V1_FLAGS}" = "1" ]; then
  # Keeps the runtime flags of the baseline to record what a user who does not
  # touch their runtime configuration sees.
  install_provider "${MIGRATION_CANDIDATE_DIR}" v2 "${DIGEST_CANDIDATE}" "${BASELINE_ARGS[@]}"
  record INFO "upgrade ran with the baseline runtime flags (MIGRATION_KEEP_V1_FLAGS=1)"
else
  install_provider "${MIGRATION_CANDIDATE_DIR}" v2 "${DIGEST_CANDIDATE}" "${CANDIDATE_ARGS[@]}"
fi
mapfile -t CANDIDATE_CRDS < <(cluster_crds cluster)
wait_provider "${DIGEST_CANDIDATE}" "${CANDIDATE_CRDS[@]}"
record PASS "the Provider upgraded to the candidate and is Healthy"
provider_pod_images >"${EVIDENCE_DIR}/provider-pods-after-upgrade.txt"

if wait_settled "${GROUP_CLUSTER}" "${MIGRATION_READY_TIMEOUT}"; then
  record PASS "every managed resource is Synced and Ready after the upgrade"
else
  not_settled "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/after-upgrade-not-ready.txt"
  record INFO "managed resources not settled after the upgrade" "$(wc -l <"${EVIDENCE_DIR}/after-upgrade-not-ready.txt") object(s), see after-upgrade-not-ready.txt"
fi
settle_pause
mr_state "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/k8s-v2.json"
take_snapshot after-v2
capture_provider_logs candidate
log_findings candidate

# --- assertions --------------------------------------------------------------
# An object the baseline could not apply, or never finished applying, is completed by the
# candidate, which is not a change to an object the baseline had reconciled. Those objects are
# narrowed in the GitHub comparison and reported (upgrade-compare.sh); everything that was Synced
# and Ready on the baseline stays under the strict assertions.
upgrade_prepare_snapshots
assert_snapshots_identical "no recreate and no write: GitHub is identical before and after the upgrade (IDs, settings, timestamps)" "${SNAP_BEFORE}" "${SNAP_AFTER}"
assert_snapshots_identical "no recreate and no setting changed: numeric IDs and settings are identical (timestamps ignored)" "${SNAP_BEFORE}" "${SNAP_AFTER}" --ignore-timestamps

# Where each updated_at move comes from: the provider's create and update lines, or somebody else.
CANDIDATE_LOGS=("${EVIDENCE_DIR}"/provider-candidate-*.log)
[ -f "${CANDIDATE_LOGS[0]}" ] || CANDIDATE_LOGS=()
upgrade_write_attribution "${EVIDENCE_DIR}/snapshots/after-v1.json" "${EVIDENCE_DIR}/snapshots/after-v2.json" \
  "${EVIDENCE_DIR}/k8s-v2.json" "${GROUP_CLUSTER}" "${EVIDENCE_DIR}/report-tables.md" ${CANDIDATE_LOGS[@]+"${CANDIDATE_LOGS[@]}"}
record INFO "updated_at moves across the upgrade: provider writes / not provider writes / unmatched" \
  "${UPGRADE_TS_PROVIDER} / ${UPGRADE_TS_OTHER} / ${UPGRADE_TS_UNMATCHED}, see report.md"

# Kubernetes side. An object that was Ready and reports "drift:" at the instant of the read is
# read again after one poll period; it fails only if it is still not Ready.
COMPARE="$(upgrade_k8s_compare "${EVIDENCE_DIR}/k8s-v1.json" "${EVIDENCE_DIR}/k8s-v2.json")"
PENDING="$(printf '%s\n' "${COMPARE}" | grep -P '\tdrift-pending' | cut -f1 || true)"
COMPARE="$(printf '%s\n' "${COMPARE}" | grep -vP '\tdrift-pending' || true)"
if [ -n "${PENDING}" ]; then
  record INFO "managed resources reporting drift when read; read again after one poll period" "$(printf '%s' "${PENDING}" | paste -sd' ')"
  sleep "$(poll_seconds "${MIGRATION_POLL}")"
  mr_state "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/k8s-v2-reread.json"
  RECHECK="$(awk -F'\t' 'NR == FNR { keys[$0]; next } $1 in keys' <(printf '%s\n' "${PENDING}") \
    <(upgrade_k8s_compare "${EVIDENCE_DIR}/k8s-v1.json" "${EVIDENCE_DIR}/k8s-v2-reread.json" final))"
  COMPARE="$(printf '%s\n%s\n' "${COMPARE}" "${RECHECK}" | sed '/^$/d')"
fi
if [ -z "${COMPARE}" ]; then
  record PASS "no managed resource was deleted, recreated, renamed or regressed by the upgrade"
else
  printf '%s\n' "${COMPARE}" >"${EVIDENCE_DIR}/k8s-regressions.txt"
  record FAIL "no managed resource was deleted, recreated, renamed or regressed by the upgrade" "$(printf '%s' "${COMPARE}" | head -3 | tr '\n' ';')"
fi

# status.atProvider.id on the seven kinds that gain it.
MISSING_ID="$(jq -r '.[] | select(.kind | IN("Organization","Membership","Team","Repository","OrganizationVariable","ActionsSecretAccess","DependabotSecretAccess"))
  | select(.ready == "True") | select(.atProviderId == null or .atProviderId == "") | "\(.kind)/\(.name)"' "${EVIDENCE_DIR}/k8s-v2.json")"
if [ -z "${MISSING_ID}" ]; then
  record PASS "status.atProvider.id is populated on the seven kinds that gain it"
else
  record FAIL "status.atProvider.id is populated on the seven kinds that gain it" "$(printf '%s' "${MISSING_ID}" | head -3 | tr '\n' ';')"
fi

# The numeric IDs of OrganizationWebhook and RunnerGroup are unchanged.
BAD_NUMERIC="$(jq -r --slurpfile s "${EVIDENCE_DIR}/snapshots/after-v1.json" '
  .[] | select(.kind == "RunnerGroup" and .ready == "True")
  | select(.atProviderId != ($s[0].runnerGroups[.externalName].id)) | "RunnerGroup/\(.name)"' "${EVIDENCE_DIR}/k8s-v2.json")"
if [ -z "${BAD_NUMERIC}" ]; then
  record PASS "RunnerGroup status.atProvider.id still equals the GitHub runner group ID"
else
  record FAIL "RunnerGroup status.atProvider.id still equals the GitHub runner group ID" "${BAD_NUMERIC}"
fi
BAD_HOOKS="$(jq -r --slurpfile s "${EVIDENCE_DIR}/snapshots/after-v1.json" '
  .[] | select(.kind == "OrganizationWebhook" and .ready == "True")
  | select(.atProviderId != (.externalName | tonumber)) | "OrganizationWebhook/\(.name)"' "${EVIDENCE_DIR}/k8s-v2.json")"
if [ -z "${BAD_HOOKS}" ]; then
  record PASS "OrganizationWebhook status.atProvider.id still equals its external name (the GitHub hook ID)"
else
  record FAIL "OrganizationWebhook status.atProvider.id still equals its external name (the GitHub hook ID)" "${BAD_HOOKS}"
fi

# The connection secret the webhook publishes through writeConnectionSecretToRef.
WCS_AFTER="$(secret_exists pgh-mig-hook-wcs-conn)"
if [ "${WCS_BEFORE}" != "absent" ] && [ "${WCS_BEFORE}" = "${WCS_AFTER}" ]; then
  record PASS "the writeConnectionSecretToRef secret is unchanged by the upgrade"
elif [ "${WCS_BEFORE}" = "absent" ]; then
  record WARN "the writeConnectionSecretToRef secret was never published on the baseline"
else
  record FAIL "the writeConnectionSecretToRef secret is unchanged by the upgrade" "before ${WCS_BEFORE:0:12}, after ${WCS_AFTER:0:12}"
fi

# --- recorded, not asserted: what happens to the removed fields -----------------
ESS_AFTER="$(secret_exists pgh-mig-hook-ess-conn)"
ESSWCS_AFTER="$(secret_exists pgh-mig-hook-ess-wcs-conn)"
record INFO "publishConnectionDetailsTo: the externally published secret" "before=${ESS_BEFORE:0:12} after=${ESS_AFTER:0:12}"
record INFO "publishConnectionDetailsTo object's own writeConnectionSecretToRef secret" "before=${ESSWCS_BEFORE:0:12} after=${ESSWCS_AFTER:0:12}"
for entry in "OrganizationWebhook/pgh-mig-hook-ess/publishConnectionDetailsTo" "OrganizationVariable/pgh-mig-var-providerref/providerRef"; do
  kind="${entry%%/*}"; rest="${entry#*/}"; name="${rest%%/*}"; field="${rest#*/}"
  # shellcheck disable=SC2016
  stored="$(kc get "$(plural_of "${kind}").${GROUP_CLUSTER}" "${name}" -o json 2>/dev/null | jq -c --arg f "${field}" '.spec[$f] // "absent"')"
  state="$(jq -r --arg n "${name}" '.[] | select(.name == $n) | "Ready=\(.ready) Synced=\(.synced) \(.syncedMessage)"' "${EVIDENCE_DIR}/k8s-v2.json")"
  record INFO "${kind}/${name}: spec.${field} after the upgrade is ${stored:0:80}" "${state}"
done
for name in pgh-mig-var-policies pgh-mig-team-orphan pgh-mig-hook-wcs; do
  state="$(jq -r --arg n "${name}" '.[] | select(.name == $n) | "Ready=\(.ready) Synced=\(.synced) managementPolicies=\(.managementPolicies) deletionPolicy=\(.deletionPolicy)"' "${EVIDENCE_DIR}/k8s-v2.json")"
  record INFO "lifecycle fields kept: ${name}" "${state}"
done

# Delete the managed resources while the candidate still runs, then let the
# cleanup sweep whatever the lifecycle policies deliberately left behind.
delete_managed_resources "${GROUP_CLUSTER}" 600 || record WARN "deleting the managed resources needed forced finalizer removal"
take_snapshot after-delete
LEFTOVER="$(jq -r '[(.repos | keys[]), (.teams | keys[]), (.variables | keys[]), (.runnerGroups | keys[]), (.orgHooks | keys[]), (.secrets.actions | keys[] | select(startswith("PGH_MIG_ORG") | not)), (.secrets.dependabot | keys[] | select(startswith("PGH_MIG_ORG") | not))] | length' "${EVIDENCE_DIR}/snapshots/after-delete.json")"
record INFO "GitHub objects left after the managed resources were deleted (lifecycle policies and the OOB secrets)" "${LEFTOVER}"

exit 0
