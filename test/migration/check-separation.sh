#!/usr/bin/env bash
# test/migration/check-separation.sh -- proves the migration harness is not part
# of the end-to-end suite. Offline; runs `make -n` only.
#
# The harness is a one-off upgrade and import validation. It must stay
# unreachable from `make e2e`, from every `e2e.*` target, from every
# UPTEST_MANIFESTS_* variable, from CI workflows and from examples/, and its
# own Makefile target must not begin with `e2e`.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${HERE}/lib.sh"

init_scenario separation
cd "${PROVIDER_ROOT}" || die "cannot enter ${PROVIDER_ROOT}"

# 1. Nothing a make e2e* goal would run mentions the harness.
targets="e2e $(grep -oE '^e2e\.[a-z0-9-]+:' Makefile | tr -d ':' | sort -u | tr '\n' ' ')"
leaks=""
for t in ${targets}; do
  if make -n "${t}" 2>&1 | grep -qiE 'migration|validate\.'; then
    leaks+="${t} "
  fi
done
if [ -z "${leaks}" ]; then
  record PASS "make -n output of e2e and every e2e.<resource> target never mentions the migration harness ($(echo "${targets}" | wc -w) targets)"
else
  record FAIL "make -n output of e2e and every e2e.<resource> target never mentions the migration harness" "${leaks}"
fi

# 2. The target names do not start with e2e, and no e2e target depends on them.
names="$(grep -oE '^validate\.migration[a-z.-]*:' Makefile | tr -d ':' | sort -u)"
bad_names="$(printf '%s\n' "${names}" | grep -E '^e2e' || true)"
if [ -n "${names}" ] && [ -z "${bad_names}" ]; then
  record PASS "the harness targets exist and none of them starts with e2e"
else
  record FAIL "the harness targets exist and none of them starts with e2e" "${names:-no validate.migration target found}"
fi
dep_leak="$(grep -E '^(e2e[a-z0-9.-]*|build|uptest)[a-z0-9.%-]*:.*(validate\.migration|test/migration)' Makefile || true)"
if [ -z "${dep_leak}" ]; then
  record PASS "no e2e, build or uptest target lists the harness as a prerequisite"
else
  record FAIL "no e2e, build or uptest target lists the harness as a prerequisite" "${dep_leak}"
fi

# 2b. Every scenario resolves through make.
unresolved=""
for t in validate.migration validate.migration.upgrade validate.migration.adopt-cluster validate.migration.adopt-namespaced validate.migration.fixtures; do
  out="$(make -n "${t}" 2>&1)" && grep -q 'test/migration/run.sh' <<<"${out}" || unresolved+="${t} "
done
if [ -z "${unresolved}" ]; then
  record PASS "make -n resolves validate.migration and every scenario target"
else
  record FAIL "make -n resolves validate.migration and every scenario target" "${unresolved}"
fi

# 3. No UPTEST_MANIFESTS_* variable or e2e recipe line names the harness.
um="$(grep -nE 'UPTEST_[A-Z_]*.*(migration|pgh-mig)' Makefile || true)"
if [ -z "${um}" ]; then
  record PASS "no UPTEST_* variable references the harness"
else
  record FAIL "no UPTEST_* variable references the harness" "${um}"
fi

# 4. CI workflows.
if [ -d .github/workflows ]; then
  wf="$(grep -rniE 'migration|validate\.migration|pgh-mig' .github/workflows || true)"
else
  wf=""
fi
if [ -z "${wf}" ]; then
  record PASS "no CI workflow references the harness"
else
  record FAIL "no CI workflow references the harness" "$(printf '%s' "${wf}" | head -3 | tr '\n' ';')"
fi

# 5. examples/ holds nothing of the harness, and the harness reads nothing from it.
ex="$(grep -rlE 'pgh-mig|test/migration' examples 2>/dev/null || true)"
if [ -z "${ex}" ]; then
  record PASS "no file under examples/ belongs to or mentions the harness"
else
  record FAIL "no file under examples/ belongs to or mentions the harness" "${ex}"
fi
reads="$(grep -nE '(^|[^a-z])examples/' "${HERE}"/*.sh | grep -v 'check-separation.sh' | grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' || true)"
if [ -z "${reads}" ]; then
  record PASS "no harness script reads from examples/"
else
  record FAIL "no harness script reads from examples/" "$(printf '%s' "${reads}" | head -3 | tr '\n' ';')"
fi

# 6. The harness is referenced from nowhere outside this directory but its Makefile block.
outside="$(grep -rlE 'test/migration|validate\.migration' . --include='*' 2>/dev/null \
  | grep -vE '^\./(test/migration/|\.git/|_output/|\.cache/|\.work/)' | grep -v '^\./Makefile$' || true)"
if [ -z "${outside}" ]; then
  record PASS "nothing outside test/migration and the Makefile refers to the harness"
else
  record FAIL "nothing outside test/migration and the Makefile refers to the harness" "$(printf '%s' "${outside}" | head -3 | tr '\n' ';')"
fi

summarize
