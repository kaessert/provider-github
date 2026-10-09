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
# shellcheck shell=bash

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
