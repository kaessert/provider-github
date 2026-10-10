#!/usr/bin/env bash
# test/migration/adopt-common.sh -- the body shared by scenarios (b) and (c), the full
# adoption flow. Sourced by scenario-adopt-cluster.sh and scenario-adopt-namespaced.sh;
# not executable on its own. run_adopt_full <cluster|namespaced> is the entry point.
#
# shellcheck shell=bash

# shellcheck source=adopt-derive.sh
. "${MIGRATION_DIR}/adopt-derive.sh"

# ===========================================================================
# The full adoption flow (scenarios (b) and (c))
# ===========================================================================
#
#   1. The baseline provider (v0.22.0, built from the tag) creates every fixtures/v1
#      object. Wait until they are Synced and Ready; snapshot GitHub (v1).
#   2. Every v1 managed resource gets deletionPolicy Orphan and is deleted: GitHub
#      must be unchanged (orphaned). The baseline Provider is removed and the
#      candidate installed.
#   3. For every v1 object a NEW managed resource of the scope under test is derived
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
# The namespaced scope (c) runs the same steps, and differs in three ways:
#
#   * its objects are spread over two namespaces, one reaching GitHub through a
#     namespaced ProviderConfig (credentials in that namespace) and one through a
#     ClusterProviderConfig (credentials in the Crossplane namespace), see adopt_target;
#   * between steps 3 and 4 two probes run over the Observe-only objects: Observe-only
#     twins that hold their references as Ref/Selector fields (same-namespace references
#     must resolve, a reference to an object of another namespace must not), and the
#     cluster-scoped Observe-only twin of one object per kind (what two scopes do when
#     they observe one GitHub object);
#   * after step 4 one Team is put under full management by a cluster-scoped and a
#     namespaced object at once (does anything stop both from writing).
#
# report.md carries the per-object, per-nested-sub-object and per-ProviderConfig-path
# tables, and for the namespaced scope the reference and both-scopes sections.

# ADOPT_SCOPE and ADOPT_GROUP are set by run_adopt_full: the scope under test and the API
# group of its managed resources.
ADOPT_SCOPE=cluster
ADOPT_GROUP="${GROUP_CLUSTER}"

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

# log_counts <log> <Kind> <name> [group] -- "<creates> <updates> <reconciles>" the provider's
# debug log records for one managed resource of the group (default: the scope under test).
# A reconcile is the positive control: a count of zero writes means something only if the
# object was reconciled. The controller is named managed/<kind>.<group>; the request carries
# the object's name (and, for a namespaced object, its namespace, in either order).
log_counts() {
  local log="$1" group="${4:-${ADOPT_GROUP}}" lines
  lines="$(grep -F "\"managed/$(printf '%s' "$2" | tr 'A-Z' 'a-z').${group}\"" "${log}" \
    | grep -E "\"request\": *\{[^}]*\"name\": *\"$3\"" || true)"
  printf '%s %s %s' \
    "$(printf '%s\n' "${lines}" | grep -c 'Successfully requested creation')" \
    "$(printf '%s\n' "${lines}" | grep -c 'Successfully requested update')" \
    "$(printf '%s\n' "${lines}" | grep -c 'Reconciling')"
}

provider_pods_gone() {
  [ "$(kc -n "${CROSSPLANE_NS}" get pods -l "pkg.crossplane.io/provider=${PROVIDER_NAME}" -o name 2>/dev/null | wc -l)" -eq 0 ]
}

# baseline_crds -- every CRD the baseline package installs: the cluster-scoped kinds, the
# ProviderConfig, and the ProviderConfigUsage and StoreConfig kinds crossplane-runtime adds.
baseline_crds() {
  cluster_crds cluster
  echo "providerconfigusages.github.crossplane.io"
  echo "storeconfigs.github.crossplane.io"
}

# baseline_crds_left -- the CRDs of the baseline package that are still in the cluster.
baseline_crds_left() {
  local crd
  for crd in $(baseline_crds); do
    ! kc get "crd/${crd}" >/dev/null 2>&1 || echo "${crd}"
  done
}

baseline_crds_gone() { [ -z "$(baseline_crds_left)" ]; }

baseline_provider_configs_gone() { [ "$(kc get providerconfigs.github.crossplane.io -o name 2>/dev/null | wc -l)" -eq 0 ]; }

# delete_baseline_provider_configs -- deletes the ProviderConfig objects of the baseline while
# its Provider still runs: a ProviderConfig carries the provider's in-use finalizer, and with
# the Provider gone nothing clears it. The CRD of a kind that still has an instance is kept,
# and its owner, the old package revision, then keeps control of it: the next Provider cannot
# take it over. The credentials Secret stays.
delete_baseline_provider_configs() {
  kc delete providerconfigs.github.crossplane.io --all --wait=false >/dev/null 2>&1 || true
  if wait_until 120 3 baseline_provider_configs_gone; then
    record PASS "the baseline's ProviderConfig objects are deleted while its Provider still runs"
    return 0
  fi
  record WARN "the baseline's ProviderConfig objects did not go while its Provider ran; their finalizers are removed" \
    "$(kc get providerconfigs.github.crossplane.io -o name 2>/dev/null | tr '\n' ' ')"
  kc get providerconfigs.github.crossplane.io -o name 2>/dev/null | while read -r res; do
    kc patch "${res}" --type merge -p '{"metadata":{"finalizers":null}}' >/dev/null 2>&1 || true
  done
}

# remove_baseline_provider -- what a user does to move from the baseline to another build:
# delete the ProviderConfig objects, uninstall the Provider, and wait until EVERY CRD of the
# baseline package is gone. A CRD that stays is a failure naming it: the next install cannot
# become Healthy while the old revision controls it.
remove_baseline_provider() {
  local left crd
  delete_baseline_provider_configs
  remove_provider
  wait_until 180 3 provider_pods_gone || warn "the baseline provider pods are still present"
  if wait_until 120 3 baseline_crds_gone; then
    record PASS "every CRD of the baseline package is gone after its Provider was removed"
    return 0
  fi
  left="$(baseline_crds_left | tr '\n' ' ')"
  for crd in ${left}; do
    kc get "crd/${crd}" -o yaml >"${EVIDENCE_DIR}/baseline-crd-left-${crd}.yaml" 2>&1 || true
  done
  record FAIL "every CRD of the baseline package is gone after its Provider was removed" "still present: ${left}"
  exit 1
}

# adopt_settled <group> <count> -- <count> adopted objects, each Synced and Ready; the
# drift probe, when present, only Synced (it is Ready=False by design). The helper objects
# of the namespaced scope (ADOPT_AUX_RE) are not counted.
adopt_settled() {
  local state
  state="$(mr_state "$1")"
  [ "$(printf '%s' "${state}" | jq --arg a "${ADOPT_AUX_RE}" '[.[] | select(.name | test($a) | not)] | length')" -eq "$2" ] || return 1
  [ "$(printf '%s' "${state}" | jq --arg p "${ADOPT_PROBE_NAME}" '[.[] | select(.synced != "True" or (.ready != "True" and .name != $p))] | length')" -eq 0 ]
}

# baseline_excluded <k8s-v1.json> -- "Kind/name" of the objects the baseline never created on
# GitHub: not Synced. An object that is Synced but not Ready exists on GitHub (the baseline
# created it and reports a problem with it) and is adopted like the others.
baseline_excluded() {
  jq -r '.[] | select(.synced != "True") | "\(.kind)/\(.name)"' "$1"
}

# baseline_not_ready <k8s-v1.json> -- "Kind/name: Ready=.. Synced=.." of the adopted objects
# the baseline did not report Ready.
baseline_not_ready() {
  jq -r '.[] | select(.synced == "True" and .ready != "True") | "\(.kind)/\(.name): Ready=\(.ready) Synced=\(.synced)"' "$1"
}

# baseline_state <k8s-v1.json> <Kind> <name> -- "Ready=.. Synced=.." the baseline reported for
# an object, "-" for one that was not a v1 object.
baseline_state() {
  jq -r --arg k "$2" --arg n "$3" '[.[] | select(.kind == $k and .name == $n)][0]
    | if . == null then "-" else "Ready=\(.ready) Synced=\(.synced)" end' "$1" 2>/dev/null || echo "-"
}

# drop_excluded <dir> [list] -- removes the manifests of the objects the baseline never
# created (v1-excluded.txt, or the list given).
drop_excluded() {
  local entry kind name
  while IFS= read -r entry; do
    [ -n "${entry}" ] || continue
    kind="${entry%%/*}"
    name="${entry#*/}"
    rm -f "$1"/*-"$(printf '%s' "${kind}" | tr 'A-Z' 'a-z')-${name}.yaml"
  done <"${2:-${EVIDENCE_DIR}/v1-excluded.txt}"
}

run_adopt_full() {
  local scope="$1" t
  case "${scope}" in
    cluster) ADOPT_GROUP="${GROUP_CLUSTER}" ;;
    namespaced) ADOPT_GROUP="${GROUP_NAMESPACED}" ;;
    *) die "run_adopt_full: unknown scope ${scope}" ;;
  esac
  ADOPT_SCOPE="${scope}"

  scenario_begin "adopt-${scope}"
  # The evidence directory is reused between runs: start the tables empty.
  rm -f "${EVIDENCE_DIR}/report-tables.md" "${EVIDENCE_DIR}"/both-*-verdict.txt "${EVIDENCE_DIR}/changes-echo.tsv"
  for t in mr nested change refs both notready; do : >"${EVIDENCE_DIR}/table-${t}.tsv"; done
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
  if [ "${scope}" = namespaced ]; then
    adopt_refs_phase
    adopt_bothscopes_observe_phase
  fi
  adopt_full_phase
  adopt_change_phase
  [ "${scope}" != namespaced ] || adopt_bothscopes_write_phase
  exit 0
}

# adopt_ns_args <Kind> <name> -- "-n" and the namespace of a derived object of the adopted
# set, one per line (for mapfile); nothing in the cluster scope.
adopt_ns_args() {
  local t
  [ "${ADOPT_SCOPE}" = namespaced ] || return 0
  t="$(adopt_target "$1" "$2")"
  printf -- '-n\n%s\n' "${t%% *}"
}

# adopt_path_of <state.json> <Kind> <name> -- the ProviderConfig path an object reaches
# GitHub through: its namespace and the kind of ProviderConfig it names ("cluster" for a
# cluster-scoped object).
adopt_path_of() {
  jq -r --arg k "$2" --arg n "$3" '[.[] | select(.kind == $k and .name == $n)][0]
    | if . == null then "-" elif .namespace == "" then "cluster" else "\(.namespace) (\(.providerConfigKind))" end' "$1" 2>/dev/null || echo "-"
}

# mr_condition <resource> <name> <namespace|-> <condition type> -- the status of a condition
# of one managed resource, Unknown when there is none.
mr_condition() {
  local nsa=() out
  [ "$3" = - ] || nsa=(-n "$3")
  out="$(kc get "$1" "$2" ${nsa[@]+"${nsa[@]}"} -o json 2>/dev/null | jq -r --arg t "$4" '[.status.conditions[]? | select(.type == $t) | .status][0] // "Unknown"' 2>/dev/null)"
  printf '%s' "${out:-Unknown}"
}

# delete_mr <Kind> <group> <name> [namespace] -- deletes one managed resource and waits;
# when the wait runs out, removes its finalizers (the caller sweeps GitHub).
delete_mr() {
  local nsa=() res
  [ -z "${4:-}" ] || nsa=(-n "$4")
  res="$(plural_of "$1").$2"
  kc delete "${res}" "$3" ${nsa[@]+"${nsa[@]}"} --ignore-not-found --wait=true --timeout=120s >/dev/null 2>&1 \
    || kc patch "${res}" "$3" ${nsa[@]+"${nsa[@]}"} --type merge -p '{"metadata":{"finalizers":null}}' >/dev/null 2>&1 || true
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
    record WARN "some v1 fixtures are not Synced and Ready on the baseline; those that are not Synced were never created, and are left out of the adoption" \
      "$(wc -l <"${EVIDENCE_DIR}/baseline-not-ready.txt") object(s), see baseline-not-ready.txt"
  fi
  # Two cycles are enough for the baseline to finish late initialisation; the zero-write
  # windows of the adoption phases use MIGRATION_SETTLE_POLLS.
  settle_pause 2
  mr_state "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/k8s-v1.json"
  baseline_excluded "${EVIDENCE_DIR}/k8s-v1.json" >"${EVIDENCE_DIR}/v1-excluded.txt"
  baseline_not_ready "${EVIDENCE_DIR}/k8s-v1.json" >"${EVIDENCE_DIR}/v1-synced-not-ready.txt"
  if [ -s "${EVIDENCE_DIR}/v1-synced-not-ready.txt" ]; then
    record WARN "v1 objects the baseline created but reports not Ready; they exist on GitHub and are adopted like the others" \
      "$(tr '\n' ';' <"${EVIDENCE_DIR}/v1-synced-not-ready.txt")"
  fi
  take_snapshot v1
  capture_provider_logs baseline
  # The connection secrets the baseline wrote (the webhook secrets it applied are recorded in
  # them) go when their owners are deleted: keep them, the namespaced objects start from them.
  kc -n "${CROSSPLANE_NS}" get secret -o json 2>/dev/null \
    | jq -c '[.items[] | select(.metadata.name | test("^pgh-mig-.*-conn$")) | {apiVersion, kind, type, data, metadata: {name: .metadata.name}}]' \
    >"${EVIDENCE_DIR}/baseline-conn-secrets.json" || echo '[]' >"${EVIDENCE_DIR}/baseline-conn-secrets.json"
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

  remove_baseline_provider
}

# --- step 3: Observe-only adoption ---------------------------------------------------

adopt_observe_phase() {
  local since crd_scope=cluster
  [ "${ADOPT_SCOPE}" = namespaced ] && crd_scope=all
  install_provider "${MIGRATION_CANDIDATE_DIR}" v2 "${DIGEST_CANDIDATE}" --debug "--poll=${MIGRATION_POLL}"
  mapfile -t crds < <(cluster_crds "${crd_scope}")
  wait_provider "${DIGEST_CANDIDATE}" "${crds[@]}"
  # The baseline's ProviderConfig was deleted before its Provider was removed.
  apply_provider_config cluster
  kc apply -f "${EVIDENCE_DIR}/rendered/v1/00-prerequisites.yaml" >>"${EVIDENCE_DIR}/apply.log" 2>&1 || warn "applying the prerequisite Secrets failed"
  if [ "${ADOPT_SCOPE}" = namespaced ]; then
    apply_namespaced_provider_configs "${ADOPT_NS_A}" "${ADOPT_NS_B}" "${ADOPT_PC_NAME}" "${ADOPT_CPC_NAME}"
    record PASS "the candidate provider is Healthy with the cluster and the namespaced CRDs; ProviderConfig ${ADOPT_PC_NAME} (namespace ${ADOPT_NS_A}) and ClusterProviderConfig ${ADOPT_CPC_NAME} (for ${ADOPT_NS_B}) are applied"
  else
    record PASS "the candidate provider is Healthy with the cluster CRDs"
  fi

  derive_adoption observe "${EVIDENCE_DIR}/rendered/v1" "${EVIDENCE_DIR}/k8s-v1.json" "${EVIDENCE_DIR}/rendered/observe" "${ADOPT_SCOPE}"
  derive_adoption full "${EVIDENCE_DIR}/rendered/v1" "${EVIDENCE_DIR}/k8s-v1.json" "${EVIDENCE_DIR}/rendered/full" "${ADOPT_SCOPE}"
  drop_excluded "${EVIDENCE_DIR}/rendered/observe"
  drop_excluded "${EVIDENCE_DIR}/rendered/full"
  derive_probe "${EVIDENCE_DIR}/rendered/observe"
  ADOPT_COUNT="$(find "${EVIDENCE_DIR}/rendered/full" -name '*.yaml' | wc -l)"
  [ "${ADOPT_COUNT}" -gt 0 ] || die "no v1 object was Synced on the baseline: nothing to adopt"
  [ "${ADOPT_SCOPE}" != namespaced ] || adopt_prepare_namespaces
  adopt_restore_conn_secrets "${ADOPT_SCOPE}" "${EVIDENCE_DIR}/rendered/observe"

  log "applying ${ADOPT_COUNT} Observe-only adoption manifests (and the drift probe)"
  since="$(now_utc)"
  apply_fixtures "${EVIDENCE_DIR}/rendered/observe"
  if wait_until "${MIGRATION_READY_TIMEOUT}" 10 adopt_settled "${ADOPT_GROUP}" "${ADOPT_COUNT}"; then
    record PASS "every adopted object is Synced and Ready (the drift probe is Synced)"
  else
    record FAIL "every adopted object is Synced and Ready (the drift probe is Synced)" \
      "$(not_settled "${ADOPT_GROUP}" | head -3 | tr '\n' ';')"
  fi
  settle_pause
  mr_state "${ADOPT_GROUP}" >"${EVIDENCE_DIR}/k8s-observe.json"
  mr_atprovider "${ADOPT_GROUP}" >"${EVIDENCE_DIR}/atprovider-observe.json"
  capture_window_logs observe "${since}"
  capture_provider_logs adopt
  log_findings adopt
  take_snapshot adopted

  assert_snapshots_identical "no write: GitHub is identical before and after Observe-only adoption (IDs, settings, timestamps)" orphaned adopted
  adopt_new_objects
  [ "${ADOPT_SCOPE}" != namespaced ] || adopt_check_paths
  adopt_check_state observe
  adopt_check_probe
  adopt_check_nested observe
  write_adopt_tables

  # An Observe-only object that is deleted leaves GitHub alone; the probe watched a team
  # that the fully managed objects manage next.
  mapfile -t nsargs < <(adopt_ns_args Team "${ADOPT_PROBE_NAME}")
  kc delete "$(plural_of Team).${ADOPT_GROUP}" "${ADOPT_PROBE_NAME}" ${nsargs[@]+"${nsargs[@]}"} --ignore-not-found --wait=true --timeout=120s >/dev/null 2>&1 || true
}

# adopt_prepare_namespaces -- what the namespaced objects read from their own namespace: the
# namespaces and the Secret the webhook fixtures name.
adopt_prepare_namespaces() {
  local ns
  for ns in "${ADOPT_NS_A}" "${ADOPT_NS_B}"; do
    kc create namespace "${ns}" --dry-run=client -o yaml | kc apply -f - >/dev/null
    yq "select(.metadata.name == \"pgh-mig-hook-secret\") | .metadata.namespace = \"${ns}\"" "${EVIDENCE_DIR}/rendered/v1/00-prerequisites.yaml" \
      | kc apply -f - >>"${EVIDENCE_DIR}/apply.log" 2>&1 || warn "applying the webhook Secret in ${ns} failed"
  done
  log "prepared ${ADOPT_NS_A} and ${ADOPT_NS_B}: the webhook Secret"
}

# adopt_restore_conn_secrets <cluster|namespaced> <manifest dir> -- the connection secrets the
# baseline wrote, put back for the objects that name one through writeConnectionSecretToRef. A webhook
# that holds its secret in secretKeyRef cannot read it back from GitHub (it is masked), so the
# controller compares against the applied value recorded in the object's connection secret; the
# baseline's secrets are owned by its objects and go with them (baseline-conn-secrets.json keeps
# them), which models a user who keeps the applied-secret records. The Secret goes where the new
# object reads it: its own namespace for a namespaced object (an empty one when the baseline wrote
# none), the namespace named in writeConnectionSecretToRef for a cluster-scoped one (nothing when the
# baseline wrote none). A manifest without writeConnectionSecretToRef restores nothing.
adopt_restore_conn_secrets() {
  local scope="$1" dir="$2" f ns cname n=0 secrets="${EVIDENCE_DIR}/baseline-conn-secrets.json" have
  for f in "${dir}"/*.yaml; do
    [ -e "${f}" ] || continue
    read -r ns cname < <(yq -o=json -I=0 '.' "${f}" | jq -r --arg cns "${CROSSPLANE_NS}" '
      [((.spec.writeConnectionSecretToRef.namespace // .metadata.namespace // $cns) | if . == "" then "-" else . end),
       (.spec.writeConnectionSecretToRef.name // "-")] | join(" ")')
    [ "${cname}" != "-" ] || continue
    have=no
    jq -e --arg n "${cname}" 'any(.[]; .metadata.name == $n)' "${secrets}" >/dev/null 2>&1 && have=yes
    if [ "${have}" = yes ]; then
      jq -c --arg n "${cname}" --arg ns "${ns}" '.[] | select(.metadata.name == $n) | .metadata.namespace = $ns' "${secrets}" \
        | kc apply -f - >>"${EVIDENCE_DIR}/apply.log" 2>&1 || warn "restoring the connection secret ${cname} in ${ns} failed"
    elif [ "${scope}" = namespaced ]; then
      kc apply -f - >>"${EVIDENCE_DIR}/apply.log" 2>&1 <<YAML || warn "creating the connection secret ${cname} in ${ns} failed"
apiVersion: v1
kind: Secret
metadata:
  name: ${cname}
  namespace: ${ns}
type: connection.crossplane.io/v1alpha1
YAML
    else
      continue
    fi
    n=$((n + 1))
  done
  log "restored ${n} connection secret(s) of the baseline for the ${scope} objects"
}

# adopt_check_paths -- every object is on the ProviderConfig path adopt_target gives it, and
# both paths carry objects.
adopt_check_paths() {
  local kind name ns pckind bad="" wns wpck a=0 b=0 state="${EVIDENCE_DIR}/k8s-observe.json"
  while IFS=$'\t' read -r kind name ns pckind; do
    read -r wns wpck _ <<<"$(adopt_target "${kind}" "${name}")"
    { [ "${ns}" = "${wns}" ] && [ "${pckind}" = "${wpck}" ]; } || bad+="${kind}/${name} is in ${ns} through ${pckind}; "
    [ "${ns}" = "${ADOPT_NS_A}" ] && a=$((a + 1))
    [ "${ns}" = "${ADOPT_NS_B}" ] && b=$((b + 1))
  done < <(jq -r --arg a "${ADOPT_AUX_RE}" '.[] | select(.name | test($a) | not) | [.kind, .name, .namespace, .providerConfigKind] | @tsv' "${state}")
  if [ -z "${bad}" ] && [ "${a}" -gt 0 ] && [ "${b}" -gt 0 ]; then
    record PASS "the objects reach GitHub through both ProviderConfig paths: ${a} in ${ADOPT_NS_A} through a ProviderConfig, ${b} in ${ADOPT_NS_B} through a ClusterProviderConfig"
  else
    record FAIL "the objects reach GitHub through both ProviderConfig paths" "${bad:-${ADOPT_NS_A}: ${a} objects, ${ADOPT_NS_B}: ${b} objects}"
  fi
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
  local phase="$1" state snap log kind name path ready synced id ext rmsg idcheck gid counts creates updates recs p total_p bad_p
  local bad_state="" bad_id="" bad_writes="" bad_idle="" total=0
  state="${EVIDENCE_DIR}/k8s-${phase}.json"
  log="${EVIDENCE_DIR}/window-${phase}.log"
  snap="${EVIDENCE_DIR}/snapshots/$([ "${phase}" = observe ] && echo adopted || echo "${phase}").json"
  adopt_note_not_ready "${phase}" "${state}"
  while IFS=$'\t' read -r kind name path ready synced id ext rmsg; do
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
    if [ "${ready}" = True ] && [ "${synced}" = True ]; then
      :
    else
      bad_state+="${kind}/${name} Ready=${ready} Synced=${synced}${rmsg:+ (${rmsg:0:100})}; "
    fi
    case "${idcheck}" in MISMATCH*) bad_id+="${kind}/${name} id=${id} external-name=${ext}; " ;; esac
    if [ "${creates}" -eq 0 ] && [ "${updates}" -eq 0 ]; then
      :
    else
      bad_writes+="${kind}/${name} created ${creates} updated ${updates}; "
    fi
    [ "${recs}" -gt 0 ] || bad_idle+="${kind}/${name}; "
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${phase}" "${path}" "${kind}" "${name}" "${ready}" "${synced}" "${id}" "${ext}" \
      "${idcheck}" "${creates}" "${updates}" "${recs}" "$(baseline_state "${EVIDENCE_DIR}/k8s-v1.json" "${kind}" "${name}")" >>"${EVIDENCE_DIR}/table-mr.tsv"
  done < <(jq -r '.[] | [.kind, .name, (if .namespace == "" then "cluster" else "\(.namespace) (\(.providerConfigKind))" end), .ready, .synced, ((.atProviderId // "-") | tostring), (.externalName | if . == "" then "-" else . end), ((.readyMessage // "") | gsub("[\t\n]"; " "))] | @tsv' "${state}")

  if [ -z "${bad_state}" ] && [ "${total}" -gt 0 ]; then
    record PASS "${phase}: every managed resource is Synced=True and Ready=True (${total} objects)"
  else
    record FAIL "${phase}: every managed resource is Synced=True and Ready=True" "${bad_state:-no managed resource found}"
  fi
  if [ "${ADOPT_SCOPE}" = namespaced ]; then
    # Synced and Ready through each ProviderConfig path.
    while IFS=$'\t' read -r p total_p bad_p; do
      if [ "${bad_p}" -eq 0 ]; then
        record PASS "${phase}: every object that reaches GitHub through ${p} is Synced and Ready (${total_p} objects)"
      else
        record FAIL "${phase}: every object that reaches GitHub through ${p} is Synced and Ready" "${bad_p} of ${total_p} objects are not"
      fi
    done < <(jq -r --arg a "${ADOPT_AUX_RE}" '[.[] | select(.name | test($a) | not)] | group_by("\(.namespace) (\(.providerConfigKind))")[]
      | [(.[0] | "\(.namespace) (\(.providerConfigKind))"), length, ([.[] | select(.synced != "True" or .ready != "True")] | length)] | @tsv' "${state}")
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

# adopt_note_not_ready <phase> <state.json> -- one row in table-notready.tsv for every object that is
# not Ready: why it says so (the Ready condition's reason and message, and the Synced message), which
# report.md prints in a table of its own.
adopt_note_not_ready() {
  jq -r --arg phase "$1" '.[] | select(.ready != "True")
    | [$phase, (if .namespace == "" then "cluster" else "\(.namespace) (\(.providerConfigKind))" end), .kind, .name,
       .ready, ((.readyReason // "") | if . == "" then "-" else . end), .synced, ((.readyMessage // "") | if . == "" then "-" else . end),
       ((.syncedMessage // "") | if . == "" then "-" else . end)]
    | map(tostring | gsub("[\t\n]"; " ")) | join("\t")' "$2" >>"${EVIDENCE_DIR}/table-notready.tsv"
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
  local phase="$1" atprov snap kind mr rid cond a s av sv verdict detail state path
  local pass=0 fail=0 nm=0 ex=0 skip=0 not_mirrored=""
  atprov="${EVIDENCE_DIR}/atprovider-${phase}.json"
  state="${EVIDENCE_DIR}/k8s-${phase}.json"
  snap="${EVIDENCE_DIR}/snapshots/$([ "${phase}" = observe ] && echo adopted || echo "${phase}").json"
  while IFS=$'\t' read -r kind mr rid cond a s; do
    detail=""
    path="$(adopt_path_of "${state}" "${kind}" "${mr}")"
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
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${phase}" "${path}" "${kind}" "${mr}" "${rid#.spec.forProvider}" "${verdict}" "${detail}" >>"${EVIDENCE_DIR}/table-nested.tsv"
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
  local out="${EVIDENCE_DIR}/report-tables.md" obs wr
  {
    echo
    echo "## Per managed resource: Ready/Synced, atProvider.id against the external name, writes observed"
    echo
    echo "Writes are the creates and updates the provider's debug log records for the object over the phase's window; reconciles is the positive control. Path is the namespace and the kind of ProviderConfig the object reaches GitHub through (cluster: the cluster-scoped ProviderConfig)."
    echo
    echo "| Phase | Path | Kind | Object | Ready | Synced | atProvider.id | external-name | id check | creates | updates | reconciles | On the baseline (v0.22.0) |"
    echo "|---|---|---|---|---|---|---|---|---|---|---|---|---|"
    awk -F'\t' '{ for (i = 1; i <= NF; i++) gsub(/\|/, "\\|", $i); printf "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13 }' "${EVIDENCE_DIR}/table-mr.tsv"
    echo
    echo "## Per nested sub-object: status.atProvider against the GitHub snapshot"
    echo
    echo "| Phase | Path | Kind | Object | Sub-object | Result | Detail |"
    echo "|---|---|---|---|---|---|---|"
    awk -F'\t' '{ for (i = 1; i <= NF; i++) gsub(/\|/, "\\|", $i); printf "| %s | %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6, $7 }' "${EVIDENCE_DIR}/table-nested.tsv"
    echo
    echo "## Per ProviderConfig path"
    echo
    echo "| Phase | Path | Objects | Ready and Synced | creates + updates | Nested rows compared | Pass | Fail | Not mirrored |"
    echo "|---|---|---|---|---|---|---|---|---|"
    awk -F'\t' '
      FNR == NR { k = $1 "\t" $2; keys[k] = 1; objs[k]++; if ($5 == "True" && $6 == "True") ok[k]++; wr[k] += $10 + $11; next }
      { k = $1 "\t" $2; keys[k] = 1
        if ($6 == "PASS") { cmp[k]++; pass[k]++ } else if ($6 == "FAIL") { cmp[k]++; fail[k]++ } else if ($6 == "NOT-MIRRORED") { cmp[k]++; nm[k]++ } }
      END { for (k in keys) { split(k, f, "\t"); printf "| %s | %s | %d | %d | %d | %d | %d | %d | %d |\n", f[1], f[2], objs[k], ok[k], wr[k], cmp[k], pass[k], fail[k], nm[k] } }' \
      "${EVIDENCE_DIR}/table-mr.tsv" "${EVIDENCE_DIR}/table-nested.tsv" | sort
    if [ -s "${EVIDENCE_DIR}/table-notready.tsv" ]; then
      echo
      echo "## Objects that are not Ready"
      echo
      echo "Why each says so: the Ready condition's reason and message and the Synced message, as read at the end of the phase (the drift probe is Ready=False by design)."
      echo
      echo "| Phase | Path | Kind | Object | Ready | Ready reason | Synced | Ready message | Synced message |"
      echo "|---|---|---|---|---|---|---|---|---|"
      awk -F'\t' '{ for (i = 1; i <= NF; i++) gsub(/\|/, "\\|", $i); printf "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6, $7, $8, $9 }' "${EVIDENCE_DIR}/table-notready.tsv"
    fi
    if [ -s "${EVIDENCE_DIR}/table-change.tsv" ]; then
      echo
      echo "## Deliberate change per kind"
      echo
      echo "| Kind | Object | Change | In status.atProvider | On GitHub | updates |"
      echo "|---|---|---|---|---|---|"
      awk -F'\t' '{ for (i = 1; i <= NF; i++) gsub(/\|/, "\\|", $i); printf "| %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6 }' "${EVIDENCE_DIR}/table-change.tsv"
    fi
    if [ -s "${EVIDENCE_DIR}/table-refs.tsv" ]; then
      echo
      echo "## References between objects of one namespace"
      echo
      echo "Each twin is an Observe-only object over the same GitHub object as an adopted one, holding its references as Ref or Selector fields instead of plain strings. \"Declared by the plain-string object\" is what the adopted object states for the field; \"Filled in by the resolver\" is what the twin's spec holds once the reference resolver has run. The cross-namespace twin names an object that exists only in the other namespace: its field must stay unset."
      echo
      echo "| Twin | Kind | Namespace | Field | Declared by the plain-string object | Filled in by the resolver | Result |"
      echo "|---|---|---|---|---|---|---|"
      awk -F'\t' '{ for (i = 1; i <= NF; i++) gsub(/\|/, "\\|", $i); printf "| %s | %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6, $7 }' "${EVIDENCE_DIR}/table-refs.tsv"
    fi
    if [ -s "${EVIDENCE_DIR}/table-both.tsv" ] || [ -s "${EVIDENCE_DIR}/both-observe-verdict.txt" ] || [ -s "${EVIDENCE_DIR}/both-write-verdict.txt" ]; then
      obs="$(cat "${EVIDENCE_DIR}/both-observe-verdict.txt" 2>/dev/null)"
      wr="$(cat "${EVIDENCE_DIR}/both-write-verdict.txt" 2>/dev/null)"
      echo
      echo "## Both scopes at once"
      echo
      echo "Observe-only: the cluster-scoped Observe-only twin of one object per kind, applied while the namespaced object is Ready (same external name). Recorded, not asserted."
      echo
      echo "| Kind | Object | Namespaced Ready | Namespaced Synced | Namespaced creates/updates/reconciles | Cluster-scoped Ready | Cluster-scoped Synced | Cluster-scoped creates/updates/reconciles | Cluster-scoped message |"
      echo "|---|---|---|---|---|---|---|---|---|"
      awk -F'\t' '{ for (i = 1; i <= NF; i++) gsub(/\|/, "\\|", $i); printf "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6, $7, $8, $9 }' "${EVIDENCE_DIR}/table-both.tsv"
      echo
      echo "- **Observe-only probe:** ${obs:-not measured}"
      echo "- **Full management of one Team by both scopes:** ${wr:-not measured}"
      echo
      echo "**Does the provider have a guard that stops two scopes managing one object?** $(both_scopes_summary "${obs}" "${wr}")"
    fi
  } >"${out}"
}

# --- step 4: full management --------------------------------------------------------

adopt_full_phase() {
  local since bad f
  log "switching the adopted objects to full management"
  since="$(now_utc)"
  apply_fixtures "${EVIDENCE_DIR}/rendered/full"
  if wait_until "${MIGRATION_READY_TIMEOUT}" 10 adopt_settled "${ADOPT_GROUP}" "${ADOPT_COUNT}"; then
    record PASS "every object is Synced and Ready under full management"
  else
    record FAIL "every object is Synced and Ready under full management" "$(not_settled "${ADOPT_GROUP}" | head -3 | tr '\n' ';')"
  fi
  settle_pause
  mr_state "${ADOPT_GROUP}" >"${EVIDENCE_DIR}/k8s-full.json"
  mr_atprovider "${ADOPT_GROUP}" >"${EVIDENCE_DIR}/atprovider-full.json"
  capture_window_logs full "${since}"
  capture_provider_logs full
  log_findings full
  take_snapshot full

  # The default policies, except an object the baseline kept with deletionPolicy Orphan in a
  # namespaced kind (no deletionPolicy there): its policies leave Delete out.
  for f in "${EVIDENCE_DIR}"/rendered/full/*.yaml; do yq -o=json -I=0 '.' "${f}"; done \
    | jq -s -c 'map({key: "\(.kind)/\(.metadata.name)", value: .spec.managementPolicies}) | from_entries' >"${EVIDENCE_DIR}/expected-policies.json"
  bad="$(jq -r --slurpfile e "${EVIDENCE_DIR}/expected-policies.json" '.[] | select(.managementPolicies != $e[0]["\(.kind)/\(.name)"]) | "\(.kind)/\(.name) \(.managementPolicies)"' "${EVIDENCE_DIR}/k8s-full.json")"
  if [ -z "${bad}" ]; then
    record PASS "every object runs under the management policies of its full manifest (the default ones, with no Delete only where the baseline kept Orphan)"
  else
    record FAIL "every object runs under the management policies of its full manifest" "$(printf '%s' "${bad}" | head -3 | tr '\n' ';')"
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
  atprov="$(mr_atprovider "${ADOPT_GROUP}")"
  [ "$(mr_state "${ADOPT_GROUP}" | jq '[.[] | select(.synced != "True" or .ready != "True")] | length')" -eq 0 ] || return 1
  while IFS=$'\t' read -r kind mr check; do
    got="$(printf '%s' "${atprov}" | jq -cS ".[\"${kind}/${mr}\"] | (${check})" 2>/dev/null)"
    [ "${got}" = true ] || return 1
  done <"${EVIDENCE_DIR}/changes-applied.tsv"
}

adopt_change_phase() {
  local kind mr patch check allowed required note since applied=0 waived="" skipped="" counts creates updates recs
  local nsargs=() changed_kinds=" " reflected on_github bad_unexpected bad_rewrite bad_other bad_unchanged_writes="" lacking_update="" slug value
  : >"${EVIDENCE_DIR}/changes-applied.tsv"
  : >"${EVIDENCE_DIR}/changes-allowed.txt"
  : >"${EVIDENCE_DIR}/changes-echo.tsv"
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
    mapfile -t nsargs < <(adopt_ns_args "${kind}" "${mr}")
    if kc patch "$(plural_of "${kind}").${ADOPT_GROUP}" "${mr}" ${nsargs[@]+"${nsargs[@]}"} --type merge -p "${patch}" </dev/null >>"${EVIDENCE_DIR}/change.log" 2>&1; then
      printf '%s\t%s\t%s\n' "${kind}" "${mr}" "${check}" >>"${EVIDENCE_DIR}/changes-applied.tsv"
      printf '%s\n' "${allowed}" >>"${EVIDENCE_DIR}/changes-allowed.txt"
      # A team's new description is also echoed inside the parent object of its child teams.
      [ "${kind}" != Team ] || jq -r --arg n "${mr}" '(.spec.forProvider.description // empty) | [$n, tojson] | @tsv' <<<"${patch}" >>"${EVIDENCE_DIR}/changes-echo.tsv"
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
      "$(not_settled "${ADOPT_GROUP}" | head -3 | tr '\n' ';')"
  fi
  # Two more cycles: a second write caused by the first would show in the snapshot.
  sleep "$(($(poll_seconds "${MIGRATION_POLL}") * 2 + 15))"
  mr_state "${ADOPT_GROUP}" >"${EVIDENCE_DIR}/k8s-change.json"
  mr_atprovider "${ADOPT_GROUP}" >"${EVIDENCE_DIR}/atprovider-change.json"
  capture_window_logs change "${since}"
  capture_provider_logs change
  take_snapshot changed

  # GitHub: exactly the intended change.
  snapshot_changed_paths "${EVIDENCE_DIR}/snapshots/full.json" "${EVIDENCE_DIR}/snapshots/changed.json" >"${EVIDENCE_DIR}/changed-paths.tsv"
  while IFS=$'\t' read -r slug value; do
    team_echo_allowed "${EVIDENCE_DIR}/snapshots/changed.json" "${slug}" "${value}" >>"${EVIDENCE_DIR}/changes-allowed.txt"
  done <"${EVIDENCE_DIR}/changes-echo.tsv"
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
  adopt_note_not_ready change "${EVIDENCE_DIR}/k8s-change.json"
  bad_other="$(jq -r '.[] | select(.synced != "True" or .ready != "True")
    | "\(.kind)/\(.name) Ready=\(.ready) Synced=\(.synced)\(if (.readyMessage // "") != "" then " (\(.readyMessage[0:100] | gsub("[\t\n]"; " ")))" else "" end)"' "${EVIDENCE_DIR}/k8s-change.json" | head -3 | tr '\n' ';')"
  [ -z "${bad_other}" ] || record FAIL "every object is Synced and Ready after the changes settled" "${bad_other}"
  write_adopt_tables
}

# ===========================================================================
# The namespaced scope: references, and two scopes over one object
# ===========================================================================

# --- references between objects of one namespace -------------------------------------

# refs_settled <names.meta> -- every twin that must resolve is Synced and Ready, and every
# twin that must not is Synced=False.
refs_settled() {
  mr_state "${ADOPT_GROUP}" | jq -e --slurpfile n "$1" '
    . as $s
    | ($n[0].resolvable | all(. as $x | any($s[]; .name == $x and .synced == "True" and .ready == "True")))
      and ($n[0].unresolvable | all(. as $x | any($s[]; .name == $x and .synced == "False")))' >/dev/null 2>&1
}

# adopt_refs_phase -- Observe-only twins of adopted objects that hold their references as
# Ref and Selector fields. A reference resolves inside the namespace of the object that holds
# it: the twins in the namespace of their targets must come out with the spec fields filled
# in by the reference resolver and Synced=True; the twin whose target exists only in the
# other namespace must not resolve.
adopt_refs_phase() {
  local dir="${EVIDENCE_DIR}/rendered/refs" since twin kind ns resolvable path want got result
  local fails=0 compared=0 bad_writes="" bad_idle="" counts creates updates recs state="${EVIDENCE_DIR}/k8s-refs.json"
  derive_ref_twins "${EVIDENCE_DIR}/rendered/observe" "${dir}"
  if [ ! -s "${dir}/checks.tsv" ]; then
    record WARN "no reference twin could be derived: the objects they point at are not in the adopted set"
    return 0
  fi
  log "applying the reference twins"
  since="$(now_utc)"
  apply_fixtures "${dir}"
  if wait_until "${MIGRATION_READY_TIMEOUT}" 10 refs_settled "${dir}/names.meta"; then
    record PASS "the reference twins in the namespace of their targets are Synced and Ready; the twin that names an object of the other namespace is Synced=False"
  else
    record FAIL "the reference twins in the namespace of their targets are Synced and Ready; the twin that names an object of the other namespace is Synced=False" \
      "$(mr_state "${ADOPT_GROUP}" | jq -r --slurpfile n "${dir}/names.meta" '.[] | select(.name as $x | ($n[0].resolvable + $n[0].unresolvable) | index($x)) | "\(.kind)/\(.name): Ready=\(.ready) Synced=\(.synced) \(.syncedMessage)"' | head -3 | tr '\n' ';')"
  fi
  settle_pause 2
  mr_state "${ADOPT_GROUP}" >"${state}"
  capture_window_logs refs "${since}"

  while IFS=$'\t' read -r twin kind ns resolvable path want; do
    got="$(kc get "$(plural_of "${kind}").${ADOPT_GROUP}" "${twin}" -n "${ns}" -o json 2>/dev/null | jq -c "${path}" 2>/dev/null)"
    [ -n "${got}" ] || got=null
    compared=$((compared + 1))
    if [ "${resolvable}" = yes ]; then
      if [ "${got}" != null ] && ref_value_equal "${got}" "${want}"; then result=PASS; else result=FAIL; fi
    else
      if [ "${got}" = null ]; then result=PASS; else result=FAIL; fi
    fi
    [ "${result}" = PASS ] || fails=$((fails + 1))
    if [ "${result}" = FAIL ]; then
      record FAIL "reference ${kind}/${twin} ${path}$([ "${resolvable}" = yes ] || echo ' stays unset')" "declared by the plain-string object: ${want}; the spec holds: ${got}"
    fi
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${twin}" "${kind}" "${ns}" "${path#.spec.forProvider.}" \
      "$([ "${resolvable}" = yes ] && echo "${want:0:70}" || echo "- (the target exists only in the other namespace)")" "${got:0:70}" "${result}" >>"${EVIDENCE_DIR}/table-refs.tsv"
  done <"${dir}/checks.tsv"
  if [ "${fails}" -eq 0 ]; then
    record PASS "every same-namespace reference is resolved into spec.forProvider, and the reference to an object of another namespace is not (${compared} fields)"
  fi
  record INFO "the cross-namespace reference" "$(jq -r --slurpfile n "${dir}/names.meta" '.[] | select(.name as $x | $n[0].unresolvable | index($x)) | "\(.kind)/\(.name): Ready=\(.ready) Synced=\(.synced) \(.syncedMessage)"' "${state}" | head -2 | tr '\n' ';')"

  # Observe-only twins write nothing, and every one was reconciled.
  while IFS=$'\t' read -r twin kind; do
    counts="$(log_counts "${EVIDENCE_DIR}/window-refs.log" "${kind}" "${twin}")"
    read -r creates updates recs <<<"${counts}"
    { [ "${creates}" -eq 0 ] && [ "${updates}" -eq 0 ]; } || bad_writes+="${kind}/${twin} created ${creates} updated ${updates}; "
    [ "${recs}" -gt 0 ] || bad_idle+="${kind}/${twin}; "
  done < <(cut -f1,2 "${dir}/checks.tsv" | sort -u)
  if [ -z "${bad_writes}" ] && [ -z "${bad_idle}" ]; then
    record PASS "no reference twin issued a create or an update, and every one was reconciled (provider debug log)"
  else
    record FAIL "no reference twin issued a create or an update, and every one was reconciled (provider debug log)" "${bad_writes}${bad_idle:+not reconciled: ${bad_idle}}"
  fi

  # The twins have served: the objects they observe are changed next.
  while IFS=$'\t' read -r twin kind ns; do
    delete_mr "${kind}" "${ADOPT_GROUP}" "${twin}" "${ns}"
  done < <(cut -f1-3 "${dir}/checks.tsv" | sort -u)
  write_adopt_tables
}

# --- the cluster-scoped twins of Observe-only objects ---------------------------------

# twins_decided <names...> -- every cluster-scoped twin has a Synced condition that is not Unknown.
twins_decided() {
  local state
  state="$(mr_state "${GROUP_CLUSTER}")"
  [ "$(printf '%s' "${state}" | jq 'length')" -ge "${ADOPT_TWIN_COUNT}" ] || return 1
  [ "$(printf '%s' "${state}" | jq '[.[] | select(.synced == "Unknown")] | length')" -eq 0 ]
}

# adopt_bothscopes_observe_phase -- a cluster-scoped Observe-only object over the same GitHub
# object as a Ready, Observe-only namespaced one (same external name), for one object per kind.
# The driver adopts into one scope at a time; this records what happens when both are
# applied: what each controller does, what the status shows, whether either writes.
adopt_bothscopes_observe_phase() {
  local dir="${EVIDENCE_DIR}/rendered/cluster-twins" since twin kind name total=0 synced=0 degraded=0
  local nready nsynced ncounts cready csynced ccounts cmsg cid nid verdict
  derive_cluster_twins "${EVIDENCE_DIR}/rendered/v1" "${EVIDENCE_DIR}/k8s-v1.json" "${EVIDENCE_DIR}/rendered/observe" "${dir}"
  ADOPT_TWIN_COUNT="$(find "${dir}" -name '*.yaml' | wc -l)"
  if [ "${ADOPT_TWIN_COUNT}" -eq 0 ]; then
    record WARN "no cluster-scoped twin could be derived: none of the twinned objects is in the adopted set"
    return 0
  fi
  log "applying ${ADOPT_TWIN_COUNT} cluster-scoped Observe-only twins of namespaced objects"
  since="$(now_utc)"
  apply_fixtures "${dir}"
  wait_until "${MIGRATION_READY_TIMEOUT}" 10 twins_decided || warn "not every cluster-scoped twin reported a Synced condition"
  settle_pause 2
  mr_state "${GROUP_CLUSTER}" >"${EVIDENCE_DIR}/k8s-twins-cluster.json"
  mr_state "${ADOPT_GROUP}" >"${EVIDENCE_DIR}/k8s-twins-ns.json"
  capture_window_logs twins "${since}"
  capture_provider_logs twins
  take_snapshot twins

  while IFS=$'\t' read -r kind twin cready csynced cmsg cid; do
    name="${twin#pgh-mig-cl-}"
    nready="$(jq -r --arg k "${kind}" --arg n "${name}" '[.[] | select(.kind == $k and .name == $n)][0].ready // "-"' "${EVIDENCE_DIR}/k8s-twins-ns.json")"
    nsynced="$(jq -r --arg k "${kind}" --arg n "${name}" '[.[] | select(.kind == $k and .name == $n)][0].synced // "-"' "${EVIDENCE_DIR}/k8s-twins-ns.json")"
    nid="$(jq -r --arg k "${kind}" --arg n "${name}" '[.[] | select(.kind == $k and .name == $n)][0].atProviderId // "-"' "${EVIDENCE_DIR}/k8s-twins-ns.json")"
    ncounts="$(log_counts "${EVIDENCE_DIR}/window-twins.log" "${kind}" "${name}" "${ADOPT_GROUP}" | tr ' ' /)"
    ccounts="$(log_counts "${EVIDENCE_DIR}/window-twins.log" "${kind}" "${twin}" "${GROUP_CLUSTER}" | tr ' ' /)"
    total=$((total + 1))
    [ "${csynced}" = True ] && synced=$((synced + 1))
    [ "${nsynced}" = True ] || degraded=$((degraded + 1))
    record INFO "both scopes observe ${kind}/${name}" \
      "namespaced: Ready=${nready} Synced=${nsynced} id=${nid} (creates/updates/reconciles ${ncounts}); cluster-scoped ${twin}: Ready=${cready} Synced=${csynced} id=${cid} (${ccounts}) ${cmsg:0:120}"
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${kind}" "${name}" "${nready}" "${nsynced}" "${ncounts}" "${cready}" "${csynced}" "${ccounts}" "${cmsg:0:100}" >>"${EVIDENCE_DIR}/table-both.tsv"
  done < <(jq -r '.[] | [.kind, .name, .ready, .synced, (.syncedMessage | if . == "" then "-" else . end), ((.atProviderId // "-") | tostring)] | @tsv' "${EVIDENCE_DIR}/k8s-twins-cluster.json")

  if "${MIGRATION_DIR}/snapshot.sh" diff "${EVIDENCE_DIR}/snapshots/adopted.json" "${EVIDENCE_DIR}/snapshots/twins.json" >"${EVIDENCE_DIR}/diff-adopted-vs-twins.txt" 2>&1; then
    record INFO "both scopes (Observe-only): GitHub is identical before and after the cluster-scoped twins, timestamps included" "neither controller wrote"
  else
    record WARN "both scopes (Observe-only): GitHub differs between before and after the cluster-scoped twins, an Observe-only object wrote" \
      "$(grep -c '^[-+][^-+]' "${EVIDENCE_DIR}/diff-adopted-vs-twins.txt" || true) differing line(s), see diff-adopted-vs-twins.txt"
  fi
  verdict="$(both_scopes_observe_verdict "${total}" "${synced}" "${degraded}")"
  printf '%s' "${verdict}" >"${EVIDENCE_DIR}/both-observe-verdict.txt"
  record INFO "both scopes (Observe-only) verdict" "${verdict}"

  # The twins served; the namespaced objects are switched to full management next.
  while IFS=$'\t' read -r kind twin; do
    delete_mr "${kind}" "${GROUP_CLUSTER}" "${twin}"
  done < <(jq -r '.[] | [.kind, .name] | @tsv' "${EVIDENCE_DIR}/k8s-twins-cluster.json")
  write_adopt_tables
}

# --- one Team under full management by both scopes -----------------------------------

# adopt_bothscopes_write_phase -- guarded: one disposable Team (pgh-mig-both-team, swept with
# the rest), a harmless field (its description), Orphan on both objects. The namespaced object
# creates the team; a cluster-scoped object adopts it with a different description. If nothing
# stops two scopes managing one object, each controller keeps overwriting the other's value.
# MIGRATION_BOTH_SCOPES_WRITE=0 skips it.
adopt_bothscopes_write_phase() {
  local dir="${EVIDENCE_DIR}/rendered/both" since nsu clu creates recs ns_synced cl_synced gh_desc ns_desc cl_desc verdict
  if [ "${MIGRATION_BOTH_SCOPES_WRITE:-1}" != 1 ]; then
    record INFO "both scopes under full management" "skipped (MIGRATION_BOTH_SCOPES_WRITE=0)"
    return 0
  fi
  derive_both_teams "${dir}"
  log "creating the disposable Team ${ADOPT_BOTH_TEAM} through a namespaced object, then adopting it with a cluster-scoped one"
  kc apply -f "${dir}/10-team-namespaced.yaml" >>"${EVIDENCE_DIR}/apply.log" 2>&1 || warn "applying the namespaced Team failed"
  wait_until 600 10 both_ns_ready || record WARN "both scopes: the namespaced Team did not become Synced and Ready before the cluster-scoped one was applied"
  since="$(now_utc)"
  kc apply -f "${dir}/20-team-cluster.yaml" >>"${EVIDENCE_DIR}/apply.log" 2>&1 || warn "applying the cluster-scoped Team failed"
  wait_until 300 10 both_cluster_decided || warn "the cluster-scoped Team reported no Synced condition"
  settle_pause
  capture_window_logs both "${since}"
  capture_provider_logs both
  read -r creates nsu recs <<<"$(log_counts "${EVIDENCE_DIR}/window-both.log" Team "${ADOPT_BOTH_TEAM}" "${GROUP_NAMESPACED}")"
  read -r creates clu recs <<<"$(log_counts "${EVIDENCE_DIR}/window-both.log" Team pgh-mig-cl-both-team "${GROUP_CLUSTER}")"
  ns_synced="$(mr_condition "$(plural_of Team).${GROUP_NAMESPACED}" "${ADOPT_BOTH_TEAM}" "${ADOPT_NS_A}" Synced)"
  cl_synced="$(mr_condition "$(plural_of Team).${GROUP_CLUSTER}" pgh-mig-cl-both-team - Synced)"
  gh_desc="$(gh_get "/orgs/${MIGRATION_ORG}/teams/${ADOPT_BOTH_TEAM}" 2>/dev/null)" \
    && gh_desc="$(jq -r '.description // ""' <<<"${gh_desc}")" || gh_desc="unreadable"
  ns_desc="$(kc get "$(plural_of Team).${GROUP_NAMESPACED}" "${ADOPT_BOTH_TEAM}" -n "${ADOPT_NS_A}" -o json 2>/dev/null | jq -r '.status.atProvider.description // ""')"
  cl_desc="$(kc get "$(plural_of Team).${GROUP_CLUSTER}" pgh-mig-cl-both-team -o json 2>/dev/null | jq -r '.status.atProvider.description // ""')"
  record INFO "both scopes under full management: updates the controllers issued over ${MIGRATION_SETTLE_POLLS} poll cycles" \
    "namespaced ${nsu} (Synced=${ns_synced}), cluster-scoped ${clu} (Synced=${cl_synced})"
  record INFO "both scopes under full management: the description now" \
    "GitHub '${gh_desc}'; namespaced status.atProvider '${ns_desc}'; cluster-scoped status.atProvider '${cl_desc}'"
  record INFO "both scopes under full management: the messages" \
    "namespaced: $(mr_state "${GROUP_NAMESPACED}" | jq -r --arg n "${ADOPT_BOTH_TEAM}" '[.[] | select(.name == $n)][0].syncedMessage // ""' | cut -c1-120); cluster-scoped: $(mr_state "${GROUP_CLUSTER}" | jq -r '[.[] | select(.name == "pgh-mig-cl-both-team")][0].syncedMessage // ""' | cut -c1-120)"
  verdict="$(both_scopes_write_verdict "${nsu}" "${clu}" "${ns_synced}" "${cl_synced}")"
  printf '%s' "${verdict}" >"${EVIDENCE_DIR}/both-write-verdict.txt"
  case "${verdict}" in
    "NO GUARD"*) record WARN "both scopes under full management: finding" "${verdict}" ;;
    *) record INFO "both scopes under full management: verdict" "${verdict}" ;;
  esac
  # Orphan on both: deleting the objects leaves the team for the cleanup sweep.
  delete_mr Team "${GROUP_CLUSTER}" pgh-mig-cl-both-team
  delete_mr Team "${GROUP_NAMESPACED}" "${ADOPT_BOTH_TEAM}" "${ADOPT_NS_A}"
  write_adopt_tables
}

both_ns_ready() {
  [ "$(mr_condition "$(plural_of Team).${GROUP_NAMESPACED}" "${ADOPT_BOTH_TEAM}" "${ADOPT_NS_A}" Synced)" = True ] \
    && [ "$(mr_condition "$(plural_of Team).${GROUP_NAMESPACED}" "${ADOPT_BOTH_TEAM}" "${ADOPT_NS_A}" Ready)" = True ]
}

both_cluster_decided() {
  [ "$(mr_condition "$(plural_of Team).${GROUP_CLUSTER}" pgh-mig-cl-both-team - Synced)" != Unknown ]
}
