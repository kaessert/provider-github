#!/usr/bin/env bash
# test/migration/cluster.sh -- cluster-side helpers for the migration scenarios.
# Sourced by the scenario drivers; not executable on its own.
#
# What it does: builds the baseline (the upstream tag, from source) and the
# candidate (this tree), puts a control plane on the cluster the launcher gave
# us, loads both builds into that cluster locally -- no registry is involved --
# installs one or the other as the Provider package, and reads managed-resource
# state back.
#
# The cluster itself is never created under a name chosen here. require_cluster_env
# insists on KIND_CLUSTER_NAME and KUBECONFIG from the launcher; the control plane
# is installed with the candidate tree's own control-plane target, which is what
# the end-to-end flow uses, and that target reads the same variable.
#
# shellcheck shell=bash

# shellcheck disable=SC2034  # the scenario drivers use these
CROSSPLANE_NS="${CROSSPLANE_NAMESPACE:-crossplane-system}"
PROVIDER_NAME="provider-github"
GROUP_CLUSTER="organizations.github.crossplane.io"
GROUP_NAMESPACED="organizations.github.m.crossplane.io"
PLURALS="organizations memberships teams repositories organizationvariables organizationwebhooks runnergroups actionssecretaccesses dependabotsecretaccesses"
# Package digests are labels for the local cache, not content hashes: the two
# builds must not share one so the package manager sees an upgrade.
DIGEST_BASELINE="sha256:0000000000000000000000000000000000000000000000000000000000000001"
DIGEST_CANDIDATE="sha256:0000000000000000000000000000000000000000000000000000000000000002"
BASELINE_VERSION_LABEL="${MIGRATION_BASELINE_REF}"
CANDIDATE_VERSION_LABEL="v0.0.0-migration"
BASELINE_DIR=""

# make_var <tree> <VAR> -- prints the value make computes for VAR in <tree>.
make_var() {
  make -C "$1" -s --no-print-directory --eval="__migration_probe: ; @echo \$($2)" __migration_probe
}

host_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) die "unsupported host architecture $(uname -m)" ;;
  esac
}

# use_candidate_tools installs (through the candidate tree's Makefile) and
# selects the kubectl, kind and Crossplane CLI the candidate pins, so both
# builds are driven by one set of tool versions.
CROSSPLANE_CLI_BIN=""
KIND_BIN=""
use_candidate_tools() {
  local v path
  for v in KUBECTL KIND CROSSPLANE_CLI HELM; do
    path="$(make_var "${MIGRATION_CANDIDATE_DIR}" "${v}")"
    [ -n "${path}" ] || die "the candidate Makefile does not define ${v}"
    if [ ! -x "${path}" ]; then
      log "installing ${v} through the candidate Makefile"
      make -C "${MIGRATION_CANDIDATE_DIR}" "${path}" >&2 || die "could not install ${v}"
    fi
    case "${v}" in
      KUBECTL) [ -n "${KUBECTL:-}" ] || KUBECTL_BIN="${path}" ;;
      KIND) KIND_BIN="${path}" ;;
      CROSSPLANE_CLI) CROSSPLANE_CLI_BIN="${path}" ;;
    esac
  done
}

kc() { "${KUBECTL_BIN}" "$@"; }

# ---------------------------------------------------------------------------
# Builds
# ---------------------------------------------------------------------------

# fetch_baseline places a checkout of the baseline tag in BASELINE_DIR.
fetch_baseline() {
  if [ -n "${MIGRATION_BASELINE_DIR:-}" ]; then
    [ -d "${MIGRATION_BASELINE_DIR}" ] || die "MIGRATION_BASELINE_DIR ${MIGRATION_BASELINE_DIR} does not exist"
    BASELINE_DIR="${MIGRATION_BASELINE_DIR}"
    return 0
  fi
  BASELINE_DIR="${MIGRATION_WORKDIR}/baseline-src"
  if [ ! -d "${BASELINE_DIR}/.git" ]; then
    log "fetching ${MIGRATION_BASELINE_REF} from ${MIGRATION_BASELINE_REPO}"
    git clone --quiet --depth 1 --branch "${MIGRATION_BASELINE_REF}" "${MIGRATION_BASELINE_REPO}" "${BASELINE_DIR}" \
      || die "could not fetch ${MIGRATION_BASELINE_REF} from ${MIGRATION_BASELINE_REPO}"
  fi
  git -C "${BASELINE_DIR}" submodule update --quiet --init --depth 1 \
    || die "could not initialise the build submodule of the baseline checkout"
  local described
  described="$(git -C "${BASELINE_DIR}" describe --tags --exact-match 2>/dev/null || true)"
  [ "${described}" = "${MIGRATION_BASELINE_REF}" ] \
    || die "baseline checkout ${BASELINE_DIR} is at '${described:-no tag}', expected ${MIGRATION_BASELINE_REF}"
}

# build_tree <dir> <version> -- builds the provider image and package from <dir>.
build_tree() {
  local dir="$1" version="$2"
  log "building ${dir} as ${version} (this takes several minutes)"
  make -C "${dir}" build VERSION="${version}" >"${EVIDENCE_DIR}/build-$(basename "${dir}").log" 2>&1 \
    || { tail -30 "${EVIDENCE_DIR}/build-$(basename "${dir}").log" >&2; die "build of ${dir} failed (full log: ${EVIDENCE_DIR}/build-$(basename "${dir}").log)"; }
}

# assert_baseline_crds records whether the baseline checkout's CRDs are the
# ones the fixtures were validated against.
assert_baseline_crds() {
  local ref="${MIGRATION_BASELINE_CRDS_REF:-13ff658}" tmp f
  tmp="$(mktemp -d)"
  for f in $(git -C "${PROVIDER_ROOT}" ls-tree --name-only "${ref}" package/crds/ 2>/dev/null); do
    git -C "${PROVIDER_ROOT}" show "${ref}:${f}" >"${tmp}/$(basename "${f}")"
  done
  if diff -rq "${tmp}" "${BASELINE_DIR}/package/crds" >/dev/null 2>&1; then
    record PASS "baseline ${MIGRATION_BASELINE_REF} CRDs are identical to the ones the v1 fixtures were validated against (${ref})"
  else
    record FAIL "baseline ${MIGRATION_BASELINE_REF} CRDs are identical to the ones the v1 fixtures were validated against (${ref})" \
      "$(diff -rq "${tmp}" "${BASELINE_DIR}/package/crds" 2>&1 | head -3 | tr '\n' ';')"
  fi
  rm -rf "${tmp}"
}

# ---------------------------------------------------------------------------
# Control plane
# ---------------------------------------------------------------------------

# cluster_up installs the control plane on the launcher's cluster. The candidate
# tree's control-plane target creates the kind cluster only if none of that name
# exists, so a cluster the launcher already made is reused.
cluster_up() {
  require_cluster_env
  log "bringing up the control plane on cluster ${KIND_CLUSTER_NAME}"
  make -C "${MIGRATION_CANDIDATE_DIR}" controlplane.up >"${EVIDENCE_DIR}/controlplane.log" 2>&1 \
    || { tail -20 "${EVIDENCE_DIR}/controlplane.log" >&2; die "controlplane.up failed"; }
  make -C "${MIGRATION_CANDIDATE_DIR}" local.xpkg.init >>"${EVIDENCE_DIR}/controlplane.log" 2>&1 \
    || { tail -20 "${EVIDENCE_DIR}/controlplane.log" >&2; die "local.xpkg.init failed"; }
  kc -n "${CROSSPLANE_NS}" wait deploy crossplane --for condition=Available --timeout=180s >/dev/null \
    || die "Crossplane did not become Available"
  local ns
  for ns in "${CROSSPLANE_NS}" default; do
    kc create namespace "${ns}" --dry-run=client -o yaml | kc apply -f - >/dev/null
  done
}

# load_package <tree> <label> <digest> -- makes the tree's build available to the
# cluster without a registry: image into the kind node, package into the
# Crossplane package cache.
load_package() {
  local tree="$1" label="$2" digest="$3" arch registry xpkg_dir xpkg cache pod friendly
  arch="$(make_var "${tree}" ARCH)"
  [ -n "${arch}" ] || arch="$(host_arch)"
  registry="$(make_var "${tree}" BUILD_REGISTRY)"
  xpkg_dir="$(make_var "${tree}" XPKG_OUTPUT_DIR)/linux_${arch}"
  xpkg="$(find "${xpkg_dir}" -maxdepth 1 -name '*.xpkg' 2>/dev/null | sort | head -n1)"
  [ -f "${xpkg}" ] || die "no package built under ${xpkg_dir}"
  cache="${MIGRATION_WORKDIR}/xpkg-${label}/cache"
  rm -rf "${MIGRATION_WORKDIR}/xpkg-${label}"
  mkdir -p "${cache}/xpkg.crossplane.internal/dev"
  # Two cache keys, as the package manager looks the package up by source and
  # digest in older releases and by a derived name in newer ones.
  "${CROSSPLANE_CLI_BIN}" xpkg extract --from-xpkg "${xpkg}" \
    -o "${cache}/xpkg.crossplane.internal/dev/${PROVIDER_NAME}@${digest}.gz" >/dev/null \
    || die "could not extract ${xpkg}"
  friendly="$(printf '%.50s-%.12s' "xpkg.crossplane.internal/dev/${PROVIDER_NAME}" "${digest}" | sed 's/[^a-z0-9]/-/g' | cut -c1-63 | sed 's/-*$//')"
  cp "${cache}/xpkg.crossplane.internal/dev/${PROVIDER_NAME}@${digest}.gz" "${cache}/${friendly}.gz"
  pod="$(kc -n "${CROSSPLANE_NS}" get pod -l app=crossplane,patched=true -o jsonpath='{.items[0].metadata.name}')"
  [ -n "${pod}" ] || die "no patched Crossplane pod to copy the package into"
  kc -n "${CROSSPLANE_NS}" cp "${cache}" -c dev "${pod}:/tmp" >/dev/null || die "could not copy the package into ${pod}"
  "${KIND_BIN}" load docker-image "${registry}/${PROVIDER_NAME}-${arch}" --name "${KIND_CLUSTER_NAME}" >/dev/null \
    || die "could not load ${registry}/${PROVIDER_NAME}-${arch} into the kind cluster"
  log "loaded ${label} build (${registry}/${PROVIDER_NAME}-${arch}) into cluster ${KIND_CLUSTER_NAME}"
}

# apply_runtime_config <label> <image> <args...>
apply_runtime_config() {
  local label="$1" image="$2" args
  shift 2
  args="$(printf '%s\n' "$@" | jq -R . | jq -sc .)"
  jq -cn --arg name "runtimeconfig-${PROVIDER_NAME}-${label}" --arg image "${image}" --argjson args "${args}" '
    {apiVersion: "pkg.crossplane.io/v1beta1", kind: "DeploymentRuntimeConfig",
     metadata: {name: $name},
     spec: {deploymentTemplate: {spec: {selector: {}, strategy: {},
       template: {spec: {containers: [{name: "package-runtime", image: $image, args: $args}]}}}}}}' \
    | kc apply -f - >/dev/null
}

# install_provider <tree> <label> <digest> <args...> -- creates or updates the
# Provider so that it runs <tree>'s build with the given runtime arguments.
install_provider() {
  local tree="$1" label="$2" digest="$3" arch registry
  shift 3
  arch="$(make_var "${tree}" ARCH)"
  [ -n "${arch}" ] || arch="$(host_arch)"
  registry="$(make_var "${tree}" BUILD_REGISTRY)"
  apply_runtime_config "${label}" "${registry}/${PROVIDER_NAME}-${arch}" "$@"
  jq -cn --arg name "${PROVIDER_NAME}" --arg pkg "xpkg.crossplane.internal/dev/${PROVIDER_NAME}@${digest}" \
    --arg drc "runtimeconfig-${PROVIDER_NAME}-${label}" '
    {apiVersion: "pkg.crossplane.io/v1", kind: "Provider", metadata: {name: $name},
     spec: {package: $pkg, skipDependencyResolution: true, packagePullPolicy: "Never",
            runtimeConfigRef: {name: $drc}}}' | kc apply -f - >/dev/null
  log "Provider ${PROVIDER_NAME} now points at the ${label} build"
}

provider_serves() { # provider_serves <digest> -- the Provider runs <digest> and is Healthy
  local ident healthy
  ident="$(kc get "provider.pkg.crossplane.io/${PROVIDER_NAME}" -o jsonpath='{.status.currentIdentifier}' 2>/dev/null)"
  healthy="$(kc get "provider.pkg.crossplane.io/${PROVIDER_NAME}" -o jsonpath='{.status.conditions[?(@.type=="Healthy")].status}' 2>/dev/null)"
  case "${ident}" in *"$1") [ "${healthy}" = "True" ] ;; *) return 1 ;; esac
}

wait_provider() { # wait_provider <digest> <crd...>
  local digest="$1" crd
  shift
  wait_until 420 5 provider_serves "${digest}" || {
    kc get "provider.pkg.crossplane.io/${PROVIDER_NAME}" -o yaml >"${EVIDENCE_DIR}/provider-not-healthy.yaml" 2>&1
    die "Provider did not become Healthy on ${digest} (state saved in provider-not-healthy.yaml)"
  }
  for crd in "$@"; do
    wait_until 180 3 kc get "crd/${crd}" >/dev/null 2>&1 || die "CRD ${crd} did not appear"
    kc wait "crd/${crd}" --for=condition=Established --timeout=120s >/dev/null || die "CRD ${crd} not Established"
  done
  # The new revision's pods replace the old ones; wait for the rollout.
  kc -n "${CROSSPLANE_NS}" rollout status deploy -l "pkg.crossplane.io/provider=${PROVIDER_NAME}" --timeout=240s >/dev/null 2>&1 || true
}

# provider_pod_images prints the images of the running provider pods.
provider_pod_images() {
  kc -n "${CROSSPLANE_NS}" get pods -l "pkg.crossplane.io/provider=${PROVIDER_NAME}" \
    -o jsonpath='{range .items[*]}{.status.phase}{" "}{.spec.containers[?(@.name=="package-runtime")].image}{"\n"}{end}'
}

# apply_provider_config <cluster|all> -- the credentials Secret and the
# ProviderConfig objects the fixtures reference. The Secret value is
# <app-id>,<installation-id>,<pem>, the format the provider reads.
apply_provider_config() {
  local scope="$1" pem creds ns
  require_credentials
  pem="$(printf '%s' "${PROVIDER_GITHUB_APP_PRIVATE_KEY_B64}" | base64 -d)"
  case "${pem}" in -----BEGIN*) ;; *) die "PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 does not decode to a PEM private key" ;; esac
  creds="${PROVIDER_GITHUB_APP_ID},${PROVIDER_GITHUB_APP_INSTALLATION_ID},${pem}"
  for ns in "${CROSSPLANE_NS}" default; do
    printf '%s' "${creds}" \
      | kc create secret generic github-app-credentials --namespace "${ns}" --from-file=creds=/dev/stdin --dry-run=client -o yaml \
      | kc apply -f - >/dev/null
  done
  kc apply -f - >/dev/null <<YAML
apiVersion: github.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: ${CROSSPLANE_NS}
      name: github-app-credentials
      key: creds
YAML
  if [ "${scope}" = "all" ]; then
    wait_until 120 3 kc get crd/clusterproviderconfigs.github.m.crossplane.io >/dev/null 2>&1 \
      || die "the namespaced ProviderConfig CRDs did not appear"
    kc apply -f - >/dev/null <<YAML
apiVersion: github.m.crossplane.io/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: ${CROSSPLANE_NS}
      name: github-app-credentials
      key: creds
---
apiVersion: github.m.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
  namespace: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: default
      name: github-app-credentials
      key: creds
YAML
  fi
}

# ---------------------------------------------------------------------------
# Managed resources
# ---------------------------------------------------------------------------

plural_of() { # plural_of <Kind>
  case "$1" in
    Organization) echo organizations ;;
    Membership) echo memberships ;;
    Team) echo teams ;;
    Repository) echo repositories ;;
    OrganizationVariable) echo organizationvariables ;;
    OrganizationWebhook) echo organizationwebhooks ;;
    RunnerGroup) echo runnergroups ;;
    ActionsSecretAccess) echo actionssecretaccesses ;;
    DependabotSecretAccess) echo dependabotsecretaccesses ;;
    *) die "unknown kind $1" ;;
  esac
}

resource_list() { # resource_list <group> -- "organizations.<group>,memberships.<group>,..."
  local p out=""
  for p in ${PLURALS}; do out+="${p}.$1,"; done
  printf '%s' "${out%,}"
}

# mr_state <group> -- prints a JSON array summarising every managed resource of
# the group (empty array when the CRDs are absent).
mr_state() {
  local out
  out="$(kc get "$(resource_list "$1")" -A -o json 2>/dev/null)" || { echo '[]'; return 0; }
  printf '%s' "${out}" | jq -c '
    [.items[] | {
      kind, name: .metadata.name, namespace: (.metadata.namespace // ""),
      uid: .metadata.uid,
      externalName: (.metadata.annotations["crossplane.io/external-name"] // ""),
      ready: ([.status.conditions[]? | select(.type == "Ready") | .status][0] // "Unknown"),
      synced: ([.status.conditions[]? | select(.type == "Synced") | .status][0] // "Unknown"),
      syncedMessage: ([.status.conditions[]? | select(.type == "Synced") | .message][0] // ""),
      deleting: (.metadata.deletionTimestamp != null),
      atProviderId: (.status.atProvider.id // null),
      managementPolicies: (.spec.managementPolicies // null),
      deletionPolicy: (.spec.deletionPolicy // null),
      hasPublishConnectionDetailsTo: (.spec | has("publishConnectionDetailsTo")),
      hasProviderRef: (.spec | has("providerRef"))
    }] | sort_by(.kind, .namespace, .name)'
}

# all_settled <group> <ready-names-file> -- true when every managed resource is
# Synced, and Ready unless it is listed as expected-not-Ready.
all_settled() {
  local state
  state="$(mr_state "$1")"
  [ "$(printf '%s' "${state}" | jq 'length')" -gt 0 ] || return 1
  [ "$(printf '%s' "${state}" | jq '[.[] | select(.synced != "True" or .ready != "True")] | length')" -eq 0 ]
}

# wait_settled <group> <timeout> -- waits for every managed resource to be
# Synced and Ready and returns 0 if they did; the final table goes to the
# evidence directory either way.
wait_settled() {
  local group="$1" timeout="$2" rc=0
  wait_until "${timeout}" 10 all_settled "${group}" || rc=1
  mr_state "${group}" >"${EVIDENCE_DIR}/mr-state-settle-$(date +%s).json"
  return "${rc}"
}

# not_settled <group> -- "Kind/name: Ready=.. Synced=.. <message>" lines.
not_settled() {
  mr_state "$1" | jq -r '.[] | select(.synced != "True" or .ready != "True")
    | "\(.kind)/\(.name): Ready=\(.ready) Synced=\(.synced) \(.syncedMessage)"'
}

resources_gone() { [ "$(kc get "$1" -A -o name 2>/dev/null | wc -l)" -eq 0 ]; }

# delete_managed_resources <group> <timeout> -- deletes every managed resource of
# the group and waits for the finalizers to clear. After the timeout the
# finalizers of what is left are removed so the cluster can be cleaned; the
# caller still sweeps GitHub through the API.
delete_managed_resources() {
  local group="$1" timeout="$2" list
  list="$(resource_list "${group}")"
  kc get "${list}" -A >/dev/null 2>&1 || return 0
  kc delete "${list}" --all -A --wait=false >/dev/null 2>&1 || true
  if ! wait_until "${timeout}" 5 resources_gone "${list}"; then
    warn "managed resources still present after ${timeout}s; removing their finalizers"
    kc get "${list}" -A -o json 2>/dev/null | jq -r '.items[] | "\(.kind | ascii_downcase).\(.apiVersion | split("/")[0]) \(.metadata.namespace // "-") \(.metadata.name)"' \
      | while read -r res ns name; do
        if [ "${ns}" = "-" ]; then
          kc patch "${res}" "${name}" --type merge -p '{"metadata":{"finalizers":null}}' >/dev/null 2>&1 || true
        else
          kc patch "${res}" "${name}" -n "${ns}" --type merge -p '{"metadata":{"finalizers":null}}' >/dev/null 2>&1 || true
        fi
      done
    return 1
  fi
  return 0
}

# remove_provider deletes the Provider and its runtime configs so that nothing
# reconciles while GitHub is being swept.
remove_provider() {
  kc delete "provider.pkg.crossplane.io/${PROVIDER_NAME}" --wait=true --timeout=180s >/dev/null 2>&1 || true
  kc delete deploymentruntimeconfigs.pkg.crossplane.io "runtimeconfig-${PROVIDER_NAME}-v1" "runtimeconfig-${PROVIDER_NAME}-v2" >/dev/null 2>&1 || true
}

# capture_provider_logs <label> -- saves the logs of every provider pod.
capture_provider_logs() {
  local label="$1" pod
  for pod in $(kc -n "${CROSSPLANE_NS}" get pods -l "pkg.crossplane.io/provider=${PROVIDER_NAME}" -o name 2>/dev/null); do
    kc -n "${CROSSPLANE_NS}" logs "${pod}" --all-containers >"${EVIDENCE_DIR}/provider-${label}-$(basename "${pod}").log" 2>&1 || true
  done
}

# log_findings <label> -- counts error and panic lines in the saved logs.
log_findings() {
  local label="$1" f errs panics
  for f in "${EVIDENCE_DIR}"/provider-"${label}"-*.log; do
    [ -f "${f}" ] || continue
    panics="$(grep -ciE 'panic|nil pointer|segmentation' "${f}" || true)"
    errs="$(grep -ciE '(^|[^a-z])error([^a-z]|$)' "${f}" || true)"
    if [ "${panics}" -gt 0 ]; then
      record FAIL "provider log $(basename "${f}") has no panic" "${panics} panic-like line(s)"
    else
      record PASS "provider log $(basename "${f}") has no panic"
    fi
    record INFO "provider log $(basename "${f}") error lines" "${errs}"
  done
}
