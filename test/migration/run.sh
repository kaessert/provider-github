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
#   adopt-cluster     scenario (b): the full adoption flow, cluster-scoped kinds
#   adopt-namespaced  scenario (c): the full adoption flow, namespaced kinds, both
#                     ProviderConfig kinds, references, both-scopes probe
#
# The three scenarios call the GitHub API and need a cluster, so they refuse to
# start without the GitHub App credentials (named when missing) and without the
# cluster name and kubeconfig of a cluster somebody else's launcher created.
# Make's validate.migration target runs all three in turn.
#
# The four offline steps write their evidence to a private directory when
# MIGRATION_WORKDIR is unset, so two runs at once (two worktrees, two shells) never
# share results files. The directory is removed when the step passes and kept, with
# its path printed, when it fails. An explicit MIGRATION_WORKDIR is used as given. The
# three scenarios keep the documented default, which later runs reuse (the token
# cache, the baseline checkout, the cleanup baseline).
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRIVATE_WORKDIR=""
case "${1:-}" in
  fixtures | coverage | separation | selftest)
    if [ -z "${MIGRATION_WORKDIR:-}" ]; then
      PRIVATE_WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/provider-github-migration-${1}.XXXXXX")" || {
        echo "run.sh: could not create a private work directory under ${TMPDIR:-/tmp}" >&2
        exit 1
      }
      export MIGRATION_WORKDIR="${PRIVATE_WORKDIR}"
    fi
    ;;
esac
# shellcheck source=lib.sh
. "${HERE}/lib.sh"

step="${1:-}"
run_offline() { # run_offline <script>: run it, then drop a private work directory on success
  "${HERE}/$1"
  local rc=$?
  if [ -n "${PRIVATE_WORKDIR}" ]; then
    if [ "${rc}" -eq 0 ]; then
      rm -rf "${PRIVATE_WORKDIR}"
    else
      echo "run.sh: ${step} failed; evidence kept in ${PRIVATE_WORKDIR}" >&2
    fi
  fi
  exit "${rc}"
}

case "${step}" in
  fixtures) run_offline validate-fixtures.sh ;;
  coverage) run_offline check-coverage.sh ;;
  separation) run_offline check-separation.sh ;;
  selftest) run_offline selftest.sh ;;
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
