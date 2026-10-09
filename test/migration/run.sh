#!/usr/bin/env bash
# test/migration/run.sh -- entry point of the migration validation harness.
#
# Usage: run.sh <step>
#
#   fixtures          offline: validate the fixtures against the CRDs
#   coverage          offline: check fixture coverage of the baseline schema
#   separation        offline: check the harness is unreachable from the E2E suite
#   selftest          offline: test the snapshot tool and the validators against
#                     a local stand-in for the GitHub API
#   upgrade           scenario (a): in-place upgrade from the baseline tag
#   adopt-cluster     scenario (b): Observe-only adoption, cluster-scoped kinds
#   adopt-namespaced  scenario (c): Observe-only adoption, namespaced kinds
#
# The three scenarios call the GitHub API and need a cluster, so they refuse to
# start without the GitHub App credentials (named when missing) and without the
# cluster name and kubeconfig of a cluster somebody else's launcher created.
# Make's validate.migration target runs all three in turn.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${HERE}/lib.sh"

step="${1:-}"
case "${step}" in
  fixtures) exec "${HERE}/validate-fixtures.sh" ;;
  coverage) exec "${HERE}/check-coverage.sh" ;;
  separation) exec "${HERE}/check-separation.sh" ;;
  selftest) exec "${HERE}/selftest.sh" ;;
  upgrade | adopt-cluster | adopt-namespaced)
    # Fail fast, before any build starts, on the inputs every scenario needs.
    require_credentials
    require_cluster_env
    exec "${HERE}/scenario-${step}.sh"
    ;;
  *)
    echo "usage: ${0##*/} fixtures|coverage|separation|selftest|upgrade|adopt-cluster|adopt-namespaced" >&2
    exit 2
    ;;
esac
