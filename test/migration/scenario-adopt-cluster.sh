#!/usr/bin/env bash
# test/migration/scenario-adopt-cluster.sh -- scenario (b): adoption of what the
# baseline provider (v0.22.0) created into the cluster-scoped kinds of the candidate
# provider: the baseline's objects are orphaned, adopted Observe-only by new managed
# resources derived from the v1 fixtures, switched to full management, and changed
# once per kind. The body is adopt-common.sh (run_adopt_full); setup is oob.sh
# prepare upgrade; cleanup is scenario.sh.
set -uo pipefail

MIGRATION_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scenario.sh
. "${MIGRATION_DIR}/scenario.sh"
# shellcheck source=adopt-common.sh
. "${MIGRATION_DIR}/adopt-common.sh"

run_adopt_full cluster
