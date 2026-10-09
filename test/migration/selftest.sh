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
#   * the coverage checker rejects a missing row, an unsatisfied row and a coverage
#     row with no adoption expectation;
#   * the adoption flow's pure parts: the manifests derived from the v1 fixtures, the
#     expectation tables (every expression compiles, reads its fixture, and for the
#     objects the stand-in API holds agrees with it), the verdict of a nested row, the
#     comparison of two snapshots into changed paths and their classification, and
#     the write counts read from a provider log;
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

# The App can create an environment but not list them: GitHub answers 403. The
# snapshot records them as unreadable, prints no ERROR line and does not fail.
touch "${T}/api/repos/pgh-test/pgh-mig-repo-main/environments.json.403"
if snap "${T}/envs403.json" && ! grep -q 'ERROR' "${T}/snap.err" \
  && jq -e '.repos["pgh-mig-repo-main"].environments == "unreadable"' "${T}/envs403.json" >/dev/null 2>&1; then
  record PASS "environments the App may not list (403) are recorded as unreadable, without an ERROR line"
else
  record FAIL "environments the App may not list (403) are recorded as unreadable, without an ERROR line" \
    "$(tail -3 "${T}/snap.err" | tr '\n' ';')"
fi
rm -f "${T}/api/repos/pgh-test/pgh-mig-repo-main/environments.json.403"

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
grep -v 'Team	pgh-mig-team-child	.spec.forProvider.members\[\]' "${HERE}/expect-adopt-nested.tsv" >"${T}/expect-missing.tsv"
cov_out="$(MIGRATION_ADOPT_NESTED_TABLE="${T}/expect-missing.tsv" MIGRATION_WORKDIR="${T}/cwd" "${HERE}/check-coverage.sh" 2>&1)"
if [ $? -ne 0 ] && grep -E '^FAIL' <<<"${cov_out}" | grep -q 'every coverage row has an adoption expectation'; then
  record PASS "the coverage checker rejects a coverage row with no adoption expectation"
else
  record FAIL "the coverage checker rejects a coverage row with no adoption expectation"
fi
{ cat "${HERE}/coverage.tsv"; printf 'Team\t.spec.forProvider.noSuchObject\tnot in any fixture\n'; } >"${T}/cov-unset.tsv"
cov_out="$(MIGRATION_COVERAGE_TABLE="${T}/cov-unset.tsv" MIGRATION_WORKDIR="${T}/cwd" "${HERE}/check-coverage.sh" 2>&1)"
if [ $? -ne 0 ] && grep -E '^FAIL' <<<"${cov_out}" | grep -q 'every coverage row is set by a v1 fixture'; then
  record PASS "the coverage checker rejects a row no fixture satisfies"
else
  record FAIL "the coverage checker rejects a row no fixture satisfies"
fi

# --- the adoption flow: derivation, expectations, verdicts ----------------------------
# shellcheck source=adopt-common.sh
. "${HERE}/adopt-common.sh"
# shellcheck disable=SC2034  # read by render in lib.sh
RUNTIME_ENV=/nonexistent
mkdir -p "${T}/ex"
for f in "${HERE}"/fixtures/v1/*.yaml; do render "${f}" "${T}/ex/$(basename "${f}")"; done
fixture_for_provider() { # fixture_for_provider <Kind> <name> -- forProvider of a rendered v1 fixture
  for f in "${T}"/ex/*.yaml; do yq -o=json -I=0 '.' "${f}"; done \
    | jq -c --arg k "$1" --arg n "$2" 'select(.kind == $k and .metadata.name == $n) | .spec.forProvider'
}

# Derivation: the external name comes from the live baseline object, a stale file is replaced.
printf '[{"kind":"OrganizationWebhook","name":"pgh-mig-hook-wcs","externalName":"694960442"},{"kind":"Team","name":"pgh-mig-team-parent","externalName":""}]' >"${T}/live.json"
mkdir -p "${T}/derived"
: >"${T}/derived/stale.yaml"
derive_adoption observe "${T}/ex" "${T}/live.json" "${T}/derived"
hook_ext="$(yq '.metadata.annotations["crossplane.io/external-name"]' "${T}/derived"/*-organizationwebhook-pgh-mig-hook-wcs.yaml)"
team_ext="$(yq '.metadata.annotations["crossplane.io/external-name"]' "${T}/derived"/*-team-pgh-mig-team-parent.yaml)"
var_ext="$(yq '.metadata.annotations["crossplane.io/external-name"]' "${T}/derived"/*-organizationvariable-pgh-mig-var-all.yaml)"
if [ "${hook_ext}" = 694960442 ] && [ "${team_ext}" = pgh-mig-team-parent ] && [ "${var_ext}" = PGH_MIG_VAR_ALL ] && [ ! -e "${T}/derived/stale.yaml" ]; then
  record PASS "a derived manifest takes the live external name, else the fixture's, else the object name; stale files are replaced"
else
  record FAIL "a derived manifest takes the live external name, else the fixture's, else the object name; stale files are replaced" "hook ${hook_ext} team ${team_ext} variable ${var_ext}"
fi
derive_probe "${T}/derived"
probe_file="${T}/derived/99-team-${ADOPT_PROBE_NAME}.yaml"
if [ -f "${probe_file}" ] && [ "$(yq '.spec.forProvider.description' "${probe_file}")" = "${ADOPT_PROBE_DESCRIPTION}" ] \
  && [ "$(yq '.metadata.annotations["crossplane.io/external-name"]' "${probe_file}")" = pgh-mig-team-parent ] \
  && [ "$(yq '.spec.managementPolicies[0]' "${probe_file}")" = Observe ]; then
  record PASS "the drift probe observes the parent team with a description that differs from GitHub's"
else
  record FAIL "the drift probe observes the parent team with a description that differs from GitHub's"
fi
derive_adoption full "${T}/ex" - "${T}/derived-full"
if [ "$(yq '.spec.managementPolicies[0]' "${T}/derived-full"/*-organization-pgh-mig-org.yaml)" = '*' ] \
  && [ "$(yq '.spec.forProvider | has("description")' "${T}/derived-full"/*-organization-pgh-mig-org.yaml)" = true ] \
  && [ "$(yq '.spec | has("providerRef") or has("publishConnectionDetailsTo")' "${T}/derived-full"/*-organizationvariable-pgh-mig-var-providerref.yaml)" = false ] \
  && [ "$(yq '.spec.providerConfigRef.name' "${T}/derived-full"/*-organizationvariable-pgh-mig-var-providerref.yaml)" = default ]; then
  record PASS "a fully managed derivation keeps the v1 forProvider, drops the removed fields and sets the default policies"
else
  record FAIL "a fully managed derivation keeps the v1 forProvider, drops the removed fields and sets the default policies"
fi

# Expectation tables: every expression compiles, reads its fixture, and agrees with the stand-in API.
bad_compile="" bad_shape="" bad_agree="" compared=0 rows=0
while IFS=$'\t' read -r kind mr rid _ a s; do
  [ "${a}" = "-" ] && continue
  rows=$((rows + 1))
  for expr in "${a}" "${s}"; do
    jq -n "${expr}" >/dev/null 2>&1
    [ $? -ne 3 ] || bad_compile+="${kind}/${mr} ${rid}; "
  done
  fixture_for_provider "${kind}" "${mr}" >"${T}/forprovider.json"
  av="$(eval_expr "${a}" "${T}/forprovider.json")"
  case "${av}" in null | ERROR | '') bad_shape+="${kind}/${mr} ${rid}; " ;; esac
  # The stand-in API holds the first repository, the child team, and one variable,
  # runner group and secret with the fixture's repository lists.
  case "${kind}/${mr}/${rid}" in
    Repository/pgh-mig-repo-main/* | */*/.spec.forProvider.selectedRepositories\[\] | Team/pgh-mig-team-child/.spec.forProvider.parent)
      case "${kind}/${mr}" in OrganizationVariable/pgh-mig-var-all | RunnerGroup/pgh-mig-rg-workflows | DependabotSecretAccess/*) continue ;; esac
      sv="$(eval_expr "${s}" "${T}/a.json")"
      compared=$((compared + 1))
      [ "${av}" = "${sv}" ] || bad_agree+="${kind}/${mr} ${rid} (fixture ${av:0:60}; snapshot ${sv:0:60}); "
      ;;
  esac
done < <(expectation_rows "${HERE}/expect-adopt-nested.tsv")
[ -z "${bad_compile}" ] && record PASS "every expression of the adoption expectations compiles (${rows} rows)" \
  || record FAIL "every expression of the adoption expectations compiles" "${bad_compile}"
[ -z "${bad_shape}" ] && record PASS "every atProvider expression yields a value over the fixture it reads" \
  || record FAIL "every atProvider expression yields a value over the fixture it reads" "${bad_shape}"
if [ -z "${bad_agree}" ] && [ "${compared}" -ge 15 ]; then
  record PASS "the atProvider and snapshot expressions agree on the objects the stand-in API holds (${compared} rows)"
else
  record FAIL "the atProvider and snapshot expressions agree on the objects the stand-in API holds" "compared ${compared}: ${bad_agree}"
fi

# Verdict of a nested row.
v_ok=1
[ "$(nested_verdict - observe '["a"]' '["a"]')" = PASS ] || v_ok=0
[ "$(nested_verdict - observe '["a"]' '["b"]')" = FAIL ] || v_ok=0
[ "$(nested_verdict - observe '["a"]' null)" = FAIL ] || v_ok=0
[ "$(nested_verdict visibility observe '[]' '["a"]')" = NOT-MIRRORED ] || v_ok=0
[ "$(nested_verdict visibility full '[]' '["a"]')" = FAIL ] || v_ok=0
[ "$(nested_verdict visibility observe '["b"]' '["a"]')" = FAIL ] || v_ok=0
[ "${v_ok}" -eq 1 ] && record PASS "a nested row passes on equal values, fails on a difference or on nothing to compare, and not-mirrored only for an omitted visibility" \
  || record FAIL "a nested row passes on equal values, fails on a difference or on nothing to compare, and not-mirrored only for an omitted visibility"

# The request budget check reads GitHub's free rate-limit endpoint and stops a run that would starve.
budget() { # budget <minimum> -- exit status and message of require_rate_budget against the stand-in API
  ( export MIGRATION_API_URL="http://127.0.0.1:${PORT}" MIGRATION_GITHUB_TOKEN=selftest MIGRATION_MIN_RATE_BUDGET="$1"
    require_rate_budget ) 2>&1
}
if budget 4000 >/dev/null && ! budget 4500 >"${T}/budget.txt" && grep -q 'only 4200 GitHub requests are left' "${T}/budget.txt" && budget 0 >/dev/null; then
  record PASS "a scenario refuses to start when the hour's remaining GitHub requests are below the budget, and names the number"
else
  record FAIL "a scenario refuses to start when the hour's remaining GitHub requests are below the budget, and names the number" "$(head -2 "${T}/budget.txt" | tr '\n' ';')"
fi

# Comparing two snapshots into changed paths, and classifying them.
jq -c '.repos["pgh-mig-repo-main"].settings.description = "x"' "${T}/a.json" >"${T}/c1.json"
jq -c '.repos["pgh-mig-repo-main"].settings.description = "x" | .repos["pgh-mig-repo-main"]._ts.updated_at = "2027-01-01T00:00:00Z"' "${T}/a.json" >"${T}/c2.json"
jq -c '.repos["pgh-mig-repo-main"]._ts.updated_at = "2027-01-01T00:00:00Z"' "${T}/a.json" >"${T}/c3.json"
jq -c '.repos["pgh-mig-repo-main"].settings.description = "x" | .teams["pgh-mig-team-child"].privacy = "secret"' "${T}/a.json" >"${T}/c4.json"
printf '%s\n' '^repos/pgh-mig-repo-main/settings/description$' >"${T}/allowed.txt"
snapshot_changed_paths "${T}/a.json" "${T}/c1.json" >"${T}/p1.tsv"
snapshot_changed_paths "${T}/a.json" "${T}/c2.json" >"${T}/p2.tsv"
snapshot_changed_paths "${T}/a.json" "${T}/c3.json" >"${T}/p3.tsv"
snapshot_changed_paths "${T}/a.json" "${T}/c4.json" >"${T}/p4.tsv"
snapshot_changed_paths "${T}/a.json" "${T}/b.json" >"${T}/p0.tsv"
if [ "$(cut -f1 "${T}/p1.tsv")" = "repos/pgh-mig-repo-main/settings/description" ] && [ ! -s "${T}/p0.tsv" ] \
  && [ -z "$(classify_changes "${T}/p1.tsv" "${T}/allowed.txt")" ] \
  && [ -z "$(classify_changes "${T}/p2.tsv" "${T}/allowed.txt")" ] \
  && [ "$(classify_changes "${T}/p3.tsv" "${T}/allowed.txt")" = "REWRITE repos/pgh-mig-repo-main/_ts/updated_at" ] \
  && [ "$(classify_changes "${T}/p4.tsv" "${T}/allowed.txt")" = "UNEXPECTED teams/pgh-mig-team-child/privacy" ]; then
  record PASS "a change is intended when its path is allowed (with its own timestamp), a rewrite when only a timestamp moved, unexpected otherwise"
else
  record FAIL "a change is intended when its path is allowed (with its own timestamp), a rewrite when only a timestamp moved, unexpected otherwise" \
    "p1=$(cut -f1 "${T}/p1.tsv" | tr '\n' ,) p3=$(classify_changes "${T}/p3.tsv" "${T}/allowed.txt") p4=$(classify_changes "${T}/p4.tsv" "${T}/allowed.txt")"
fi

# Every change row names a fixture object and an allowed path that contains the required one.
bad_change=""
while IFS=$'\t' read -r kind mr patch check allowed required note; do
  if [ "${patch}" = "-" ]; then
    [ -n "${note}" ] && [ "${note}" != "-" ] || bad_change+="${kind}/${mr} is waived without a reason; "
    continue
  fi
  [ -n "$(fixture_for_provider "${kind}" "${mr}")" ] || bad_change+="${kind}/${mr} is not a v1 fixture; "
  jq -e . <<<"${patch}" >/dev/null 2>&1 || bad_change+="${kind}/${mr} patch is not JSON; "
  jq -n "${check}" >/dev/null 2>&1
  [ $? -ne 3 ] || bad_change+="${kind}/${mr} check does not compile; "
  [ -n "${required}" ] && [ -n "${allowed}" ] || bad_change+="${kind}/${mr} has no path; "
done < <(expectation_rows "${HERE}/expect-adopt-change.tsv")
[ -z "${bad_change}" ] && record PASS "every deliberate change names a v1 fixture, a JSON patch, a check and the paths it may change" \
  || record FAIL "every deliberate change names a v1 fixture, a JSON patch, a check and the paths it may change" "${bad_change}"

# Write counts from a provider log (the debug lines of crossplane-runtime).
cat >"${T}/provider.log" <<'LOG'
2026-10-09T21:39:20Z	DEBUG	provider-github	Reconciling	{"controller": "managed/team.organizations.github.crossplane.io", "request": {"name":"pgh-mig-team-parent"}}
2026-10-09T21:39:21Z	DEBUG	provider-github	Successfully requested update of external resource	{"controller": "managed/team.organizations.github.crossplane.io", "request": {"name":"pgh-mig-team-parent"}, "requeue-after": "x"}
2026-10-09T21:39:22Z	DEBUG	provider-github	Reconciling	{"controller": "managed/team.organizations.github.crossplane.io", "request": {"name":"pgh-mig-team-parent"}}
2026-10-09T21:39:23Z	DEBUG	provider-github	Successfully requested creation of external resource	{"controller": "managed/team.organizations.github.crossplane.io", "request": {"name":"pgh-mig-team-child"}}
2026-10-09T21:39:24Z	DEBUG	provider-github	Reconciling	{"controller": "managed/repository.organizations.github.crossplane.io", "request": {"name":"pgh-mig-team-parent"}}
LOG
counts="$(log_counts "${T}/provider.log" Team pgh-mig-team-parent)"
none="$(log_counts "${T}/provider.log" Team pgh-mig-absent)"
if [ "${counts}" = "0 1 2" ] && [ "${none}" = "0 0 0" ]; then
  record PASS "write counts are read per kind and object from the provider log, with the reconciles as the control"
else
  record FAIL "write counts are read per kind and object from the provider log, with the reconciles as the control" "got '${counts}' and '${none}'"
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
