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
#   KUBECTL          path to the kubectl binary (uptest sets it; default: kubectl)
#   E2E_ORG_SECRETS  "true" when the run includes the SecretAccess or the
#                    Organization examples (the Makefile sets it); the run then
#                    creates the disposable organization secrets those examples
#                    manage, and removes them when it ends. Needs E2E_ORG, which
#                    the Makefile exports.
#   E2E_ORG_OWN_SECRETS "true" when the run includes the ActionsOrganizationSecret
#                    examples (the Makefile sets it); the provider creates those
#                    secrets itself, so setup only starts a detached watcher that
#                    deletes them if the run ends without reaching the teardown
#                    script. Needs E2E_ORG, which the Makefile exports.
#   E2E_ORG_ACTIONS  "true" when the run includes the Organization examples (the
#                    Makefile sets it); the run first checks that the
#                    organization's Actions policy is the baseline the update test
#                    restores, and refuses to start otherwise.
#   E2E_REPO_FIXTURES "true" when the run includes the Organization, Repository
#                    branches or RunnerGroup examples (the Makefile sets it); the
#                    run then creates the disposable template and workflow
#                    repositories those examples need, and removes them when it
#                    ends.
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

# ---------------------------------------------------------------------------
# Disposable GitHub objects the examples need but the provider never creates:
#
#   organization secrets  ActionsSecretAccess, DependabotSecretAccess and the
#                         Organization's secrets field manage access to a secret
#                         but never create one.
#   repositories          a template with more than one branch (the
#                         repository-branches examples) and a repository holding
#                         a workflow file (the RunnerGroup and Organization
#                         examples).
#
# test/teardown.sh removes them once the delete step has passed. A run that ends
# before that step -- a failed assertion, a stopped job -- never reaches the
# teardown script, so a detached watcher per script also removes them when this
# cluster disappears. A watcher holds no file descriptor of this process open.
# ---------------------------------------------------------------------------
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# start_watcher <script> [verb] detaches "<script> <verb>" (verb defaults to
# watch). The runner removes the kubeconfig it gives this script once the script
# returns, so the watcher keeps a private flattened copy of the context; the copy
# is the watcher's own (it deletes the file when it exits), so each watcher gets
# one.
start_watcher() {
  local script="$1" verb="${2:-watch}" watch_kubeconfig
  watch_kubeconfig="$(mktemp)"
  ${KUBECTL} config view --minify --flatten > "${watch_kubeconfig}"
  WATCH_KUBECONFIG="${watch_kubeconfig}" KUBECTL="${KUBECTL}" \
    setsid nohup "${ROOT}/test/${script}" "${verb}" \
    >"${TMPDIR:-/tmp}/${script}-${verb}.$$.log" 2>&1 </dev/null &
}

if [ "${E2E_ORG_ACTIONS:-}" = "true" ]; then
  "${ROOT}/test/org-secrets.sh" check-actions
fi

if [ "${E2E_ORG_SECRETS:-}" = "true" ]; then
  # The watcher starts first, as below: a create that fails half way has still
  # made a secret, and nothing else would remove it.
  start_watcher org-secrets.sh
  "${ROOT}/test/org-secrets.sh" create
fi

if [ "${E2E_ORG_OWN_SECRETS:-}" = "true" ]; then
  # The provider creates these secrets, so there is nothing to create here; the
  # watcher removes them if the run ends before the teardown script.
  start_watcher org-secrets.sh watch-own
fi

if [ "${E2E_REPO_FIXTURES:-}" = "true" ]; then
  # The watcher starts first: a create that fails half way has still made a
  # repository, and nothing else would remove it.
  start_watcher repo-fixtures.sh
  "${ROOT}/test/repo-fixtures.sh" create
fi

echo "setup.sh: complete -- credentials Secret, ProviderConfig, namespaced ProviderConfig and ClusterProviderConfig applied."
