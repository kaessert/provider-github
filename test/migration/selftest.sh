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
#   * the upgrade scenario's comparisons: a repository the baseline never reconciled may gain its settings
#     and branch protection with its IDs unchanged, one that was Synced and Ready may not, an Organization
#     reporting a drift: message is re-read before it counts as a regression, and an updated_at move is
#     attributed to a provider write or to somebody else;
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
for _ in $(seq 1 300); do [ -s "${T}/port" ] && break; sleep 0.1; done
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
# shellcheck source=cluster.sh
. "${HERE}/cluster.sh"
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

# The namespaced scope: the same v1 fixtures, moved to two namespaces and two ProviderConfig kinds.
derive_adoption observe "${T}/ex" - "${T}/ns-observe" namespaced
derive_adoption full "${T}/ex" - "${T}/ns-full" namespaced
fy() { yq "$1" "$(compgen -G "$2" | head -n1)"; } # fy <yq expression> <glob of one file>
ns_of() { yq '.metadata.namespace' "${T}/ns-observe"/*-"$1"-"$2".yaml; }
pc_of() { yq '.spec.providerConfigRef.kind + "/" + .spec.providerConfigRef.name' "${T}/ns-observe"/*-"$1"-"$2".yaml; }
if [ "$(ns_of team pgh-mig-team-child)" = "${ADOPT_NS_A}" ] && [ "$(pc_of team pgh-mig-team-child)" = "ProviderConfig/${ADOPT_PC_NAME}" ] \
  && [ "$(ns_of organizationvariable pgh-mig-var-all)" = "${ADOPT_NS_B}" ] && [ "$(pc_of organizationvariable pgh-mig-var-all)" = "ClusterProviderConfig/${ADOPT_CPC_NAME}" ] \
  && [ "$(ns_of repository pgh-mig-repo-fork)" = "${ADOPT_NS_B}" ] && [ "$(ns_of repository pgh-mig-repo-main)" = "${ADOPT_NS_A}" ] \
  && [ "$(fy '.apiVersion' "${T}/ns-observe"/*-team-pgh-mig-team-child.yaml)" = "organizations.github.m.crossplane.io/v1alpha1" ] \
  && [ "$(fy '.metadata.labels."pgh-mig-target"' "${T}/ns-observe"/*-team-pgh-mig-team-child.yaml)" = "pgh-mig-team-child" ]; then
  record PASS "the namespaced derivation spreads the objects over two namespaces, a ProviderConfig in one and a ClusterProviderConfig in the other, in the namespaced group"
else
  record FAIL "the namespaced derivation spreads the objects over two namespaces, a ProviderConfig in one and a ClusterProviderConfig in the other, in the namespaced group"
fi
hook_doc="${T}/ns-observe/*-organizationwebhook-pgh-mig-hook-wcs.yaml"
repo_doc="${T}/ns-observe/*-repository-pgh-mig-repo-main.yaml"
if [ "$(fy '.spec.writeConnectionSecretToRef | keys | join(",")' "${hook_doc}")" = name ] \
  && [ "$(fy '.spec.forProvider.secretKeyRef.namespace' "${hook_doc}")" = "${ADOPT_NS_B}" ] \
  && [ "$(fy '.spec.forProvider.webhooks[0].secretKeyRef.namespace' "${repo_doc}")" = "${ADOPT_NS_A}" ] \
  && [ "$(fy '.spec | has("publishConnectionDetailsTo")' "${T}/ns-observe"/*-organizationwebhook-pgh-mig-hook-ess.yaml)" = false ]; then
  record PASS "a namespaced object reads its secrets from its own namespace: bare connection secret name, secretKeyRef in the object's namespace, no external secret store"
else
  record FAIL "a namespaced object reads its secrets from its own namespace: bare connection secret name, secretKeyRef in the object's namespace, no external secret store"
fi
mship="${T}/ns-full/*-membership-pgh-mig-membership.yaml"
if [ "$(fy '.spec | has("deletionPolicy")' "${mship}")" = false ] && [ "$(fy '.spec.managementPolicies | contains(["Delete"])' "${mship}")" = false ] \
  && [ "$(fy '.spec.managementPolicies | contains(["Update"])' "${mship}")" = true ] \
  && [ "$(fy '.spec.managementPolicies[0]' "${T}/ns-full/"*-team-pgh-mig-team-parent.yaml)" = '*' ]; then
  record PASS "a namespaced kind has no deletionPolicy: the Membership the baseline kept with Orphan leaves Delete out of its management policies, the others keep the default"
else
  record FAIL "a namespaced kind has no deletionPolicy: the Membership the baseline kept with Orphan leaves Delete out of its management policies, the others keep the default"
fi

# Reference twins: plain strings replaced by Ref and Selector fields, checks that read the declared value.
derive_ref_twins "${T}/ns-observe" "${T}/refs"
rt_team="${T}/refs/*-team-pgh-mig-ref-team.yaml"
rt_repo="${T}/refs/*-repository-pgh-mig-ref-repo.yaml"
rt_xns="${T}/refs/*-organizationvariable-pgh-mig-ref-xns-var.yaml"
if [ "$(find "${T}/refs" -name '*.yaml' | wc -l)" -eq 5 ] \
  && [ "$(fy '.spec.forProvider.orgRef.name' "${rt_team}")" = pgh-mig-org ] && [ "$(fy '.spec.forProvider | has("org")' "${rt_team}")" = false ] \
  && [ "$(fy '.spec.forProvider.parentRef.name' "${rt_team}")" = pgh-mig-team-parent ] \
  && [ "$(fy '.spec.forProvider.members[0].userRef.name' "${rt_team}")" = pgh-mig-membership ] \
  && [ "$(fy '.spec.forProvider.orgSelector.matchLabels."pgh-mig-target"' "${rt_repo}")" = pgh-mig-org ] \
  && [ "$(fy '.spec.forProvider.permissions.teams[0].teamSelector.matchLabels."pgh-mig-target"' "${rt_repo}")" = pgh-mig-team-child ] \
  && [ "$(fy '.spec.forProvider.permissions.users[0].userSelector.matchLabels."pgh-mig-target"' "${rt_repo}")" = pgh-mig-membership ] \
  && [ "$(fy '.metadata.namespace' "${rt_xns}")" = "${ADOPT_NS_B}" ] && [ "$(fy '.spec.forProvider.orgRef.name' "${rt_xns}")" = pgh-mig-org ] \
  && [ "$(fy '.metadata | has("labels")' "${rt_team}")" = false ] \
  && [ "$(jq -r '.resolvable | join(",")' "${T}/refs/names.meta")" = "pgh-mig-ref-membership,pgh-mig-ref-org,pgh-mig-ref-repo,pgh-mig-ref-team" ] \
  && [ "$(jq -r '.unresolvable | join(",")' "${T}/refs/names.meta")" = pgh-mig-ref-xns-var ] \
  && [ "$(awk -F'\t' '$1 == "pgh-mig-ref-team" && $5 == ".spec.forProvider.parent" { print $6 }' "${T}/refs/checks.tsv")" = '"pgh-mig-team-parent"' ] \
  && [ "$(awk -F'\t' '$1 == "pgh-mig-ref-xns-var" { print $6 }' "${T}/refs/checks.tsv")" = null ]; then
  record PASS "the reference twins hold Ref and Selector fields in place of the plain strings, in the namespace of their targets, and the cross-namespace twin names a target of the other namespace"
else
  record FAIL "the reference twins hold Ref and Selector fields in place of the plain strings, in the namespace of their targets, and the cross-namespace twin names a target of the other namespace"
fi
# A twin whose target is missing from the adopted set is skipped.
rm -f "${T}/ns-observe"/*-team-pgh-mig-team-parent.yaml
derive_ref_twins "${T}/ns-observe" "${T}/refs-missing"
if [ -z "$(compgen -G "${T}/refs-missing/*-team-pgh-mig-ref-team.yaml")" ] && [ "$(find "${T}/refs-missing" -name '*.yaml' | wc -l)" -eq 4 ]; then
  record PASS "a reference twin is skipped when an object it points at is not in the adopted set"
else
  record FAIL "a reference twin is skipped when an object it points at is not in the adopted set"
fi
ref_ok=1
ref_value_equal '"Octocat"' '"octocat"' || ref_ok=0
ref_value_equal '["A","b"]' '["a","B"]' || ref_ok=0
ref_value_equal null '"x"' && ref_ok=0
ref_value_equal '["a"]' '["a","b"]' && ref_ok=0
[ "${ref_ok}" -eq 1 ] && record PASS "a resolved reference is compared with the declared value case-insensitively, and a missing one never matches" \
  || record FAIL "a resolved reference is compared with the declared value case-insensitively, and a missing one never matches"

# The cluster-scoped twins: one per kind, over the namespaced objects.
derive_adoption observe "${T}/ex" - "${T}/ns-observe-all" namespaced
derive_cluster_twins "${T}/ex" - "${T}/ns-observe-all" "${T}/cl-twins"
if [ "$(find "${T}/cl-twins" -name '*.yaml' | wc -l)" -eq 9 ] \
  && [ "$(fy '.metadata.name' "${T}/cl-twins"/*-team-pgh-mig-cl-pgh-mig-team-parent.yaml)" = pgh-mig-cl-pgh-mig-team-parent ] \
  && [ "$(fy '.metadata.annotations["crossplane.io/external-name"]' "${T}/cl-twins"/*-team-pgh-mig-cl-pgh-mig-team-parent.yaml)" = pgh-mig-team-parent ] \
  && [ "$(fy '.apiVersion' "${T}/cl-twins"/*-team-pgh-mig-cl-pgh-mig-team-parent.yaml)" = organizations.github.crossplane.io/v1alpha1 ] \
  && [ "$(fy '.spec.managementPolicies[0]' "${T}/cl-twins"/*-team-pgh-mig-cl-pgh-mig-team-parent.yaml)" = Observe ]; then
  record PASS "the cluster-scoped twin of an object is Observe-only, in the cluster group, with the same external name and a name of its own (9 kinds)"
else
  record FAIL "the cluster-scoped twin of an object is Observe-only, in the cluster group, with the same external name and a name of its own (9 kinds)"
fi
derive_both_teams "${T}/both"
if [ "$(fy '.metadata.annotations["crossplane.io/external-name"]' "${T}/both/10-team-namespaced.yaml")" = "${ADOPT_BOTH_TEAM}" ] \
  && [ "$(fy '.metadata.annotations["crossplane.io/external-name"]' "${T}/both/20-team-cluster.yaml")" = "${ADOPT_BOTH_TEAM}" ] \
  && [ "$(fy '.spec.forProvider.description' "${T}/both/10-team-namespaced.yaml")" != "$(fy '.spec.forProvider.description' "${T}/both/20-team-cluster.yaml")" ]; then
  record PASS "the two Teams of the both-scopes probe name one GitHub team and declare different descriptions"
else
  record FAIL "the two Teams of the both-scopes probe name one GitHub team and declare different descriptions"
fi

# The verdicts of the both-scopes probe, in words.
v_ok=1
[[ "$(both_scopes_observe_verdict 9 9 0)" == "no guard"* ]] || v_ok=0
[[ "$(both_scopes_observe_verdict 9 7 0)" == "the second scope is treated differently"* ]] || v_ok=0
[[ "$(both_scopes_observe_verdict 9 9 2)" == "the second scope is treated differently"* ]] || v_ok=0
[[ "$(both_scopes_observe_verdict 0 0 0)" == "not measured"* ]] || v_ok=0
[[ "$(both_scopes_write_verdict 3 2 True True)" == "NO GUARD"* ]] || v_ok=0
[[ "$(both_scopes_write_verdict 0 0 True True)" == "inconclusive"* ]] || v_ok=0
[[ "$(both_scopes_write_verdict 3 0 True False)" == "ONE SCOPE WROTE: only the namespaced"* ]] || v_ok=0
[[ "$(both_scopes_write_verdict 0 3 False True)" == "ONE SCOPE WROTE: only the cluster-scoped"* ]] || v_ok=0
[[ "$(both_scopes_summary x "$(both_scopes_write_verdict 3 2 True True)")" == "No. The provider has no guard"* ]] || v_ok=0
[[ "$(both_scopes_summary x "$(both_scopes_write_verdict 3 0 True False)")" == "Something stopped"* ]] || v_ok=0
[[ "$(both_scopes_summary x '')" == "Not measured"* ]] || v_ok=0
[ "${v_ok}" -eq 1 ] && record PASS "the both-scopes verdicts say a guard is absent only when both controllers wrote, and say plainly when nothing was measured" \
  || record FAIL "the both-scopes verdicts say a guard is absent only when both controllers wrote, and say plainly when nothing was measured"

# The ProviderConfig path of an object is read from its namespace and the kind of ProviderConfig it names.
printf '[{"kind":"Team","name":"a","namespace":"n1","providerConfigKind":"ProviderConfig"},{"kind":"Team","name":"b","namespace":"n2","providerConfigKind":"ClusterProviderConfig"},{"kind":"Team","name":"c","namespace":"","providerConfigKind":"ProviderConfig"}]' >"${T}/paths.json"
if [ "$(adopt_path_of "${T}/paths.json" Team a)" = "n1 (ProviderConfig)" ] && [ "$(adopt_path_of "${T}/paths.json" Team b)" = "n2 (ClusterProviderConfig)" ] \
  && [ "$(adopt_path_of "${T}/paths.json" Team c)" = cluster ] && [ "$(adopt_path_of "${T}/paths.json" Team z)" = - ]; then
  record PASS "the ProviderConfig path of an object is its namespace and the kind of ProviderConfig it names"
else
  record FAIL "the ProviderConfig path of an object is its namespace and the kind of ProviderConfig it names"
fi

# Helper objects are told from the adopted ones by name.
aux_ok=1
for n in pgh-mig-adopt-drift pgh-mig-ref-team pgh-mig-ref-xns-var pgh-mig-cl-pgh-mig-org pgh-mig-both-team; do
  jq -en --arg a "${ADOPT_AUX_RE}" --arg n "${n}" '$n | test($a)' >/dev/null || aux_ok=0
done
for n in pgh-mig-org pgh-mig-team-parent pgh-mig-repo-main pgh-mig-rg-selected pgh-mig-hook-plain pgh-mig-var-all; do
  jq -en --arg a "${ADOPT_AUX_RE}" --arg n "${n}" '$n | test($a)' >/dev/null && aux_ok=0
done
[ "${aux_ok}" -eq 1 ] && record PASS "the helper objects (drift probe, reference twins, cluster-scoped twins, both-scopes Teams) are told from the adopted ones by name" \
  || record FAIL "the helper objects (drift probe, reference twins, cluster-scoped twins, both-scopes Teams) are told from the adopted ones by name"

# Write counts of a namespaced controller: the group is part of the controller name, the namespace of the request.
cat >"${T}/provider-ns.log" <<'LOG'
2026-10-09T21:39:20Z	DEBUG	provider-github	Reconciling	{"controller": "managed/team.organizations.github.m.crossplane.io", "request": {"name":"pgh-mig-team-parent","namespace":"pgh-mig-ns-a"}}
2026-10-09T21:39:21Z	DEBUG	provider-github	Successfully requested update of external resource	{"controller": "managed/team.organizations.github.m.crossplane.io", "request": {"name":"pgh-mig-team-parent","namespace":"pgh-mig-ns-a"}}
2026-10-09T21:39:22Z	DEBUG	provider-github	Reconciling	{"controller": "managed/team.organizations.github.m.crossplane.io", "request": {"namespace":"pgh-mig-ns-a","name":"pgh-mig-team-parent"}}
2026-10-09T21:39:23Z	DEBUG	provider-github	Reconciling	{"controller": "managed/team.organizations.github.crossplane.io", "request": {"name":"pgh-mig-team-parent"}}
2026-10-09T21:39:24Z	DEBUG	provider-github	Successfully requested update of external resource	{"controller": "managed/team.organizations.github.crossplane.io", "request": {"name":"pgh-mig-team-parent"}}
2026-10-09T21:39:25Z	DEBUG	provider-github	Successfully requested update of external resource	{"controller": "managed/team.organizations.github.crossplane.io", "request": {"name":"pgh-mig-team-parent"}}
LOG
ns_counts="$(log_counts "${T}/provider-ns.log" Team pgh-mig-team-parent "${GROUP_NAMESPACED}")"
cl_counts="$(log_counts "${T}/provider-ns.log" Team pgh-mig-team-parent "${GROUP_CLUSTER}")"
# shellcheck disable=SC2034  # read by log_counts
ADOPT_GROUP="${GROUP_NAMESPACED}"
dflt_counts="$(log_counts "${T}/provider-ns.log" Team pgh-mig-team-parent)"
# shellcheck disable=SC2034  # read by log_counts
ADOPT_GROUP="${GROUP_CLUSTER}"
if [ "${ns_counts}" = "0 1 2" ] && [ "${cl_counts}" = "0 2 1" ] && [ "${dflt_counts}" = "0 1 2" ]; then
  record PASS "write counts tell the namespaced controller from the cluster-scoped one over the same object name, whatever the order of the request's fields"
else
  record FAIL "write counts tell the namespaced controller from the cluster-scoped one over the same object name, whatever the order of the request's fields" "namespaced '${ns_counts}', cluster '${cl_counts}', default '${dflt_counts}'"
fi

# --- leaving the baseline: the ProviderConfig goes first, every baseline CRD is awaited ----------
# A stand-in cluster in files. remove_provider takes the CRDs of the baseline package away EXCEPT a
# kind that still has an instance (a ProviderConfig the Provider is no longer there to release), and
# the CRD named in STICKY_CRD, which stays whatever happens.
SIM="${T}/sim"
sim_reset() { # sim_reset [sticky crd]
  rm -rf "${SIM}"
  mkdir -p "${SIM}"
  baseline_crds >"${SIM}/crds"
  [ "$(wc -l <"${SIM}/crds")" -ge 12 ] || die "the baseline CRD list is short: $(wc -l <"${SIM}/crds")"
  echo default >"${SIM}/providerconfigs"
  : >"${SIM}/order"
  STICKY_CRD="${1:-}"
}
kc() {
  case "$*" in
    "delete providerconfigs.github.crossplane.io --all --wait=false")
      echo delete-providerconfigs >>"${SIM}/order"
      grep -q running "${SIM}/state" 2>/dev/null && : >"${SIM}/providerconfigs" ;;
    "get providerconfigs.github.crossplane.io -o name") sed 's|^|providerconfig.github.crossplane.io/|' "${SIM}/providerconfigs" ;;
    "get crd/"*) grep -qx "${2#crd/}" "${SIM}/crds" ;;
    *) return 0 ;;
  esac
}
remove_provider() {
  echo remove-provider >>"${SIM}/order"
  : >"${SIM}/state"
  {
    [ -s "${SIM}/providerconfigs" ] && echo providerconfigs.github.crossplane.io
    [ -z "${STICKY_CRD}" ] || echo "${STICKY_CRD}"
  } >"${SIM}/keep"
  grep -Fx -f "${SIM}/keep" "${SIM}/crds" >"${SIM}/crds.left" || : >"${SIM}/crds.left"
  mv "${SIM}/crds.left" "${SIM}/crds"
}
wait_until() { shift 2; "$@"; } # one try: the stand-in cluster does not change while it waits
provider_pods_gone() { return 0; }
run_leave() { ( RESULTS_FILE="${T}/leave.tsv"; : >"${RESULTS_FILE}"; remove_baseline_provider ) >"${T}/leave.out" 2>&1; }

sim_reset
echo running >"${SIM}/state"
if run_leave && [ "$(paste -sd, "${SIM}/order")" = "delete-providerconfigs,remove-provider" ] && [ ! -s "${SIM}/crds" ]; then
  record PASS "leaving the baseline deletes its ProviderConfig before the Provider is removed, and no baseline CRD is left"
else
  record FAIL "leaving the baseline deletes its ProviderConfig before the Provider is removed, and no baseline CRD is left" \
    "order $(paste -sd, "${SIM}/order"), CRDs left: $(tr '\n' ' ' <"${SIM}/crds")"
fi
# Control: the stand-in reproduces the defect when the ProviderConfig is still there as the Provider goes.
sim_reset
echo running >"${SIM}/state"
remove_provider
if [ "$(baseline_crds_left)" = providerconfigs.github.crossplane.io ]; then
  record PASS "control: a ProviderConfig left behind keeps the CRD of its kind after the Provider is removed (the defect the ordering prevents)"
else
  record FAIL "control: a ProviderConfig left behind keeps the CRD of its kind after the Provider is removed" "left: $(baseline_crds_left | tr '\n' ' ')"
fi
sim_reset providerconfigusages.github.crossplane.io
echo running >"${SIM}/state"
run_leave
rc=$?
if [ "${rc}" -ne 0 ] && grep -q '^FAIL.*every CRD of the baseline package is gone.*still present: providerconfigusages.github.crossplane.io' "${T}/leave.tsv" \
  && ! grep -qi 'kept' "${T}/leave.tsv"; then
  record PASS "a baseline CRD that stays after the Provider was removed fails the scenario and is named, with no 'kept' path"
else
  record FAIL "a baseline CRD that stays after the Provider was removed fails the scenario and is named, with no 'kept' path" "rc ${rc}: $(cat "${T}/leave.tsv")"
fi
# (kc, remove_provider, wait_until and provider_pods_gone stay replaced: nothing below calls them.)

# The exclusion rule: only an object the baseline never created (not Synced) is left out of the adoption.
cat >"${T}/k8s-v1.json" <<'JSON'
[{"kind":"Team","name":"pgh-mig-team-parent","ready":"True","synced":"True"},
 {"kind":"Repository","name":"pgh-mig-repo-rules","ready":"False","synced":"True"},
 {"kind":"OrganizationVariable","name":"pgh-mig-var-policies","ready":"False","synced":"False"},
 {"kind":"OrganizationVariable","name":"pgh-mig-var-unknown","ready":"Unknown","synced":"Unknown"}]
JSON
baseline_excluded "${T}/k8s-v1.json" >"${T}/excluded.txt"
mkdir -p "${T}/excl"
for f in team-pgh-mig-team-parent repository-pgh-mig-repo-rules organizationvariable-pgh-mig-var-policies organizationvariable-pgh-mig-var-unknown; do : >"${T}/excl/10-${f}.yaml"; done
drop_excluded "${T}/excl" "${T}/excluded.txt"
if [ "$(paste -sd, "${T}/excluded.txt")" = "OrganizationVariable/pgh-mig-var-policies,OrganizationVariable/pgh-mig-var-unknown" ] \
  && [ "$(ls "${T}/excl" | paste -sd,)" = "10-repository-pgh-mig-repo-rules.yaml,10-team-pgh-mig-team-parent.yaml" ] \
  && [ "$(baseline_not_ready "${T}/k8s-v1.json")" = "Repository/pgh-mig-repo-rules: Ready=False Synced=True" ] \
  && [ "$(baseline_state "${T}/k8s-v1.json" Repository pgh-mig-repo-rules)" = "Ready=False Synced=True" ] \
  && [ "$(baseline_state "${T}/k8s-v1.json" Team pgh-mig-absent)" = "-" ]; then
  record PASS "only objects that are not Synced on the baseline are excluded; a Synced object that is not Ready is adopted and reported with its baseline state"
else
  record FAIL "only objects that are not Synced on the baseline are excluded; a Synced object that is not Ready is adopted and reported with its baseline state" \
    "excluded: $(paste -sd, "${T}/excluded.txt"); kept: $(ls "${T}/excl" | paste -sd,)"
fi

# --- the upgrade scenario: objects the baseline never reconciled ---------------------------------
# shellcheck source=upgrade-compare.sh
. "${HERE}/upgrade-compare.sh"
U="${T}/up"
# up_snapshot <out> <repo description> <main branch protection: yes|no> <repo id> <hook id> <repo updated_at> <org updated_at>
up_snapshot() {
  jq -n --arg d "$2" --arg bp "$3" --argjson id "$4" --argjson hook "$5" --arg rts "$6" --arg ots "$7" '
    def repo($n; $i; $desc; $prot; $ts): {id: $i, name: $n, settings: {description: $desc, has_wiki: ($prot == "yes")},
      branchProtection: (if $prot == "yes" then {main: {required_signatures: {enabled: true},
        restrictions: {teams: [{id: 20031733, parent: {id: 20031735}}], users: [{id: 25257851}]}}} else {} end),
      hooks: [{id: $hook, _ts: {updated_at: "h1"}}], _ts: {updated_at: $ts}};
    {org: {id: 9, description: "o", _ts: {updated_at: $ots}},
     repos: {"pgh-mig-repo-main": repo("pgh-mig-repo-main"; $id; $d; $bp; $rts),
             "pgh-mig-repo-archive": repo("pgh-mig-repo-archive"; 77; "kept"; "no"; "a1")},
     teams: {}, variables: {}, secrets: {actions: {}, dependabot: {}}, orgHooks: {}, runnerGroups: {}}' >"$1"
}
# up_case <name> <repo state on the baseline: ok|never> <after: description> <protection> <repo id after> <hook id after>
up_case() {
  local saved_evidence="${EVIDENCE_DIR}" saved_results="${RESULTS_FILE}"
  UP_DIR="${U}/$1"
  rm -rf "${UP_DIR}"
  mkdir -p "${UP_DIR}/snapshots"
  up_snapshot "${UP_DIR}/snapshots/after-v1.json" "declared-later" no 1413 695 "r1" "o1"
  up_snapshot "${UP_DIR}/snapshots/after-v2.json" "$3" "$4" "$5" "$6" "r2" "o1"
  cat >"${UP_DIR}/k8s-v1.json" <<'JSON'
[{"kind":"Repository","name":"pgh-mig-repo-main","externalName":"pgh-mig-repo-main","ready":"False","synced":"False"},
 {"kind":"Repository","name":"pgh-mig-repo-archive","externalName":"pgh-mig-repo-archive","ready":"True","synced":"True"}]
JSON
  : >"${UP_DIR}/baseline-not-ready.txt"
  [ "$2" != never ] || echo "Repository/pgh-mig-repo-main: Ready=False Synced=False observe failed: branch is not protected" >"${UP_DIR}/baseline-not-ready.txt"
  EVIDENCE_DIR="${UP_DIR}"
  RESULTS_FILE="${UP_DIR}/results.tsv"
  upgrade_prepare_snapshots
  EVIDENCE_DIR="${saved_evidence}"
  RESULTS_FILE="${saved_results}"
  UP_STRICT=0
  UP_LAX=0
  "${SNAP}" diff "${UP_DIR}/snapshots/${SNAP_BEFORE}.json" "${UP_DIR}/snapshots/${SNAP_AFTER}.json" >"${UP_DIR}/strict.txt" 2>&1 || UP_STRICT=1
  "${SNAP}" diff "${UP_DIR}/snapshots/${SNAP_BEFORE}.json" "${UP_DIR}/snapshots/${SNAP_AFTER}.json" --ignore-timestamps >"${UP_DIR}/lax.txt" 2>&1 || UP_LAX=1
}

# The reported case: the baseline never applied the repository's settings or protection; the candidate does,
# in one Update, and every ID is unchanged.
up_case never-reconciled never "applied" yes 1413 695
if [ "${UP_STRICT}${UP_LAX}" = 00 ] && [ -s "${UP_DIR}/unreconciled-pgh-mig-repo-main-delta.txt" ] \
  && grep -q '^+.*"has_wiki": true' "${UP_DIR}/unreconciled-pgh-mig-repo-main-delta.txt" \
  && grep -q '^+.*"required_signatures"' "${UP_DIR}/unreconciled-pgh-mig-repo-main-delta.txt" \
  && grep -q '^INFO.*completed a reconciliation the baseline never finished' "${UP_DIR}/results.tsv"; then
  record PASS "a repository the baseline never reconciled may gain its settings and branch protection: identical IDs pass both comparisons, the delta is INFO"
else
  record FAIL "a repository the baseline never reconciled may gain its settings and branch protection: identical IDs pass both comparisons, the delta is INFO" \
    "strict=${UP_STRICT} lax=${UP_LAX}: $(head -c 300 "${UP_DIR}/strict.txt" | tr '\n' ';')"
fi
up_case never-recreated never "applied" yes 9999 695
if [ "${UP_STRICT}${UP_LAX}" = 11 ] && grep -q 'numeric IDs differ' "${UP_DIR}/lax.txt"; then
  record PASS "a repository the baseline never reconciled still fails when it is recreated (new numeric ID)"
else
  record FAIL "a repository the baseline never reconciled still fails when it is recreated (new numeric ID)" "strict=${UP_STRICT} lax=${UP_LAX}"
fi
up_case never-hook never "applied" yes 1413 696
if [ "${UP_STRICT}${UP_LAX}" = 11 ] && grep -q 'numeric IDs differ' "${UP_DIR}/lax.txt"; then
  record PASS "a repository the baseline never reconciled still fails when its webhook is recreated"
else
  record FAIL "a repository the baseline never reconciled still fails when its webhook is recreated" "strict=${UP_STRICT} lax=${UP_LAX}"
fi
# The same changes on a repository that was Synced and Ready on the baseline are a write.
up_case ready-setting ok "applied" yes 1413 695
if [ "${UP_STRICT}${UP_LAX}" = 11 ] && grep -q '+.*"description": "applied"' "${UP_DIR}/lax.txt" && [ ! -e "${UP_DIR}/snapshots/after-v2-filtered.json" ]; then
  record PASS "a repository that was Synced and Ready on the baseline and changes a setting after the upgrade still fails"
else
  record FAIL "a repository that was Synced and Ready on the baseline and changes a setting after the upgrade still fails" "strict=${UP_STRICT} lax=${UP_LAX}"
fi
up_case ready-id ok "declared-later" no 9999 695
if [ "${UP_STRICT}${UP_LAX}" = 11 ] && grep -q 'numeric IDs differ' "${UP_DIR}/lax.txt"; then
  record PASS "a repository that was Synced and Ready on the baseline and is recreated after the upgrade still fails"
else
  record FAIL "a repository that was Synced and Ready on the baseline and is recreated after the upgrade still fails" "strict=${UP_STRICT} lax=${UP_LAX}"
fi
up_case ready-protection ok "declared-later" yes 1413 695
if [ "${UP_STRICT}${UP_LAX}" = 11 ] && grep -q 'required_signatures' "${UP_DIR}/lax.txt"; then
  record PASS "a repository that was Synced and Ready on the baseline and gains branch protection after the upgrade still fails"
else
  record FAIL "a repository that was Synced and Ready on the baseline and gains branch protection after the upgrade still fails" "strict=${UP_STRICT} lax=${UP_LAX}"
fi
# The other repository (Synced and Ready) keeps its strict comparison while the never-reconciled one is narrowed.
up_case never-and-ready never "applied" yes 1413 695
jq '.repos["pgh-mig-repo-archive"].settings.description = "changed"' "${UP_DIR}/snapshots/after-v2.json" >"${UP_DIR}/snapshots/x.json"
upgrade_filter_snapshot "${UP_DIR}/snapshots/x.json" "${UP_DIR}/snapshots/x-filtered.json" "" "pgh-mig-repo-main"
if ! "${SNAP}" diff "${UP_DIR}/snapshots/after-v1-filtered.json" "${UP_DIR}/snapshots/x-filtered.json" --ignore-timestamps >"${UP_DIR}/x.txt" 2>&1 \
  && grep -q '"description": "changed"' "${UP_DIR}/x.txt"; then
  record PASS "narrowing a never-reconciled repository leaves every other repository under the strict comparison"
else
  record FAIL "narrowing a never-reconciled repository leaves every other repository under the strict comparison" "$(head -c 200 "${UP_DIR}/x.txt")"
fi

# An Organization caught mid-correction: Ready=False with a "drift:" message, Synced=True.
cat >"${U}/k8s-a.json" <<'JSON'
[{"kind":"Organization","name":"pgh-mig-org","uid":"u1","externalName":"pgh-test","ready":"True","synced":"True","syncedMessage":"","readyMessage":""},
 {"kind":"Team","name":"pgh-mig-team","uid":"u2","externalName":"t","ready":"True","synced":"True","syncedMessage":"","readyMessage":""},
 {"kind":"OrganizationVariable","name":"pgh-mig-var","uid":"u3","externalName":"V","ready":"False","synced":"False","syncedMessage":"x","readyMessage":""}]
JSON
upgrade_v2() { # upgrade_v2 <out> <org ready> <org synced> <org ready message> <team ready>
  jq --arg r "$2" --arg s "$3" --arg m "$4" --arg t "$5" '
    map(if .kind == "Organization" then .ready = $r | .synced = $s | .readyMessage = $m
        elif .kind == "Team" then .ready = $t else . end)' "${U}/k8s-a.json" >"$1"
}
upgrade_v2 "${U}/k8s-drift.json" False True "drift: actions.enabledRepos: [a] != [b]" True
upgrade_v2 "${U}/k8s-other.json" False True "cannot update organization" True
upgrade_v2 "${U}/k8s-unsynced.json" False False "drift: x" True
upgrade_v2 "${U}/k8s-healed.json" True True "" True
upgrade_v2 "${U}/k8s-team.json" True True "" False
first="$(upgrade_k8s_compare "${U}/k8s-a.json" "${U}/k8s-drift.json")"
final="$(upgrade_k8s_compare "${U}/k8s-a.json" "${U}/k8s-drift.json" final)"
if [ "${first%%$'\t'*}" = "Organization/pgh-mig-org" ] && [[ "${first}" == *$'\t'drift-pending:* ]] \
  && [[ "${final}" == *$'\t'regressed:* ]] && [ -z "$(upgrade_k8s_compare "${U}/k8s-a.json" "${U}/k8s-healed.json")" ]; then
  record PASS "an Organization that was Ready and reports a drift: message is pending the re-read; still not Ready on the re-read it is a regression; Ready again it passes"
else
  record FAIL "an Organization that was Ready and reports a drift: message is pending the re-read; still not Ready on the re-read it is a regression; Ready again it passes" "first=${first} final=${final}"
fi
other="$(upgrade_k8s_compare "${U}/k8s-a.json" "${U}/k8s-other.json")"
unsynced="$(upgrade_k8s_compare "${U}/k8s-a.json" "${U}/k8s-unsynced.json")"
team="$(upgrade_k8s_compare "${U}/k8s-a.json" "${U}/k8s-team.json")"
if [[ "${other}" == *$'\t'regressed:* ]] && [[ "${unsynced}" == *$'\t'regressed:* ]] && [[ "${team}" == "Team/pgh-mig-team"$'\t'regressed:* ]]; then
  record PASS "only a Synced object with a drift: message is tolerated: another Ready message, Synced=False and any other kind not Ready are regressions"
else
  record FAIL "only a Synced object with a drift: message is tolerated: another Ready message, Synced=False and any other kind not Ready are regressions" "other=${other} unsynced=${unsynced} team=${team}"
fi
jq 'map(select(.kind != "Team"))' "${U}/k8s-a.json" >"${U}/k8s-gone.json"
if [[ "$(upgrade_k8s_compare "${U}/k8s-a.json" "${U}/k8s-gone.json")" == "Team/pgh-mig-team"$'\t'deleted ]]; then
  record PASS "a managed resource the upgrade deleted is still reported"
else
  record FAIL "a managed resource the upgrade deleted is still reported"
fi

# Provider writes told from other updated_at moves.
up_snapshot "${U}/ts-a.json" d no 1413 695 r1 o1
up_snapshot "${U}/ts-b.json" d no 1413 695 r2 o2
jq '.repos["pgh-mig-repo-archive"]._ts.updated_at = "a2"' "${U}/ts-b.json" >"${U}/ts-c.json"
cat >"${U}/k8s-ts.json" <<'JSON'
[{"kind":"Organization","name":"pgh-mig-org","externalName":"pgh-test"},
 {"kind":"Repository","name":"pgh-mig-repo-main","externalName":"pgh-mig-repo-main"},
 {"kind":"Repository","name":"pgh-mig-repo-archive","externalName":"pgh-mig-repo-archive"}]
JSON
cat >"${U}/candidate.log" <<'LOG'
2026-10-10T14:11:29Z	DEBUG	provider-github	Successfully requested update of external resource	{"controller": "managed/repository.organizations.github.crossplane.io", "request": {"name":"pgh-mig-repo-main"}}
2026-10-10T14:12:29Z	DEBUG	provider-github	Reconciling	{"controller": "managed/repository.organizations.github.crossplane.io", "request": {"name":"pgh-mig-repo-archive"}}
2026-10-10T14:12:30Z	DEBUG	provider-github	Reconciling	{"controller": "managed/organization.organizations.github.crossplane.io", "request": {"name":"pgh-mig-org"}}
LOG
rm -f "${U}/tables.md"
upgrade_write_attribution "${U}/ts-a.json" "${U}/ts-c.json" "${U}/k8s-ts.json" "${GROUP_CLUSTER}" "${U}/tables.md" "${U}/candidate.log"
if grep -q '^| repos/pgh-mig-repo-main/_ts/updated_at | r1 | r2 | provider write: 1 create/update' "${U}/tables.md" \
  && grep -q '^| repos/pgh-mig-repo-archive/_ts/updated_at | a1 | a2 | NOT a provider write: no create/update line for Repository/pgh-mig-repo-archive' "${U}/tables.md" \
  && grep -q '^| org/_ts/updated_at | o1 | o2 | NOT a provider write: no create/update line for Organization/pgh-mig-org' "${U}/tables.md" \
  && [ "${UPGRADE_TS_PROVIDER}/${UPGRADE_TS_OTHER}/${UPGRADE_TS_UNMATCHED}" = 1/2/0 ]; then
  record PASS "report.md tells an updated_at move on an object the provider updated from one on an object it did not (a reconcile is not an update)"
else
  record FAIL "report.md tells an updated_at move on an object the provider updated from one on an object it did not" "$(cat "${U}/tables.md" | tr '\n' ' ' | cut -c1-400)"
fi

# A run starts from a clean evidence directory: nothing the previous run left may narrow this run's
# comparison, count as this run's writes, or add a second table to the report.
up_case stale-baseline never "applied" yes 1413 695
cp "${U}/candidate.log" "${UP_DIR}/provider-candidate-old-pod.log"
cp "${U}/tables.md" "${UP_DIR}/report-tables.md"
echo "Repository/pgh-mig-repo-archive: Ready=False Synced=True" >"${UP_DIR}/k8s-regressions.txt"
saved_evidence="${EVIDENCE_DIR}"; saved_results="${RESULTS_FILE}"
EVIDENCE_DIR="${UP_DIR}"; RESULTS_FILE="${UP_DIR}/results.tsv"
upgrade_reset_run_state
: >"${RESULTS_FILE}"
upgrade_prepare_snapshots
cp "${UP_DIR}/snapshots/after-v1.json" "${UP_DIR}/snapshots/y.json"
upgrade_write_attribution "${U}/ts-a.json" "${U}/ts-c.json" "${U}/k8s-ts.json" "${GROUP_CLUSTER}" "${UP_DIR}/report-tables.md" "${U}/candidate.log"
EVIDENCE_DIR="${saved_evidence}"; RESULTS_FILE="${saved_results}"
if ! compgen -G "${UP_DIR}/provider-*.log" >/dev/null && [ ! -e "${UP_DIR}/baseline-not-ready.txt" ] \
  && [ ! -e "${UP_DIR}/k8s-regressions.txt" ] && [ ! -e "${UP_DIR}/unreconciled-pgh-mig-repo-main-delta.txt" ] \
  && [ ! -e "${UP_DIR}/snapshots/after-v1-filtered.json" ] && [ -e "${UP_DIR}/snapshots/y.json" ] \
  && [ "${SNAP_BEFORE}/${SNAP_AFTER}" = "after-v1/after-v2" ] \
  && [ ! -s "${UP_DIR}/results.tsv" ] && [ "$(grep -c '^## updated_at moves' "${UP_DIR}/report-tables.md")" = 1 ]; then
  record PASS "a new upgrade run clears the previous run's baseline list, provider logs, deltas, narrowed snapshots and report tables: a baseline that settled narrows nothing"
else
  record FAIL "a new upgrade run clears the previous run's baseline list, provider logs, deltas, narrowed snapshots and report tables: a baseline that settled narrows nothing" \
    "$(ls "${UP_DIR}" "${UP_DIR}/snapshots" | tr '\n' ' ') $(cat "${UP_DIR}/results.tsv" 2>/dev/null | head -c 200)"
fi

# --- the reruns of the cluster-scoped adoption: what the first full run showed ----------------------
# A directory of its own for the evidence these cases read and write; the run's is restored afterwards.
# scenario.sh (assert_snapshots_identical) sources lib.sh again, which resets the run's state: keep it.
saved_evidence="${EVIDENCE_DIR}"; saved_results="${RESULTS_FILE}"; saved_scenario="${SCENARIO}"
# shellcheck source=scenario.sh
. "${HERE}/scenario.sh"
EVIDENCE_DIR="${saved_evidence}"; RESULTS_FILE="${saved_results}"; SCENARIO="${saved_scenario}"
# shellcheck disable=SC2034  # read by render in lib.sh
RUNTIME_ENV=/nonexistent
rec() { local r="${RESULTS_FILE}"; RESULTS_FILE="${saved_results}"; record "$@"; RESULTS_FILE="${r}"; } # the verdicts go to the run's results
RR="${T}/rerun"
rm -rf "${RR}"
mkdir -p "${RR}/snapshots"
EVIDENCE_DIR="${RR}"; RESULTS_FILE="${RR}/results.tsv"; : >"${RESULTS_FILE}"

# The connection secrets the baseline wrote come back for the objects that name one, in both scopes.
cat >"${RR}/baseline-conn-secrets.json" <<'JSON'
[{"apiVersion":"v1","kind":"Secret","type":"connection.crossplane.io/v1alpha1","data":{"secret":"YXBwbGllZA=="},"metadata":{"name":"pgh-mig-hook-wcs-conn"}},
 {"apiVersion":"v1","kind":"Secret","type":"connection.crossplane.io/v1alpha1","data":{"secret":"cmVwbw=="},"metadata":{"name":"pgh-mig-repo-main-conn"}},
 {"apiVersion":"v1","kind":"Secret","type":"connection.crossplane.io/v1alpha1","data":{"x":"eA=="},"metadata":{"name":"pgh-mig-unrelated-conn"}}]
JSON
KCLOG="${RR}/applied.jsonl"
kc() { # records what is applied: one JSON document per line
  case "$1" in
    apply) yq -o=json -I=0 '.' >>"${KCLOG}" ;;
    *) return 0 ;;
  esac
}
restored() { # restored -- "<namespace>/<name>:<data keys>" of everything applied, sorted
  jq -r '"\(.metadata.namespace)/\(.metadata.name):\((.data // {}) | keys | join(","))"' "${KCLOG}" 2>/dev/null | sort | paste -sd' '
}
: >"${KCLOG}"
adopt_restore_conn_secrets cluster "${T}/derived" 2>/dev/null
cluster_restored="$(restored)"
: >"${KCLOG}"
adopt_restore_conn_secrets namespaced "${T}/ns-observe" 2>/dev/null
ns_restored="$(restored)"
if [ "${cluster_restored}" = "crossplane-system/pgh-mig-hook-wcs-conn:secret crossplane-system/pgh-mig-repo-main-conn:secret" ]; then
  rec PASS "cluster scope: the baseline's connection secrets named by the Observe manifests are applied into the namespace the manifests name, and no others (a secret the baseline never wrote is not invented)"
else
  rec FAIL "cluster scope: the baseline's connection secrets named by the Observe manifests are applied into the namespace the manifests name, and no others" "got: ${cluster_restored}"
fi
: >"${KCLOG}"
adopt_restore_conn_secrets cluster "${T}/derived" 2>/dev/null
cluster_data="$(jq -r 'select(.metadata.name == "pgh-mig-hook-wcs-conn") | .data.secret' "${KCLOG}")"
if [ "${cluster_data}" = "YXBwbGllZA==" ] \
  && [ "${ns_restored}" = "pgh-mig-ns-a/pgh-mig-repo-main-conn:secret pgh-mig-ns-b/pgh-mig-hook-ess-wcs-conn: pgh-mig-ns-b/pgh-mig-hook-wcs-conn:secret" ]; then
  rec PASS "namespaced scope: the baseline's connection secrets go into the namespace of the object that names them, with their data; one the baseline never wrote is created empty"
else
  rec FAIL "namespaced scope: the baseline's connection secrets go into the namespace of the object that names them, with their data; one the baseline never wrote is created empty" "cluster data ${cluster_data}; namespaced: ${ns_restored}"
fi
mkdir -p "${RR}/noconn"
cp "${T}/derived"/*-team-pgh-mig-team-parent.yaml "${RR}/noconn/"
: >"${KCLOG}"
adopt_restore_conn_secrets cluster "${RR}/noconn" 2>/dev/null
adopt_restore_conn_secrets namespaced "${RR}/noconn" 2>/dev/null
if [ ! -s "${KCLOG}" ]; then
  rec PASS "a manifest without writeConnectionSecretToRef restores nothing, in either scope"
else
  rec FAIL "a manifest without writeConnectionSecretToRef restores nothing, in either scope" "applied: $(restored)"
fi

# The Ready message of every object that is not Ready, with the reason, is in the report.
cat >"${RR}/k8s-observe.json" <<'JSON'
[{"kind":"Organization","name":"pgh-mig-org","namespace":"","providerConfigKind":"ProviderConfig","ready":"False","synced":"True","readyReason":"Unavailable","readyMessage":"drift: description: GitHub has \"a|b\", spec declares \"c\"","syncedMessage":""},
 {"kind":"Team","name":"pgh-mig-team-parent","namespace":"","providerConfigKind":"ProviderConfig","ready":"True","synced":"True","readyReason":"Available","readyMessage":"","syncedMessage":""},
 {"kind":"Repository","name":"pgh-mig-repo-main","namespace":"","providerConfigKind":"ProviderConfig","ready":"False","synced":"False","readyReason":"","readyMessage":"","syncedMessage":"observe failed: x"}]
JSON
for t in mr nested change refs both notready; do : >"${RR}/table-${t}.tsv"; done
adopt_note_not_ready observe "${RR}/k8s-observe.json"
write_adopt_tables
if [ "$(wc -l <"${RR}/table-notready.tsv")" -eq 2 ] \
  && grep -qF '| observe | cluster | Organization | pgh-mig-org | False | Unavailable | True | drift: description: GitHub has "a\|b", spec declares "c" | - |' "${RR}/report-tables.md" \
  && grep -qF '| observe | cluster | Repository | pgh-mig-repo-main | False | - | False | - | observe failed: x |' "${RR}/report-tables.md" \
  && ! grep -q 'pgh-mig-team-parent' "${RR}/table-notready.tsv"; then
  rec PASS "the report prints the Ready reason, the Ready message and the Synced message of every object that is not Ready, and nothing for a Ready one"
else
  rec FAIL "the report prints the Ready reason, the Ready message and the Synced message of every object that is not Ready, and nothing for a Ready one" "$(cat "${RR}/table-notready.tsv")"
fi

# --- a changed team's description, echoed inside the parent object of its child under branch protection
# Two repositories' protections list the child team (restrictions, reviews); GitHub embeds the parent.
echo_snapshot() { # echo_snapshot <out> <parent description> <child team description> <parent slug>
  jq -c --arg pd "$2" --arg cd "$3" --arg ps "$4" '
    .repos["pgh-mig-repo-main"].branchProtection = {main: {
      restrictions: {teams: [{slug: "pgh-mig-team-child", description: $cd, parent: {slug: $ps, description: $pd}}]},
      required_pull_request_reviews: {
        dismissal_restrictions: {teams: [{slug: "pgh-mig-team-child", description: $cd, parent: {slug: $ps, description: $pd}}]},
        bypass_pull_request_allowances: {teams: [{slug: "pgh-mig-team-child", description: $cd, parent: {slug: $ps, description: $pd}}]}}}}' \
    "${T}/a.json" >"$1"
}
echo_snapshot "${RR}/echo-before.json" "old parent" "child" pgh-mig-team-parent
echo_snapshot "${RR}/echo-after.json" "pgh-mig parent team (changed)" "child" pgh-mig-team-parent
printf '%s\n' '^teams/pgh-mig-team-parent/description$' >"${RR}/echo-allowed.txt"
team_echo_allowed "${RR}/echo-after.json" pgh-mig-team-parent '"pgh-mig parent team (changed)"' >>"${RR}/echo-allowed.txt"
classify_with() { # classify_with <before> <after> <slug> <json value> -- UNEXPECTED/REWRITE lines
  local allowed="${RR}/allowed.$$.txt"
  printf '%s\n' '^teams/pgh-mig-team-parent/description$' >"${allowed}"
  team_echo_allowed "$2" "$3" "$4" >>"${allowed}"
  snapshot_changed_paths "$1" "$2" >"${RR}/paths.tsv"
  classify_changes "${RR}/paths.tsv" "${allowed}"
}
jq -c '.teams["pgh-mig-team-parent"].description = "pgh-mig parent team (changed)"' "${RR}/echo-after.json" >"${RR}/echo-after-team.json"
jq -c '.teams["pgh-mig-team-parent"].description = "old parent"' "${RR}/echo-before.json" >"${RR}/echo-before-team.json"
allowed_lines="$(grep -c . "${RR}/echo-allowed.txt")"
if [ -z "$(classify_with "${RR}/echo-before-team.json" "${RR}/echo-after-team.json" pgh-mig-team-parent '"pgh-mig parent team (changed)"')" ] && [ "${allowed_lines}" -eq 4 ]; then
  rec PASS "the changed team's new description is allowed where GitHub echoes it as the parent of a team under branch protection (restrictions, dismissal restrictions, bypass allowances)"
else
  rec FAIL "the changed team's new description is allowed where GitHub echoes it as the parent of a team under branch protection" "$(classify_with "${RR}/echo-before-team.json" "${RR}/echo-after-team.json" pgh-mig-team-parent '"pgh-mig parent team (changed)"' | head -3 | tr '\n' ';') allowed regexes: ${allowed_lines}"
fi
# A parent echo holding another value than the one the harness set is a change.
echo_snapshot "${RR}/echo-other-value.json" "somebody else's text" "child" pgh-mig-team-parent
jq -c '.teams["pgh-mig-team-parent"].description = "pgh-mig parent team (changed)"' "${RR}/echo-other-value.json" >"${RR}/echo-other-value-team.json"
other_value="$(classify_with "${RR}/echo-before-team.json" "${RR}/echo-other-value-team.json" pgh-mig-team-parent '"pgh-mig parent team (changed)"')"
# The description of the child team itself, in the same place, is not the parent's echo.
echo_snapshot "${RR}/echo-child.json" "pgh-mig parent team (changed)" "child edited" pgh-mig-team-parent
jq -c '.teams["pgh-mig-team-parent"].description = "pgh-mig parent team (changed)"' "${RR}/echo-child.json" >"${RR}/echo-child-team.json"
child_desc="$(classify_with "${RR}/echo-before-team.json" "${RR}/echo-child-team.json" pgh-mig-team-parent '"pgh-mig parent team (changed)"')"
# A parent that is another team than the changed one.
echo_snapshot "${RR}/echo-other-parent.json" "pgh-mig parent team (changed)" "child" pgh-mig-team-other
jq -c '.teams["pgh-mig-team-parent"].description = "pgh-mig parent team (changed)"' "${RR}/echo-other-parent.json" >"${RR}/echo-other-parent-team.json"
other_parent="$(classify_with "${RR}/echo-before-team.json" "${RR}/echo-other-parent-team.json" pgh-mig-team-parent '"pgh-mig parent team (changed)"')"
# Any other path under the same protection.
jq -c '.repos["pgh-mig-repo-main"].branchProtection.main.restrictions.teams[0].parent.privacy = "secret"' "${RR}/echo-after-team.json" >"${RR}/echo-other-path.json"
other_path="$(classify_with "${RR}/echo-after-team.json" "${RR}/echo-other-path.json" pgh-mig-team-parent '"pgh-mig parent team (changed)"')"
if [ "$(grep -c '^UNEXPECTED repos/pgh-mig-repo-main/branchProtection/main/.*/parent/description$' <<<"${other_value}")" -eq 3 ] \
  && [ "$(grep -c '^UNEXPECTED repos/pgh-mig-repo-main/branchProtection/main/.*/teams/0/description$' <<<"${child_desc}")" -eq 3 ] \
  && [ "$(grep -c '^UNEXPECTED .*/parent/description$' <<<"${child_desc}")" -eq 0 ] \
  && [ "$(grep -c '^UNEXPECTED .*/parent/description$' <<<"${other_parent}")" -eq 3 ] \
  && [ "$(grep -c '^UNEXPECTED .*/parent/slug$' <<<"${other_parent}")" -eq 3 ] \
  && [ "${other_path}" = "UNEXPECTED repos/pgh-mig-repo-main/branchProtection/main/restrictions/teams/0/parent/privacy" ]; then
  rec PASS "a parent echo with another value, the description of the child team itself, a different parent and any other path under branch protection are still unexpected changes"
else
  rec FAIL "a parent echo with another value, the description of the child team itself, a different parent and any other path under branch protection are still unexpected changes" \
    "value: $(head -c 150 <<<"${other_value}" | tr '\n' ';') child: $(head -c 150 <<<"${child_desc}" | tr '\n' ';') parent: $(head -c 150 <<<"${other_parent}" | tr '\n' ';') path: ${other_path}"
fi

# --- the Organization the external writer resets ------------------------------------------------------
cat >"${RR}/baseline.json" <<'JSON'
{"org":{"id":9,"description":"pgh-test","actionsEnabledRepos":[{"id":2,"name":"demo-repository-2"},{"id":1,"name":"demo-repository-1"}]}}
JSON
org_baseline_load "${RR}/baseline.json"
SIG_MSG="drift: actions.enabledRepos: GitHub has [demo-repository-1 demo-repository-2], spec declares [pgh-mig-repo-main pgh-mig-repo-rules]"
m_ok=1
org_reset_match Organization False True "${SIG_MSG}" || m_ok=0
org_reset_match Organization False True "drift: actions.enabledRepos: GitHub has [demo-repository-1 demo-repository-2 other], spec declares [x]" && m_ok=0
org_reset_match Organization False True "drift: description: GitHub has \"pgh-test\", spec declares \"x\"" && m_ok=0
org_reset_match Organization False True "" && m_ok=0
org_reset_match Organization True True "${SIG_MSG}" && m_ok=0
org_reset_match Organization False False "${SIG_MSG}" && m_ok=0
org_reset_match Repository False True "${SIG_MSG}" && m_ok=0
if [ "${m_ok}" -eq 1 ] && [ "${ORG_RESET_PREFIX}" = "drift: actions.enabledRepos: GitHub has [demo-repository-1 demo-repository-2]" ]; then
  rec PASS "the external-reset signature is the drift message of the baseline's enabled repositories on an Organization that is Synced and Ready=False; another list, another message, another state and another kind are not"
else
  rec FAIL "the external-reset signature is the drift message of the baseline's enabled repositories on an Organization that is Synced and Ready=False; another list, another message, another state and another kind are not" "prefix '${ORG_RESET_PREFIX}' m_ok ${m_ok}"
fi

# adopt_check_state over one Organization (or another kind) and a reconciled Team.
ADOPT_SCOPE=cluster; ADOPT_GROUP="${GROUP_CLUSTER}"; ADOPT_WINDOW_SINCE="2026-10-10T15:00:00Z"
: "${ADOPT_SCOPE}${ADOPT_GROUP}${ADOPT_WINDOW_SINCE}" # read by adopt_check_state and the org helpers
BASE_SAMPLE=$'2026-10-10T15:20:00Z\tpgh-test\t["demo-repository-1","demo-repository-2"]'
FIX_SAMPLE=$'2026-10-10T15:21:00Z\tpgh-mig organization description\t["pgh-mig-repo-main","pgh-mig-repo-rules"]'
# state_case <phase> <kind> <ready> <synced> <ready message> <samples...> -- sets CASE_FAILS, CASE_WARNS, CASE_INFOS
state_case() {
  local phase="$1" kind="$2" ready="$3" synced="$4" msg="$5"
  shift 5
  : >"${RESULTS_FILE}"
  : >"${RR}/org-samples.log"
  [ "$#" -eq 0 ] || printf '%s\n' "$@" >"${RR}/org-samples.log"
  jq -n --arg k "${kind}" --arg r "${ready}" --arg s "${synced}" --arg m "${msg}" '
    [{kind: $k, name: "pgh-mig-obj", namespace: "", providerConfigKind: "ProviderConfig", ready: $r, synced: $s, readyMessage: $m, readyReason: "", syncedMessage: "",
      atProviderId: "pgh-mig-obj", externalName: "pgh-mig-obj"}]' >"${RR}/k8s-${phase}.json"
  mkdir -p "${RR}/snapshots"
  echo '{}' >"${RR}/snapshots/adopted.json"; echo '{}' >"${RR}/snapshots/full.json"
  {
    printf '2026-10-10T15:25:00Z\tDEBUG\tprovider-github\tReconciling\t{"controller": "managed/%s.organizations.github.crossplane.io", "request": {"name":"pgh-mig-obj"}}\n' "$(tr 'A-Z' 'a-z' <<<"${kind}")"
    [ -z "${CASE_UPDATE_AT:-}" ] || printf '%s\tDEBUG\tprovider-github\tSuccessfully requested update of external resource\t{"controller": "managed/%s.organizations.github.crossplane.io", "request": {"name":"pgh-mig-obj"}}\n' "${CASE_UPDATE_AT}" "$(tr 'A-Z' 'a-z' <<<"${kind}")"
  } >"${RR}/window-${phase}.log"
  for t in mr nested change refs both notready; do : >"${RR}/table-${t}.tsv"; done
  adopt_check_state "${phase}" 2>/dev/null
  CASE_FAILS="$(grep -c '^FAIL' "${RESULTS_FILE}" || true)"
  CASE_WARNS="$(grep -c '^WARN' "${RESULTS_FILE}" || true)"
  CASE_INFOS="$(grep -c '^INFO' "${RESULTS_FILE}" || true)"
}
CASE_UPDATE_AT=""
state_case observe Organization False True "${SIG_MSG}" "${FIX_SAMPLE}" "${BASE_SAMPLE}"
obs_reset="${CASE_FAILS}/${CASE_WARNS}"; obs_warn_text="$(grep '^WARN' "${RESULTS_FILE}")"
state_case observe Organization False True "${SIG_MSG}" "${FIX_SAMPLE}"
obs_nosample="${CASE_FAILS}/${CASE_WARNS}"
state_case observe Organization False True "${SIG_MSG}"
obs_nolog="${CASE_FAILS}/${CASE_WARNS}"
state_case observe Organization False True "drift: description: GitHub has \"pgh-test\", spec declares \"x\"" "${BASE_SAMPLE}"
obs_other_msg="${CASE_FAILS}/${CASE_WARNS}"
state_case observe Repository False True "${SIG_MSG}" "${BASE_SAMPLE}"
obs_repo="${CASE_FAILS}/${CASE_WARNS}"
state_case observe Organization False False "${SIG_MSG}" "${BASE_SAMPLE}"
obs_unsynced="${CASE_FAILS}/${CASE_WARNS}"
state_case observe Organization True True "" "${FIX_SAMPLE}"
obs_ready="${CASE_FAILS}/${CASE_WARNS}"
state_case observe Organization False True "${SIG_MSG}" $'2026-10-09T15:20:00Z\tpgh-test\t["demo-repository-1","demo-repository-2"]'
obs_old_sample="${CASE_FAILS}/${CASE_WARNS}"
if [ "${obs_reset}" = 0/1 ] && [[ "${obs_warn_text}" == *"reset to its baseline by something outside the cluster"* ]] && [[ "${obs_warn_text}" == *"2026-10-10T15:20:00Z | pgh-test"* ]] \
  && [ "${obs_ready}" = 0/0 ]; then
  rec PASS "an Observe Organization with the baseline signature is a WARN naming the sample that shows the baseline values, not a failure"
else
  rec FAIL "an Observe Organization with the baseline signature is a WARN naming the sample that shows the baseline values, not a failure" "fails/warns ${obs_reset}, ready ${obs_ready}: ${obs_warn_text:0:300}"
fi
if [ "${obs_nosample}" = 1/0 ] && [ "${obs_nolog}" = 1/0 ] && [ "${obs_old_sample}" = 1/0 ] && [ "${obs_other_msg}" = 1/0 ] && [ "${obs_repo}" = 1/0 ] && [ "${obs_unsynced}" = 1/0 ]; then
  rec PASS "the signature without a sample of the window showing the baseline values, another drift message, a Repository with the same signature and an unsynced Organization are all failures"
else
  rec FAIL "the signature without a sample of the window showing the baseline values, another drift message, a Repository with the same signature and an unsynced Organization are all failures" \
    "no sample ${obs_nosample}, no samples at all ${obs_nolog}, sample before the window ${obs_old_sample}, other message ${obs_other_msg}, repository ${obs_repo}, unsynced ${obs_unsynced}"
fi

# Under full management: the provider's Organization update is the correction of the reset only when a
# sample within the poll before it shows the baseline values.
CASE_UPDATE_AT="2026-10-10T15:26:10Z"
state_case full Organization True True "" $'2026-10-10T15:25:40Z\tpgh-test\t["demo-repository-1","demo-repository-2"]'
full_attributed="${CASE_FAILS}/${CASE_INFOS}"
state_case full Organization True True ""
full_nosample="${CASE_FAILS}"
state_case full Organization True True "" $'2026-10-10T15:24:00Z\tpgh-test\t["demo-repository-1","demo-repository-2"]'
full_early="${CASE_FAILS}"
state_case full Organization True True "" "${FIX_SAMPLE}" $'2026-10-10T15:25:40Z\tpgh-mig organization description\t["pgh-mig-repo-main","pgh-mig-repo-rules"]'
full_fixture_values="${CASE_FAILS}"
state_case full Team True True "" $'2026-10-10T15:25:40Z\tpgh-test\t["demo-repository-1","demo-repository-2"]'
full_team="${CASE_FAILS}"
state_case observe Organization True True "" $'2026-10-10T15:25:40Z\tpgh-test\t["demo-repository-1","demo-repository-2"]'
observe_update="${CASE_FAILS}"
CASE_UPDATE_AT=""
if [ "${full_attributed}" = 0/1 ] && [ "${full_nosample}" = 1 ] && [ "${full_early}" = 1 ] && [ "${full_fixture_values}" = 1 ]; then
  rec PASS "under full management an Organization update is attributed to the external reset (INFO) only when a sample within the previous poll shows the baseline values; without one it is a FAIL"
else
  rec FAIL "under full management an Organization update is attributed to the external reset (INFO) only when a sample within the previous poll shows the baseline values; without one it is a FAIL" \
    "attributed ${full_attributed}, no sample ${full_nosample}, sample older than a poll ${full_early}, samples at the declared values ${full_fixture_values}"
fi
if [ "${full_team}" = 1 ] && [ "${observe_update}" = 1 ]; then
  rec PASS "every other kind keeps the strict write check, and an Organization update under Observe is a FAIL whatever the samples show"
else
  rec FAIL "every other kind keeps the strict write check, and an Organization update under Observe is a FAIL whatever the samples show" "team ${full_team}, observe ${observe_update}"
fi

# The zero-write comparison leaves the organization's own values out only with a sample of the baseline
# and no Organization write the reset does not explain.
jq -c '.org.description = "pgh-test" | .org.actionsEnabledRepos = [{id: 1, name: "demo-repository-1"}] | .org._ts.updated_at = "2026-10-10T14:57:17Z"' "${T}/a.json" >"${RR}/snapshots/adopted.json"
cp "${T}/a.json" "${RR}/snapshots/orphaned.json"
jq -c '.repos["pgh-mig-repo-main"].settings.description = "moved"' "${RR}/snapshots/adopted.json" >"${RR}/snapshots/adopted-repo.json"
zero_case() { # zero_case <phase> <snapshot b> <update at|-> <samples...> -- PASS/FAIL of the comparison
  local phase="$1" b="$2" upd="$3"
  shift 3
  : >"${RESULTS_FILE}"
  : >"${RR}/org-samples.log"
  [ "$#" -eq 0 ] || printf '%s\n' "$@" >"${RR}/org-samples.log"
  : >"${RR}/window-${phase}.log"
  [ "${upd}" = - ] || printf '%s\tDEBUG\tprovider-github\tSuccessfully requested update of external resource\t{"controller": "managed/organization.organizations.github.crossplane.io", "request": {"name":"pgh-mig-org"}}\n' "${upd}" >"${RR}/window-${phase}.log"
  adopt_assert_zero_write "zero-write case" orphaned "${b}" "${phase}" 2>/dev/null
  awk -F'\t' '$2 == "zero-write case" { print $1 }' "${RESULTS_FILE}"
}
z_exempt="$(zero_case observe adopted - "${BASE_SAMPLE}")"
z_nosample="$(zero_case observe adopted - "${FIX_SAMPLE}")"
z_update="$(zero_case observe adopted 2026-10-10T15:21:00Z "${BASE_SAMPLE}")"
z_repo="$(zero_case observe adopted-repo - "${BASE_SAMPLE}")"
z_full_attr="$(zero_case full adopted 2026-10-10T15:20:30Z "${BASE_SAMPLE}")"
z_full_unattr="$(zero_case full adopted 2026-10-10T15:26:10Z "${BASE_SAMPLE}")"
if [ "${z_exempt}" = PASS ] && [ "${z_nosample}" = FAIL ] && [ "${z_update}" = FAIL ] && [ "${z_repo}" = FAIL ] && [ "${z_full_attr}" = PASS ] && [ "${z_full_unattr}" = FAIL ]; then
  rec PASS "the org description, Actions settings and updated_at leave the zero-write comparison only with a baseline sample and no unexplained Organization write; a change anywhere else still fails it"
else
  rec FAIL "the org description, Actions settings and updated_at leave the zero-write comparison only with a baseline sample and no unexplained Organization write; a change anywhere else still fails it" \
    "exempt ${z_exempt}, no baseline sample ${z_nosample}, Observe update ${z_update}, repository moved ${z_repo}, full attributed ${z_full_attr}, full unattributed ${z_full_unattr}"
fi

# An Organization the external writer reset is settled for the wait loops (the cluster cannot heal it).
mr_state() { cat "${RR}/mr-state.json"; }
jq -n --arg m "${SIG_MSG}" '[{kind:"Organization",name:"pgh-mig-org",ready:"False",synced:"True",readyMessage:$m},{kind:"Team",name:"pgh-mig-team-parent",ready:"True",synced:"True",readyMessage:""}]' >"${RR}/mr-state.json"
settled_org=0; adopt_settled "${GROUP_CLUSTER}" 2 && settled_org=1
jq -n '[{kind:"Organization",name:"pgh-mig-org",ready:"False",synced:"True",readyMessage:"drift: description: GitHub has x"},{kind:"Team",name:"pgh-mig-team-parent",ready:"True",synced:"True",readyMessage:""}]' >"${RR}/mr-state.json"
settled_other=1; adopt_settled "${GROUP_CLUSTER}" 2 && settled_other=0
jq -n --arg m "${SIG_MSG}" '[{kind:"Organization",name:"pgh-mig-org",ready:"True",synced:"True",readyMessage:""},{kind:"Repository",name:"pgh-mig-repo-main",ready:"False",synced:"True",readyMessage:$m}]' >"${RR}/mr-state.json"
settled_repo=1; adopt_settled "${GROUP_CLUSTER}" 2 && settled_repo=0
if [ "${settled_org}" = 1 ] && [ "${settled_other}" = 1 ] && [ "${settled_repo}" = 1 ]; then
  rec PASS "waiting for the adopted objects accepts an Organization reset to its baseline as settled, and nothing else that is not Ready"
else
  rec FAIL "waiting for the adopted objects accepts an Organization reset to its baseline as settled, and nothing else that is not Ready" "reset org ${settled_org}, other drift ${settled_other}, repository ${settled_repo}"
fi
unset -f mr_state

# The sampler's two requests: a sample line is "<timestamp>\t<description>\t<repositories>".
org_samp="$(MIGRATION_API_URL="http://127.0.0.1:${PORT}" MIGRATION_GITHUB_TOKEN=selftest MIGRATION_ORG=pgh-test org_sample_once 2>/dev/null)"
if [ "$(awk -F'\t' '{ print NF }' <<<"${org_samp}")" -eq 3 ] && jq -e . >/dev/null 2>&1 <<<"$(cut -f3 <<<"${org_samp}")" && [[ "${org_samp}" == 20??-??-??T??:??:??Z$'\t'* ]]; then
  rec PASS "a sample of the organization is a timestamp, the description and the Actions-enabled repositories, read with two GETs"
else
  rec FAIL "a sample of the organization is a timestamp, the description and the Actions-enabled repositories, read with two GETs" "got: ${org_samp:0:200}"
fi
EVIDENCE_DIR="${saved_evidence}"; RESULTS_FILE="${saved_results}"

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
