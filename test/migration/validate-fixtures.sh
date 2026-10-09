#!/usr/bin/env bash
# test/migration/validate-fixtures.sh -- offline validation of the migration
# fixtures against the CRDs. No cluster, no GitHub call.
#
# Checks, each reported as PASS/FAIL:
#   1. fixtures/v1/*.yaml validate (strict: unknown fields are errors) against
#      the baseline CRDs;
#   2. fixtures/adopt/cluster/*.yaml and fixtures/adopt/namespaced/*.yaml
#      validate against the candidate CRDs of the matching scope;
#   3. the CEL rules the CRDs carry hold for every fixture: a SecretAccess with
#      visibility `selected` lists repositories, and a create-time field that
#      the candidate makes optional is present unless the object's management
#      policies are Observe-only (kubeconform evaluates schemas, not CEL, so
#      these rules are evaluated here from the rule text);
#   4. the v1 fixtures, validated against the candidate CRDs, fail ONLY on the
#      two fields the move to crossplane-runtime v2 removes
#      (spec.publishConnectionDetailsTo, spec.providerRef) -- anything else
#      that no longer validates is an unannounced API break;
#   5. the namespaced adoption fixtures are the cluster ones moved to the
#      namespaced scope and nothing else.
#
# Baseline CRDs come from MIGRATION_BASELINE_CRDS (a directory), else from
# MIGRATION_BASELINE_DIR/package/crds (a checkout of the baseline tag), else
# from this repository's history at MIGRATION_BASELINE_CRDS_REF (default
# 13ff658, the commit the baseline tag's CRDs are identical to).
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${HERE}/lib.sh"
# Offline checks render the fixtures with the sample values, never with values a
# previous live run discovered.
RUNTIME_ENV=/nonexistent

require_tools kubeconform yq jq git sed
init_scenario fixtures

WORK="${EVIDENCE_DIR}/validate"
rm -rf "${WORK}"
mkdir -p "${WORK}"

# ---------------------------------------------------------------------------
# CRD directories
# ---------------------------------------------------------------------------
BASE_CRDS="${MIGRATION_BASELINE_CRDS:-}"
if [ -z "${BASE_CRDS}" ] && [ -n "${MIGRATION_BASELINE_DIR:-}" ] && [ -d "${MIGRATION_BASELINE_DIR}/package/crds" ]; then
  BASE_CRDS="${MIGRATION_BASELINE_DIR}/package/crds"
fi
if [ -z "${BASE_CRDS}" ]; then
  ref="${MIGRATION_BASELINE_CRDS_REF:-13ff658}"
  BASE_CRDS="${WORK}/baseline-crds"
  mkdir -p "${BASE_CRDS}"
  git -C "${PROVIDER_ROOT}" cat-file -e "${ref}^{commit}" 2>/dev/null \
    || die "baseline CRDs: commit ${ref} is not in this repository's history; set MIGRATION_BASELINE_CRDS or MIGRATION_BASELINE_DIR"
  for f in $(git -C "${PROVIDER_ROOT}" ls-tree --name-only "${ref}" package/crds/); do
    git -C "${PROVIDER_ROOT}" show "${ref}:${f}" >"${BASE_CRDS}/$(basename "${f}")"
  done
fi
CAND_CRDS="${MIGRATION_CANDIDATE_DIR}/package/crds"
[ -d "${CAND_CRDS}" ] || die "candidate CRDs not found at ${CAND_CRDS}"
log "baseline CRDs: ${BASE_CRDS}"
log "candidate CRDs: ${CAND_CRDS}"

# crds_to_schemas <crd-dir> <out-dir> -- writes <group>/<kind>_<version>.json (kind
# lower-cased, the spelling kubeconform looks up): the JSON schema kubeconform
# reads, from each CRD version's openAPIV3Schema. An API server prunes unknown
# fields; this validation rejects them instead (additionalProperties: false on
# every object that lists properties), so a misspelt or removed field is an
# error here rather than a silent no-op.
crds_to_schemas() {
  local dir="$1" out="$2" f line path
  mkdir -p "${out}"
  for f in "${dir}"/*.yaml; do
    yq -o=json '.' "${f}" | jq -r '
      . as $c | .spec.versions[] |
      "\($c.spec.group)/\($c.spec.names.kind | ascii_downcase)_\(.name).json\t\(
        .schema.openAPIV3Schema
        | walk(if type == "object" and .["x-kubernetes-int-or-string"] == true
               then del(.type) | . + {anyOf: [{type: "integer"}, {type: "string"}]}
               elif type == "object" and (.properties | type) == "object"
                    and (has("additionalProperties") | not)
                    and .["x-kubernetes-preserve-unknown-fields"] != true
               then . + {additionalProperties: false}
               else . end)
        | tojson)"' |
      while IFS=$'\t' read -r path line; do
        mkdir -p "${out}/$(dirname "${path}")"
        printf '%s' "${line}" >"${out}/${path}"
      done
  done
}
crds_to_schemas "${BASE_CRDS}" "${WORK}/schemas-baseline"
crds_to_schemas "${CAND_CRDS}" "${WORK}/schemas-candidate"

# ---------------------------------------------------------------------------
# Render every fixture with sample values
# ---------------------------------------------------------------------------
render_dir() { # render_dir <src-dir> <dst-dir>
  local f
  mkdir -p "$2"
  for f in "$1"/*.yaml; do
    render "${f}" "$2/$(basename "${f}")"
  done
}
render_dir "${FIXTURES_DIR}/v1" "${WORK}/rendered/v1"
render_dir "${FIXTURES_DIR}/adopt/cluster" "${WORK}/rendered/adopt-cluster"
render_dir "${FIXTURES_DIR}/adopt/namespaced" "${WORK}/rendered/adopt-namespaced"

# kc <schema-dir> <fixture-dir> -- kubeconform JSON report on stdout.
kc() {
  kubeconform -strict -verbose -skip Secret,Namespace -output json \
    -schema-location "$1/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json" "$2" 2>/dev/null
}

# kc_check <name> <schema-dir> <fixture-dir>
kc_check() {
  local name="$1" report invalid count
  report="$(kc "$2" "$3")" || true
  [ -n "${report}" ] || { record FAIL "${name}" "kubeconform produced no report"; return; }
  count="$(printf '%s' "${report}" | jq '[.resources[] | select(.status == "statusValid")] | length')"
  invalid="$(printf '%s' "${report}" | jq -r '.resources[] | select(.status != "statusValid" and .status != "statusSkipped") | "\(.kind)/\(.name): \(.status) \(.msg)"')"
  if [ -n "${invalid}" ]; then
    record FAIL "${name}" "$(printf '%s' "${invalid}" | head -5 | tr '\n' ';')"
    printf '%s\n' "${invalid}" >"${WORK}/${name// /_}.errors"
  elif [ "${count}" -eq 0 ]; then
    record FAIL "${name}" "no resources were validated"
  else
    record PASS "${name} (${count} resources)"
  fi
}

kc_check "v1 fixtures validate against the baseline CRDs" "${WORK}/schemas-baseline" "${WORK}/rendered/v1"
kc_check "adoption fixtures (cluster) validate against the candidate CRDs" "${WORK}/schemas-candidate" "${WORK}/rendered/adopt-cluster"
kc_check "adoption fixtures (namespaced) validate against the candidate CRDs" "${WORK}/schemas-candidate" "${WORK}/rendered/adopt-namespaced"

# ---------------------------------------------------------------------------
# CEL rules, evaluated from the rule text
# ---------------------------------------------------------------------------
# cel_check <name> <crd-dir> <fixture-dir>
# Rule shapes handled (these are the only two the CRDs carry):
#   self.visibility != 'selected' || (has(self.selectedRepositories) && ...)
#   !has(self.spec) || !has(self.spec.managementPolicies) || !(... 'Create' ...) || has(self.spec.forProvider.<field>)
cel_check() {
  local name="$1" crddir="$2" fxdir="$3" crd kind group rule field bad="" doc
  local rules="${WORK}/cel-rules.tsv"
  : >"${rules}"
  for crd in "${crddir}"/*.yaml; do
    yq -o=json '.' "${crd}" | jq -r '
      .spec.names.kind as $k | .spec.group as $g |
      [.. | objects | select(has("x-kubernetes-validations")) | .["x-kubernetes-validations"][].rule] | unique[] |
      "\($g)\t\($k)\t\(.)"' >>"${rules}"
  done
  # Flatten every fixture document to one JSON object per line.
  local docs="${WORK}/docs.jsonl"
  : >"${docs}"
  for f in "${fxdir}"/*.yaml; do
    yq -o=json -I=0 '.' "${f}" >>"${docs}"
  done
  while IFS=$'\t' read -r group kind rule; do
    case "${rule}" in
      *"has(self.spec.forProvider."*)
        field="$(printf '%s' "${rule}" | sed -E 's/.*has\(self\.spec\.forProvider\.([A-Za-z0-9]+)\)$/\1/')"
        bad+="$(jq -r --arg kind "${kind}" --arg g "${group}" --arg f "${field}" '
          select(.kind == $kind and (.apiVersion | startswith($g + "/"))) |
          (.spec.managementPolicies // ["*"]) as $mp |
          select(($mp | map(select(. == "*" or . == "Create" or . == "Update")) | length) > 0) |
          select((.spec.forProvider | has($f)) | not) |
          "\(.kind)/\(.metadata.name) lacks forProvider.\($f) but its management policies write"' "${docs}")"$'\n'
        ;;
      *"self.visibility != 'selected'"*)
        bad+="$(jq -r --arg kind "${kind}" --arg g "${group}" '
          select(.kind == $kind and (.apiVersion | startswith($g + "/"))) |
          select(.spec.forProvider.visibility == "selected") |
          select(((.spec.forProvider.selectedRepositories // []) | length) == 0) |
          "\(.kind)/\(.metadata.name) has visibility selected without selectedRepositories"' "${docs}")"$'\n'
        ;;
      *) bad+="unrecognised CEL rule on ${kind}: ${rule}"$'\n' ;;
    esac
  done <"${rules}"
  bad="$(printf '%s' "${bad}" | sed '/^$/d')"
  doc="$(wc -l <"${rules}") rule(s)"
  if [ -n "${bad}" ]; then
    record FAIL "${name}" "$(printf '%s' "${bad}" | head -5 | tr '\n' ';')"
  else
    record PASS "${name} (${doc})"
  fi
}
# The v1 fixtures write, so every create-time field is present (baseline has
# only the SecretAccess rule; candidate rules are checked against the adoption
# fixtures, which are Observe-only, and against the v1 fixtures as well).
cel_check "CEL rules of the baseline CRDs hold for the v1 fixtures" "${BASE_CRDS}" "${WORK}/rendered/v1"
cel_check "CEL rules of the candidate CRDs hold for the v1 fixtures" "${CAND_CRDS}" "${WORK}/rendered/v1"
cel_check "CEL rules of the candidate CRDs hold for the cluster adoption fixtures" "${CAND_CRDS}" "${WORK}/rendered/adopt-cluster"
cel_check "CEL rules of the candidate CRDs hold for the namespaced adoption fixtures" "${CAND_CRDS}" "${WORK}/rendered/adopt-namespaced"

# ---------------------------------------------------------------------------
# The v1 fixtures against the candidate CRDs: what the upgrade removes
# ---------------------------------------------------------------------------
report="$(kc "${WORK}/schemas-candidate" "${WORK}/rendered/v1")" || true
unexpected="$(printf '%s' "${report}" | jq -r '
  .resources[] | select(.status != "statusValid" and .status != "statusSkipped") |
  select((.msg | test("at ./spec.: additional properties (.(publishConnectionDetailsTo|providerRef).(, )?)+ not allowed$")) | not) |
  "\(.kind)/\(.name): \(.msg)"' 2>/dev/null)"
removed="$(printf '%s' "${report}" | jq -r '[.resources[] | select(.status != "statusValid" and .status != "statusSkipped") | "\(.kind)/\(.name)"] | join(" ")' 2>/dev/null)"
if [ -n "${unexpected}" ]; then
  record FAIL "v1 fixtures fail on the candidate CRDs only for the removed fields" "$(printf '%s' "${unexpected}" | head -5 | tr '\n' ';')"
else
  record PASS "v1 fixtures fail on the candidate CRDs only for the removed fields"
  record INFO "objects the candidate schema rejects (expected: publishConnectionDetailsTo, providerRef)" "${removed:-none}"
fi

# ---------------------------------------------------------------------------
# Namespaced adoption fixtures == cluster ones moved to the namespaced scope
# ---------------------------------------------------------------------------
# to_namespaced rewrites a cluster-scoped adoption document the way the scope
# change does: group, namespace, provider config reference.
to_namespaced() {
  yq -o=json -I=0 '.' "$1" | jq -S '
    .apiVersion |= sub("\\.github\\.crossplane\\.io/"; ".github.m.crossplane.io/")
    | .metadata.namespace = "default"
    | .spec.providerConfigRef = {"kind": "ClusterProviderConfig", "name": "default"}
    | del(.spec.deletionPolicy)'
}
parity_bad=""
for f in "${FIXTURES_DIR}"/adopt/cluster/*.yaml; do
  other="${FIXTURES_DIR}/adopt/namespaced/$(basename "${f}")"
  if [ ! -f "${other}" ]; then
    parity_bad+="$(basename "${f}") has no namespaced counterpart; "
    continue
  fi
  want="$(to_namespaced "${f}")"
  have="$(yq -o=json -I=0 '.' "${other}" | jq -S .)"
  [ "${want}" = "${have}" ] || parity_bad+="$(basename "${f}") differs from the cluster fixture beyond the scope change; "
done
if [ -n "${parity_bad}" ]; then
  record FAIL "namespaced adoption fixtures mirror the cluster ones" "${parity_bad}"
else
  record PASS "namespaced adoption fixtures mirror the cluster ones"
fi

summarize
