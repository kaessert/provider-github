#!/usr/bin/env bash
# test/migration/scenario-adopt-namespaced.sh -- scenario (c): the full adoption flow
# into the namespaced kinds (organizations.github.m.crossplane.io) of the candidate
# provider. What the baseline provider (v0.22.0) created is orphaned, adopted
# Observe-only by new namespaced managed resources derived from the v1 fixtures,
# switched to full management and changed once per kind, with the objects spread over
# two namespaces: one reaching GitHub through a namespaced ProviderConfig, one through a
# ClusterProviderConfig. Between those steps it resolves references between objects of
# one namespace, and records what two scopes do over one GitHub object. The body is
# adopt-common.sh (run_adopt_full); setup is oob.sh prepare upgrade; cleanup is
# scenario.sh.
set -uo pipefail

MIGRATION_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scenario.sh
. "${MIGRATION_DIR}/scenario.sh"
# shellcheck source=adopt-common.sh
. "${MIGRATION_DIR}/adopt-common.sh"

run_adopt_full namespaced
