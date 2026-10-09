#!/usr/bin/env bash
# test/migration/selftest.sh -- offline tests for the harness itself. No cluster
# and no call to GitHub: the snapshot tool runs against testdata/mockapi.py, a
# read-only local stand-in for the REST API, and the validators run against
# mutated copies of the fixtures.
#
# Covered:
#   * snapshot dump reads only (the stand-in refuses everything but GET), keys
#     on the pgh-mig- / PGH_MIG_ prefixes, and is stable between two dumps;
#   * snapshot diff reports a recreated object (new numeric ID), a changed
#     setting and a changed timestamp, ignores an unrelated object, and drops
#     timestamps on request;
#   * the fixture validator rejects an unknown field, a `selected` object without
#     repositories and a write-capable object that omits a create-time field;
#   * the coverage checker rejects a missing row and an unsatisfied row;
#   * the entry point fails fast and names the missing input.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${HERE}/lib.sh"

require_tools python3 jq yq kubeconform curl
init_scenario selftest
T="${EVIDENCE_DIR}/work"
rm -rf "${T}"
mkdir -p "${T}"

# --- stand-in API ---------------------------------------------------------------
cp -r "${HERE}/testdata/api" "${T}/api"
REQLOG="${T}/requests.log"
: >"${REQLOG}"
python3 "${HERE}/testdata/mockapi.py" "${T}/api" "${REQLOG}" >"${T}/port" 2>"${T}/mock.err" &
MOCK_PID=$!
trap 'kill "${MOCK_PID}" 2>/dev/null' EXIT
for _ in $(seq 1 50); do [ -s "${T}/port" ] && break; sleep 0.1; done
PORT="$(cat "${T}/port")"
[ -n "${PORT}" ] || die "the stand-in API did not start: $(cat "${T}/mock.err")"

snap() { # snap <out>
  MIGRATION_API_URL="http://127.0.0.1:${PORT}" MIGRATION_GITHUB_TOKEN=selftest MIGRATION_MEMBER=octocat \
    MIGRATION_WORKDIR="${T}/wd" PROVIDER_GITHUB_APP_ID=1 PROVIDER_GITHUB_APP_INSTALLATION_ID=2 \
    PROVIDER_GITHUB_APP_PRIVATE_KEY_B64="$(printf x | base64)" "${HERE}/snapshot.sh" dump "$1" 2>"${T}/snap.err"
}
SNAP="${HERE}/snapshot.sh"

if snap "${T}/a.json" && jq -e . "${T}/a.json" >/dev/null; then
  record PASS "snapshot dump produces a JSON document"
else
  record FAIL "snapshot dump produces a JSON document" "$(tail -3 "${T}/snap.err" | tr '\n' ';')"
fi

if ! grep -qvE '^GET ' "${REQLOG}"; then
  record PASS "snapshot dump issues GET requests only ($(wc -l <"${REQLOG}") requests)"
else
  record FAIL "snapshot dump issues GET requests only" "$(grep -vE '^GET ' "${REQLOG}" | head -2 | tr '\n' ';')"
fi

want='pgh-mig-repo-main'
got="$(jq -r '.repos | keys | join(",")' "${T}/a.json" 2>/dev/null)"
[ "${got}" = "${want}" ] && record PASS "only prefixed repositories are read in (other-repo is not)" \
  || record FAIL "only prefixed repositories are read in" "got ${got}"
got="$(jq -r '[(.teams|keys[]), (.variables|keys[]), (.secrets.actions|keys[]), (.runnerGroups|keys[]), (.orgHooks|keys[])] | join(",")' "${T}/a.json" 2>/dev/null)"
want='pgh-mig-team-child,PGH_MIG_VAR_SELECTED,PGH_MIG_ACTIONS_SECRET,pgh-mig-rg-selected,https://example.com/pgh-mig/org-hook-wcs'
[ "${got}" = "${want}" ] && record PASS "teams, variables, secrets, runner groups and webhooks are filtered by prefix" \
  || record FAIL "teams, variables, secrets, runner groups and webhooks are filtered by prefix" "got ${got}"
jq -e '.repos["pgh-mig-repo-main"] | (.branchProtection.main.required_signatures.enabled == true)
  and (.rulesets[0].id == 5001) and (.hooks[0].hookUrl == "https://example.com/pgh-mig/repo-hook")
  and (.hooks[0].config.secret == "********") and (.collaborators[0].login == "octocat")
  and (.org? == null)' "${T}/a.json" >/dev/null 2>&1 \
  && record PASS "repository detail carries protection, rulesets, webhooks (secret masked) and collaborators" \
  || record FAIL "repository detail carries protection, rulesets, webhooks (secret masked) and collaborators"
jq -e '.org.actionsEnabledRepos[0].name == "pgh-mig-repo-main" and .membership.role == "admin"' "${T}/a.json" >/dev/null 2>&1 \
  && record PASS "organization settings and membership are captured" \
  || record FAIL "organization settings and membership are captured"

snap "${T}/b.json"
"${SNAP}" diff "${T}/a.json" "${T}/b.json" >"${T}/d0.txt" 2>&1 \
  && record PASS "two dumps of an unchanged organization are identical" \
  || record FAIL "two dumps of an unchanged organization are identical" "$(head -3 "${T}/d0.txt" | tr '\n' ';')"

mutate() { # mutate <name> <jq filter> <file under api, relative>
  local f="${T}/api/$3"
  cp "${f}" "${T}/orig.json"
  jq -c "$2" "${T}/orig.json" >"${f}"
  snap "${T}/$1.json"
  cp "${T}/orig.json" "${f}"
}

mutate recreated '.id = 2002' repos/pgh-test/pgh-mig-repo-main.json
if "${SNAP}" diff "${T}/a.json" "${T}/recreated.json" >"${T}/d1.txt" 2>&1; then
  record FAIL "a recreated repository (new numeric ID) is reported"
elif grep -q 'numeric IDs differ' "${T}/d1.txt"; then
  record PASS "a recreated repository (new numeric ID) is reported"
else
  record FAIL "a recreated repository (new numeric ID) is reported" "$(head -3 "${T}/d1.txt" | tr '\n' ';')"
fi

mutate retitled '.description = "changed"' repos/pgh-test/pgh-mig-repo-main.json
if "${SNAP}" diff "${T}/a.json" "${T}/retitled.json" --ignore-timestamps >"${T}/d2.txt" 2>&1; then
  record FAIL "a changed repository setting is reported"
elif grep -q '+.*"description": "changed"' "${T}/d2.txt"; then
  record PASS "a changed repository setting is reported"
else
  record FAIL "a changed repository setting is reported" "$(head -3 "${T}/d2.txt" | tr '\n' ';')"
fi

mutate touched '.updated_at = "2027-01-01T00:00:00Z"' repos/pgh-test/pgh-mig-repo-main.json
if "${SNAP}" diff "${T}/a.json" "${T}/touched.json" >"${T}/d3.txt" 2>&1; then
  record FAIL "a changed timestamp alone is reported by default"
else
  record PASS "a changed timestamp alone is reported by default"
fi
if "${SNAP}" diff "${T}/a.json" "${T}/touched.json" --ignore-timestamps >"${T}/d4.txt" 2>&1; then
  record PASS "a changed timestamp alone is ignored with --ignore-timestamps"
else
  record FAIL "a changed timestamp alone is ignored with --ignore-timestamps" "$(head -3 "${T}/d4.txt" | tr '\n' ';')"
fi

mutate unrelated '. | map(if .name == "other-repo" then .id = 1 else . end)' orgs/pgh-test/repos.json
if "${SNAP}" diff "${T}/a.json" "${T}/unrelated.json" >"${T}/d5.txt" 2>&1; then
  record PASS "a change to an object outside the prefixes is not seen"
else
  record FAIL "a change to an object outside the prefixes is not seen" "$(head -3 "${T}/d5.txt" | tr '\n' ';')"
fi

# --- fixture validator ----------------------------------------------------------
copy_fixtures() { rm -rf "${T}/fx"; cp -r "${HERE}/fixtures" "${T}/fx"; }
validate_expect_fail() { # validate_expect_fail <name> <text that must appear>
  local out
  out="$(MIGRATION_FIXTURES_DIR="${T}/fx" MIGRATION_WORKDIR="${T}/vwd" "${HERE}/validate-fixtures.sh" 2>&1)"
  if [ $? -ne 0 ] && grep -E '^FAIL' <<<"${out}" | grep -q "$2"; then
    record PASS "$1"
  else
    record FAIL "$1" "validator output: $(grep -E '^(FAIL|PASS)' <<<"${out}" | head -3 | tr '\n' ';')"
  fi
}

copy_fixtures
yq -i 'select(.kind == "Team" and .metadata.name == "pgh-mig-team-parent") .spec.forProvider.noSuchField = "x"' "${T}/fx/v1/10-teams.yaml"
validate_expect_fail "the validator rejects an unknown field in a v1 fixture" "v1 fixtures validate against the baseline CRDs"

copy_fixtures
yq -i 'select(.kind == "ActionsSecretAccess") .spec.forProvider.selectedRepositories = []' "${T}/fx/v1/80-secret-access.yaml"
validate_expect_fail "the validator rejects visibility selected without repositories" "has visibility selected without selectedRepositories"

copy_fixtures
yq -i 'select(.kind == "OrganizationWebhook" and .metadata.name == "pgh-mig-hook-plain") |= del(.spec.forProvider.url)' "${T}/fx/v1/60-webhooks.yaml"
validate_expect_fail "the validator rejects a write-capable object that omits a create-time field" "lacks forProvider.url"

copy_fixtures
yq -i 'select(.kind == "Team" and .metadata.name == "pgh-mig-adopt-team") .spec.forProvider.noSuchField = "x"' "${T}/fx/adopt/namespaced/30-teams.yaml"
validate_expect_fail "the validator rejects drift between the cluster and namespaced adoption fixtures" "namespaced adoption fixtures mirror the cluster ones"

# --- coverage checker -----------------------------------------------------------
grep -v 'repositoryRules\[\].conditions.refName' "${HERE}/coverage.tsv" >"${T}/cov-missing.tsv"
cov_out="$(MIGRATION_COVERAGE_TABLE="${T}/cov-missing.tsv" MIGRATION_WORKDIR="${T}/cwd" "${HERE}/check-coverage.sh" 2>&1)"
if [ $? -ne 0 ] && grep -E '^FAIL' <<<"${cov_out}" | grep -q 'every baseline sub-object has a coverage row'; then
  record PASS "the coverage checker rejects a sub-object with no row"
else
  record FAIL "the coverage checker rejects a sub-object with no row"
fi
{ cat "${HERE}/coverage.tsv"; printf 'Team\t.spec.forProvider.noSuchObject\tnot in any fixture\n'; } >"${T}/cov-unset.tsv"
cov_out="$(MIGRATION_COVERAGE_TABLE="${T}/cov-unset.tsv" MIGRATION_WORKDIR="${T}/cwd" "${HERE}/check-coverage.sh" 2>&1)"
if [ $? -ne 0 ] && grep -E '^FAIL' <<<"${cov_out}" | grep -q 'every coverage row is set by a v1 fixture'; then
  record PASS "the coverage checker rejects a row no fixture satisfies"
else
  record FAIL "the coverage checker rejects a row no fixture satisfies"
fi

# --- entry point fails fast and names the input -----------------------------------
creds=(PROVIDER_GITHUB_APP_ID=1 PROVIDER_GITHUB_APP_INSTALLATION_ID=2 "PROVIDER_GITHUB_APP_PRIVATE_KEY_B64=$(printf x | base64)")
out="$(env -i PATH="${PATH}" "${HERE}/run.sh" upgrade 2>&1)"
if [ $? -ne 0 ] && grep -q 'PROVIDER_GITHUB_APP_ID is not set' <<<"${out}"; then
  record PASS "a scenario without credentials fails at once, naming PROVIDER_GITHUB_APP_ID"
else
  record FAIL "a scenario without credentials fails at once, naming PROVIDER_GITHUB_APP_ID" "${out:0:120}"
fi
out="$(env -i PATH="${PATH}" "${creds[@]}" "${HERE}/run.sh" adopt-cluster 2>&1)"
if [ $? -ne 0 ] && grep -q 'KIND_CLUSTER_NAME is not set' <<<"${out}"; then
  record PASS "a scenario without a launcher-provided cluster fails at once, naming KIND_CLUSTER_NAME"
else
  record FAIL "a scenario without a launcher-provided cluster fails at once, naming KIND_CLUSTER_NAME" "${out:0:120}"
fi
out="$(env -i PATH="${PATH}" "${creds[@]/PROVIDER_GITHUB_APP_INSTALLATION_ID=2/PROVIDER_GITHUB_APP_INSTALLATION_ID=}" KIND_CLUSTER_NAME=x KUBECONFIG=/nonexistent "${HERE}/run.sh" upgrade 2>&1)"
if [ $? -ne 0 ] && grep -q 'PROVIDER_GITHUB_APP_INSTALLATION_ID is not set' <<<"${out}"; then
  record PASS "an empty credential is reported by name"
else
  record FAIL "an empty credential is reported by name" "${out:0:120}"
fi
out="$(env -i PATH="${PATH}" bash -c ". '${HERE}/lib.sh'; echo \"\${MIGRATION_ORG}\"")"
[ "${out}" = "pgh-test" ] && record PASS "the test organization defaults to pgh-test" \
  || record FAIL "the test organization defaults to pgh-test" "got ${out}"

summarize
