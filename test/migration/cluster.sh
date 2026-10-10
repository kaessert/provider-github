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

# The harness is started by `make validate.migration.*`, and that make exports its
# whole variable set (OUTPUT_DIR, XPKG_OUTPUT_DIR, ROOT_DIR, tool paths,
# IMAGE_TEMP_DIR, MAKEFLAGS, ...) to everything it starts. A baseline build that
# inherits them writes its binary, image and package into the candidate's output
# directories, and an inherited IMAGE_TEMP_DIR names a directory the first image
# build already removed. So every make that looks at a tree other than the
# candidate's runs through clean_make, which keeps only an allowlist of the
# environment.
unset IMAGE_TEMP_DIR MAKEFLAGS MFLAGS MAKELEVEL

clean_make() {
  local v args=()
  for v in PATH HOME USER LOGNAME LANG LC_ALL TERM TMPDIR XDG_CACHE_HOME XDG_CONFIG_HOME \
    DOCKER_HOST DOCKER_CONFIG DOCKER_CONTEXT DOCKER_BUILDKIT DOCKER_CERT_PATH DOCKER_TLS_VERIFY \
    GOPATH GOCACHE GOMODCACHE GOPROXY GOSUMDB GONOSUMDB GONOPROXY GOPRIVATE GOTOOLCHAIN GOENV GOROOT \
    HTTP_PROXY HTTPS_PROXY NO_PROXY http_proxy https_proxy no_proxy SSL_CERT_FILE SSL_CERT_DIR; do
    [ -n "${!v+x}" ] && args+=("${v}=${!v}")
  done
  env -i "${args[@]}" make "$@"
}

# The baseline tag pins an `up` CLI whose Docker client is too old for current
# daemons ("client version 1.41 is too old"); the baseline package is built with
# a newer one. The candidate does not use `up`.
MIGRATION_BASELINE_UP_VERSION="${MIGRATION_BASELINE_UP_VERSION:-v0.39.0}"

CROSSPLANE_NS="${CROSSPLANE_NAMESPACE:-crossplane-system}"
PROVIDER_NAME="provider-github"
GROUP_CLUSTER="organizations.github.crossplane.io"
GROUP_NAMESPACED="organizations.github.m.crossplane.io"
PLURALS="organizations memberships teams repositories organizationvariables organizationwebhooks runnergroups actionssecretaccesses dependabotsecretaccesses"
# Package digests are labels for the local cache, not content hashes: the two
# builds must not share one so the package manager sees an upgrade.
# They differ in their FIRST characters: Crossplane names the package revision and
# the cached package after the first 12 characters of the digest, so digests that
# differ only at the end would make the upgrade reuse the baseline's revision and
# cache entry.
DIGEST_BASELINE="sha256:1111111111111111111111111111111111111111111111111111111111111111"
DIGEST_CANDIDATE="sha256:2222222222222222222222222222222222222222222222222222222222222222"
BASELINE_VERSION_LABEL="${MIGRATION_BASELINE_REF}"
CANDIDATE_VERSION_LABEL="v0.0.0-migration"
BASELINE_DIR=""

# make_var <tree> <VAR> -- prints the value make computes for VAR in <tree>.
make_var() {
  clean_make -C "$1" -s --no-print-directory --eval="__migration_probe: ; @echo \$($2)" __migration_probe
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

# tree_arch <tree> -- the architecture <tree> builds for.
tree_arch() {
  local arch
  arch="$(make_var "$1" ARCH)"
  [ -n "${arch}" ] || arch="$(host_arch)"
  printf '%s' "${arch}"
}

# tree_image <tree> -- the local name the tree's runtime image is kept under.
# Both trees build to one name (same project, same build registry), so the second
# build would replace the first; each image is retagged right after its build.
tree_image() {
  if [ "$1" = "${BASELINE_DIR}" ]; then
    printf 'migration.local/%s:baseline' "${PROVIDER_NAME}"
  else
    printf 'migration.local/%s:candidate' "${PROVIDER_NAME}"
  fi
}

# build_tree <dir> <version> -- builds the provider image and package from <dir>.
build_tree() {
  local dir="$1" version="$2"
  log "building ${dir} as ${version} (this takes several minutes)"
  # load_package takes the one package found in the output directory, so a
  # package left by an earlier build must not be there.
  rm -f "$(make_var "${dir}" XPKG_OUTPUT_DIR)/linux_$(tree_arch "${dir}")/"*.xpkg
  local extra=()
  [ "${dir}" = "${BASELINE_DIR}" ] && extra=("UP_VERSION=${MIGRATION_BASELINE_UP_VERSION}")
  clean_make -C "${dir}" build VERSION="${version}" ${extra[@]+"${extra[@]}"} >"${EVIDENCE_DIR}/build-$(basename "${dir}").log" 2>&1 \
    || { tail -30 "${EVIDENCE_DIR}/build-$(basename "${dir}").log" >&2; die "build of ${dir} failed (full log: ${EVIDENCE_DIR}/build-$(basename "${dir}").log)"; }
  local built
  built="$(make_var "${dir}" BUILD_REGISTRY)/${PROVIDER_NAME}-$(tree_arch "${dir}")"
  docker tag "${built}" "$(tree_image "${dir}")" || die "could not tag ${built} as $(tree_image "${dir}")"
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
  local tree="$1" label="$2" digest="$3" arch xpkg_dir xpkg cache pod friendly
  arch="$(tree_arch "${tree}")"
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
  "${KIND_BIN}" load docker-image "$(tree_image "${tree}")" --name "${KIND_CLUSTER_NAME}" >/dev/null \
    || die "could not load $(tree_image "${tree}") into the kind cluster"
  log "loaded ${label} build ($(tree_image "${tree}")) into cluster ${KIND_CLUSTER_NAME}"
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
  local tree="$1" label="$2" digest="$3"
  shift 3
  apply_runtime_config "${label}" "$(tree_image "${tree}")" "$@"
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

cluster_crds() { # cluster_crds <cluster|all> -- the CRDs a healthy install provides
  local p
  echo "providerconfigs.github.crossplane.io"
  for p in ${PLURALS}; do echo "${p}.${GROUP_CLUSTER}"; done
  if [ "$1" = "all" ]; then
    echo "clusterproviderconfigs.github.m.crossplane.io"
    for p in ${PLURALS}; do echo "${p}.${GROUP_NAMESPACED}"; done
  fi
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

# apply_namespaced_provider_configs -- the two ways a namespaced managed resource
# reaches GitHub, each with its credentials where that path looks for them:
#   * a ProviderConfig (github.m.crossplane.io) in namespace A, whose Secret is in
#     namespace A (a namespaced ProviderConfig reads its Secret from its own namespace);
#   * a ClusterProviderConfig, whose Secret is in the Crossplane namespace, used from
#     namespace B, which holds no credentials Secret and no ProviderConfig.
# The ProviderConfig of namespace A and the ClusterProviderConfig have different names,
# so a reference can only mean one of them.
apply_namespaced_provider_configs() {
  local ns_pc="$1" ns_cpc="$2" pc="$3" cpc="$4" pem creds ns
  require_credentials
  pem="$(printf '%s' "${PROVIDER_GITHUB_APP_PRIVATE_KEY_B64}" | base64 -d)"
  case "${pem}" in -----BEGIN*) ;; *) die "PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 does not decode to a PEM private key" ;; esac
  creds="${PROVIDER_GITHUB_APP_ID},${PROVIDER_GITHUB_APP_INSTALLATION_ID},${pem}"
  for ns in "${ns_pc}" "${ns_cpc}"; do
    kc create namespace "${ns}" --dry-run=client -o yaml | kc apply -f - >/dev/null
  done
  for ns in "${ns_pc}" "${CROSSPLANE_NS}"; do
    printf '%s' "${creds}" \
      | kc create secret generic github-app-credentials --namespace "${ns}" --from-file=creds=/dev/stdin --dry-run=client -o yaml \
      | kc apply -f - >/dev/null
  done
  wait_until 120 3 kc get crd/providerconfigs.github.m.crossplane.io >/dev/null 2>&1 \
    || die "the namespaced ProviderConfig CRD did not appear"
  wait_until 120 3 kc get crd/clusterproviderconfigs.github.m.crossplane.io >/dev/null 2>&1 \
    || die "the ClusterProviderConfig CRD did not appear"
  kc apply -f - >/dev/null <<YAML
apiVersion: github.m.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: ${pc}
  namespace: ${ns_pc}
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: ${ns_pc}
      name: github-app-credentials
      key: creds
---
apiVersion: github.m.crossplane.io/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: ${cpc}
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: ${CROSSPLANE_NS}
      name: github-app-credentials
      key: creds
YAML
  log "ProviderConfig ${pc} in ${ns_pc} (credentials in ${ns_pc}), ClusterProviderConfig ${cpc} for ${ns_cpc} (credentials in ${CROSSPLANE_NS}; ${ns_cpc} holds none)"
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
      readyMessage: ([.status.conditions[]? | select(.type == "Ready") | .message][0] // ""),
      readyReason: ([.status.conditions[]? | select(.type == "Ready") | .reason][0] // ""),
      deleting: (.metadata.deletionTimestamp != null),
      atProviderId: (.status.atProvider.id // null),
      providerConfigKind: (.spec.providerConfigRef.kind // "ProviderConfig"),
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
