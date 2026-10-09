#!/usr/bin/env bash
# test/setup.sh -- E2E pre-flight setup for provider-github.
#
# uptest runs this script (UPTEST_SETUP_SCRIPT) once the provider package is
# applied and before any example manifest. It builds the credentials Secret
# from the GitHub App values in the environment and applies a ProviderConfig
# for each API scope the examples use. Every operation is `apply`, so running
# it again against an existing cluster is safe.
#
# Required environment:
#   PROVIDER_GITHUB_APP_ID               GitHub App ID
#   PROVIDER_GITHUB_APP_INSTALLATION_ID  installation ID of the App on the test org
#   PROVIDER_GITHUB_APP_PRIVATE_KEY_B64  the App's PEM private key, base64-encoded
#
# Optional environment:
#   KUBECTL  path to the kubectl binary (uptest sets it; default: kubectl)
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"

for var in PROVIDER_GITHUB_APP_ID PROVIDER_GITHUB_APP_INSTALLATION_ID PROVIDER_GITHUB_APP_PRIVATE_KEY_B64; do
  if [ -z "${!var:-}" ]; then
    echo "setup.sh: ERROR: ${var} is not set." >&2
    echo "  Export it from your environment or place it in ../.env" >&2
    exit 1
  fi
done

# The provider reads one comma-separated credential: <app-id>,<installation-id>,<pem>.
PEM="$(printf '%s' "${PROVIDER_GITHUB_APP_PRIVATE_KEY_B64}" | base64 -d)"
case "${PEM}" in
  -----BEGIN*) ;;
  *)
    echo "setup.sh: ERROR: PROVIDER_GITHUB_APP_PRIVATE_KEY_B64 does not decode to a PEM private key." >&2
    exit 1
    ;;
esac
CREDS="${PROVIDER_GITHUB_APP_ID},${PROVIDER_GITHUB_APP_INSTALLATION_ID},${PEM}"

echo "setup.sh: GitHub App credentials are set, proceeding..."

# ---------------------------------------------------------------------------
# Wait for the ProviderConfig-family CRDs to be Established.
#
# local.xpkg.deploy.provider.* applies the Provider package and returns before
# the package manager has unpacked it and registered its CRDs, so an
# un-retried apply of a ProviderConfig races that and fails with "no matches
# for kind ProviderConfig ... ensure CRDs are installed first". The jsonpath
# poll reads an EMPTY string both while the CRD does not exist and while it
# exists with no .status yet, so both races fall into the same retry.
# ---------------------------------------------------------------------------
wait_for_crd_established() {
  local crd="$1"
  local max_wait=120
  local elapsed=0
  while [ "$(${KUBECTL} get "crd/${crd}" -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>/dev/null)" != "True" ]; do
    if [ "${elapsed}" -ge "${max_wait}" ]; then
      echo "setup.sh: ERROR: CRD ${crd} not Established within ${max_wait}s" >&2
      return 1
    fi
    sleep 3
    elapsed=$((elapsed + 3))
  done
}

for crd in providerconfigs.github.crossplane.io providerconfigs.github.m.crossplane.io clusterproviderconfigs.github.m.crossplane.io; do
  echo "setup.sh: waiting for CRD ${crd} to be Established..."
  wait_for_crd_established "${crd}"
done

# ---------------------------------------------------------------------------
# Namespaces. Cluster-scoped examples publish connection secrets into
# crossplane-system; namespaced examples live in default.
# ---------------------------------------------------------------------------
for ns in crossplane-system default; do
  ${KUBECTL} create namespace "${ns}" --dry-run=client -o yaml | ${KUBECTL} apply -f -
done

# ---------------------------------------------------------------------------
# Credentials Secret. A namespaced ProviderConfig reads its Secret from its own
# namespace, so the Secret exists in both. The value goes in on stdin so the
# private key never appears in a process listing.
# ---------------------------------------------------------------------------
for ns in crossplane-system default; do
  printf '%s' "${CREDS}" \
    | ${KUBECTL} create secret generic github-app-credentials --namespace "${ns}" \
        --from-file=creds=/dev/stdin --dry-run=client -o yaml \
    | ${KUBECTL} apply -f -
done

# ---------------------------------------------------------------------------
# Cluster-scoped ProviderConfig (github.crossplane.io), referenced by name from
# the cluster-scoped examples.
# ---------------------------------------------------------------------------
${KUBECTL} apply -f - <<YAML
apiVersion: github.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: github-app-credentials
      key: creds
YAML

# ---------------------------------------------------------------------------
# Namespaced ProviderConfig (github.m.crossplane.io). It is namespace-scoped, so
# it must live in the namespace the namespaced examples use (default); one in
# crossplane-system serves examples placed there.
# ---------------------------------------------------------------------------
for ns in default crossplane-system; do
  ${KUBECTL} apply -f - <<YAML
apiVersion: github.m.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
  namespace: ${ns}
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: ${ns}
      name: github-app-credentials
      key: creds
YAML
done

# ---------------------------------------------------------------------------
# ClusterProviderConfig (github.m.crossplane.io): cluster-scoped, referenced
# from any namespace; the default for a namespaced resource that names none.
# ---------------------------------------------------------------------------
${KUBECTL} apply -f - <<YAML
apiVersion: github.m.crossplane.io/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: github-app-credentials
      key: creds
YAML

echo "setup.sh: complete -- credentials Secret, ProviderConfig, namespaced ProviderConfig and ClusterProviderConfig applied."
