#!/usr/bin/env bash
# test/migration/scenario-adopt-cluster.sh -- scenario (b): Observe-only adoption
# into the cluster-scoped kinds of the candidate provider. The body is
# adopt-common.sh; setup is oob.sh prepare adopt; cleanup is scenario.sh.
set -uo pipefail

MIGRATION_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scenario.sh
. "${MIGRATION_DIR}/scenario.sh"
# shellcheck source=adopt-common.sh
. "${MIGRATION_DIR}/adopt-common.sh"

run_adopt cluster
