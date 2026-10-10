#!/usr/bin/env bash
# test/migration/scenario.sh -- scaffolding shared by the three scenario drivers:
# set-up, assertion helpers and the cleanup that returns the organization to its
# baseline snapshot on every exit path. Sourced; not executable on its own.
#
# shellcheck shell=bash

# shellcheck source=lib.sh
. "${MIGRATION_DIR}/lib.sh"
# shellcheck source=cluster.sh
. "${MIGRATION_DIR}/cluster.sh"

CLEANED=0
BACKGROUND_PIDS=()

# scenario_begin <name> -- checks every precondition before
# anything is created, so a missing input fails in seconds and names itself.
scenario_begin() {
  local name="$1"
  mkdir -p "${MIGRATION_WORKDIR}"
  require_tools curl openssl jq yq base64 git make docker go
  require_credentials
  require_cluster_env
  init_scenario "${name}"
  mkdir -p "${EVIDENCE_DIR}/snapshots" "${EVIDENCE_DIR}/rendered"
  trap 'scenario_cleanup $?' EXIT
  trap 'exit 130' INT TERM HUP
}

# scenario_cleanup <rc> -- runs on every exit. It deletes the managed resources,
# removes the provider (so nothing reconciles while GitHub is swept), sweeps
# GitHub back to the baseline and compares the result with the baseline snapshot.
scenario_cleanup() {
  local rc="$1" pid
  set +e
  trap - EXIT
  [ "${CLEANED}" -eq 0 ] || return 0
  CLEANED=1
  for pid in "${BACKGROUND_PIDS[@]}"; do kill "${pid}" 2>/dev/null; done
  log "cleanup (exit status of the scenario body: ${rc})"
  if [ -n "${KUBECONFIG:-}" ] && kc version >/dev/null 2>&1; then
    capture_provider_logs final
    delete_managed_resources "${GROUP_CLUSTER}" 300 || record WARN "cluster-scoped managed resources needed forced finalizer removal"
    delete_managed_resources "${GROUP_NAMESPACED}" 300 || record WARN "namespaced managed resources needed forced finalizer removal"
    remove_provider
    # The namespaces of the namespaced adoption scenario (a no-op for the others).
    kc delete namespace "${ADOPT_NS_A}" "${ADOPT_NS_B}" --ignore-not-found --wait=false >/dev/null 2>&1
    kc delete clusterproviderconfigs.github.m.crossplane.io "${ADOPT_CPC_NAME}" --ignore-not-found >/dev/null 2>&1
    kc delete secret pgh-mig-hook-secret -n "${CROSSPLANE_NS}" --ignore-not-found >/dev/null 2>&1
    kc delete secret pgh-mig-hook-wcs-conn pgh-mig-hook-ess-wcs-conn pgh-mig-hook-ess-conn -n "${CROSSPLANE_NS}" --ignore-not-found >/dev/null 2>&1
  else
    warn "the cluster is not reachable; skipping the cluster half of the cleanup"
  fi
  if "${MIGRATION_DIR}/oob.sh" sweep >"${EVIDENCE_DIR}/sweep.log" 2>&1; then
    record PASS "sweep removed every pgh-mig- object and restored the organization settings"
  else
    record FAIL "sweep removed every pgh-mig- object and restored the organization settings" "see sweep.log"
  fi
  if "${MIGRATION_DIR}/oob.sh" verify-baseline >"${EVIDENCE_DIR}/baseline-diff.txt" 2>&1; then
    record PASS "the organization is back at its baseline snapshot"
  else
    record FAIL "the organization is back at its baseline snapshot" "see baseline-diff.txt"
  fi
  write_report
  summarize
  local status=$?
  [ "${rc}" -eq 0 ] || status=1
  exit "${status}"
}

# write_report renders results.tsv into a short Markdown report beside it.
write_report() {
  {
    echo "# Migration validation: ${SCENARIO}"
    echo
    echo "Organization ${MIGRATION_ORG}, baseline ${MIGRATION_BASELINE_REF}, candidate $(git -C "${MIGRATION_CANDIDATE_DIR}" rev-parse --short HEAD 2>/dev/null || echo unknown)."
    echo
    echo "| Status | Check | Detail |"
    echo "|---|---|---|"
    awk -F'\t' '{printf "| %s | %s | %s |\n", $1, $2, $3}' "${RESULTS_FILE}"
    # The adoption flow keeps its per-object and per-sub-object tables beside the results.
    [ ! -s "${EVIDENCE_DIR}/report-tables.md" ] || cat "${EVIDENCE_DIR}/report-tables.md"
  } >"${EVIDENCE_DIR}/report.md"
}

# render_fixtures <src-dir> <dst-dir> -- renders every fixture of a directory.
render_fixtures() {
  local f
  mkdir -p "$2"
  for f in "$1"/*.yaml; do
    render "${f}" "$2/$(basename "${f}")"
  done
}

# apply_fixtures <rendered-dir> -- applies every file; the prerequisite Secret
# file sorts first.
apply_fixtures() {
  local f rc=0
  for f in "$1"/*.yaml; do
    kc apply -f "${f}" >>"${EVIDENCE_DIR}/apply.log" 2>&1 || { warn "applying ${f} failed (see apply.log)"; rc=1; }
  done
  return "${rc}"
}

take_snapshot() { # take_snapshot <name> -- snapshots/<name>.json
  "${MIGRATION_DIR}/snapshot.sh" dump "${EVIDENCE_DIR}/snapshots/$1.json" || die "snapshot $1 failed"
}

# assert_snapshots_identical <check name> <snapshot a> <snapshot b> [--ignore-timestamps]
assert_snapshots_identical() {
  local name="$1" a="$2" b="$3" mode="${4:-}" out="${EVIDENCE_DIR}/diff-$2-vs-$3${4:+-timestamps-ignored}.txt"
  if "${MIGRATION_DIR}/snapshot.sh" diff "${EVIDENCE_DIR}/snapshots/${a}.json" "${EVIDENCE_DIR}/snapshots/${b}.json" ${mode:+"${mode}"} >"${out}" 2>&1; then
    record PASS "${name}"
  else
    record FAIL "${name}" "$(grep -c '^[-+][^-+]' "${out}" || true) differing line(s), see $(basename "${out}")"
  fi
}

# settle_pause [polls] waits long enough for the running provider to complete a few poll
# cycles, so a change it was going to make has been made before the next snapshot.
settle_pause() {
  local polls="${1:-${MIGRATION_SETTLE_POLLS}}" seconds
  seconds=$(($(poll_seconds "${MIGRATION_POLL}") * polls + 15))
  log "letting the provider run ${polls} poll cycles (${seconds}s)"
  sleep "${seconds}"
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
