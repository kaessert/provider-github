#!/usr/bin/env bash
# test/migration/check-coverage.sh -- fixture coverage for the baseline schema.
#
# Usage: check-coverage.sh [--table]
#
# Offline. Two directions, both mechanical:
#   schema -> table   every nested object and array of objects in each baseline
#                     CRD's forProvider (references and selectors aside) has a
#                     row in coverage.tsv;
#   table -> fixtures every row is satisfied by at least one document of that
#                     kind in fixtures/v1.
# It also checks that the three visibilities all appear on OrganizationVariable
# and reports (without failing) the leaf fields of forProvider no fixture sets.
# --table prints the kind x sub-object table in Markdown, with the covering
# object, instead of the PASS/FAIL lines.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${HERE}/lib.sh"
# Offline checks render the fixtures with the sample values, never with values a
# previous live run discovered.
RUNTIME_ENV=/nonexistent

require_tools yq jq git
MODE="check"
[ "${1:-}" = "--table" ] && MODE="table"
init_scenario coverage
WORK="${EVIDENCE_DIR}/coverage"
rm -rf "${WORK}"
mkdir -p "${WORK}/crds"

BASE_CRDS="${MIGRATION_BASELINE_CRDS:-}"
if [ -z "${BASE_CRDS}" ] && [ -n "${MIGRATION_BASELINE_DIR:-}" ] && [ -d "${MIGRATION_BASELINE_DIR}/package/crds" ]; then
  BASE_CRDS="${MIGRATION_BASELINE_DIR}/package/crds"
fi
if [ -z "${BASE_CRDS}" ]; then
  ref="${MIGRATION_BASELINE_CRDS_REF:-13ff658}"
  git -C "${PROVIDER_ROOT}" cat-file -e "${ref}^{commit}" 2>/dev/null \
    || die "baseline CRDs: commit ${ref} is not in this repository's history; set MIGRATION_BASELINE_CRDS or MIGRATION_BASELINE_DIR"
  BASE_CRDS="${WORK}/crds"
  for f in $(git -C "${PROVIDER_ROOT}" ls-tree --name-only "${ref}" package/crds/); do
    git -C "${PROVIDER_ROOT}" show "${ref}:${f}" >"${BASE_CRDS}/$(basename "${f}")"
  done
fi

# Fixtures rendered with sample values, one JSON document per line.
DOCS="${WORK}/v1.jsonl"
: >"${DOCS}"
for f in "${FIXTURES_DIR}"/v1/*.yaml; do
  render "${f}" "${WORK}/rendered.yaml"
  yq -o=json -I=0 '.' "${WORK}/rendered.yaml" >>"${DOCS}"
done

# schema_paths prints "<Kind>\t<path>" for every sub-object of forProvider.
schema_paths() {
  local crd
  for crd in "${BASE_CRDS}"/organizations.github.crossplane.io_*.yaml; do
    yq -o=json '.' "${crd}" | jq -r '
      def w($p):
        (.properties // {}) | to_entries[]
        | select(.key | test("^(org|repo|user|team|parent)(Ref|Selector)$") | not)
        | . as $e | ($p + "." + $e.key) as $np
        | ($e.value
           | if .type == "array" and (.items.type == "object") then ($np + "[]"), (.items | w($np + "[]"))
             elif .type == "object" and .properties then $np, w($np)
             else empty end);
      .spec.names.kind as $k
      | .spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.forProvider
      | w(".spec.forProvider") | "\($k)\t\(.)"'
  done
}

# leaf_paths prints "<Kind>\t<path>" for every scalar or scalar-array field.
leaf_paths() {
  local crd
  for crd in "${BASE_CRDS}"/organizations.github.crossplane.io_*.yaml; do
    yq -o=json '.' "${crd}" | jq -r '
      def w($p):
        (.properties // {}) | to_entries[]
        | select(.key | test("^(org|repo|user|team|parent)(Ref|Selector)$") | not)
        | . as $e | ($p + "." + $e.key) as $np
        | ($e.value
           | if .type == "array" and (.items.type == "object") then (.items | w($np + "[]"))
             elif .type == "object" and .properties then w($np)
             else $np end);
      .spec.names.kind as $k
      | .spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.forProvider
      | w(".spec.forProvider") | "\($k)\t\(.)"'
  done
}

ROWS="${WORK}/rows.tsv"
grep -v "^#" "${MIGRATION_COVERAGE_TABLE:-${MIGRATION_DIR}/coverage.tsv}" | grep -v '^[[:space:]]*$' >"${ROWS}"

# covering <Kind> <path> -- names of the fixture objects that set the path.
covering() {
  jq -r --arg kind "$1" "select(.kind == \$kind) | select([${2}?] | map(select(. != null and . != {} and . != [])) | length > 0) | .metadata.name" "${DOCS}" \
    | sort -u | paste -sd, -
}

if [ "${MODE}" = "table" ]; then
  echo "| Kind | Nested sub-object | Set by |"
  echo "|---|---|---|"
  while IFS=$'\t' read -r kind path _; do
    # shellcheck disable=SC2016  # the backticks are Markdown
    printf '| %s | `%s` | %s |\n' "${kind}" "${path#.spec.}" "$(covering "${kind}" "${path}" | sed 's/,/, /g')"
  done <"${ROWS}"
  exit 0
fi

# schema -> table
missing="$(schema_paths | sort -u | while IFS=$'\t' read -r kind path; do
  grep -qxF "${kind}"$'\t'"${path}" < <(cut -f1,2 "${ROWS}" | sed 's/$//') || echo "${kind} ${path}"
done)"
if [ -n "${missing}" ]; then
  record FAIL "every baseline sub-object has a coverage row" "$(printf '%s' "${missing}" | tr '\n' ';')"
else
  record PASS "every baseline sub-object has a coverage row ($(schema_paths | sort -u | wc -l) sub-objects)"
fi

# table -> fixtures
unset_rows=""
while IFS=$'\t' read -r kind path _; do
  [ -n "$(covering "${kind}" "${path}")" ] || unset_rows+="${kind} ${path}; "
done <"${ROWS}"
if [ -n "${unset_rows}" ]; then
  record FAIL "every coverage row is set by a v1 fixture" "${unset_rows}"
else
  record PASS "every coverage row is set by a v1 fixture ($(wc -l <"${ROWS}") rows)"
fi

# Every visibility on OrganizationVariable.
vis="$(jq -r 'select(.kind == "OrganizationVariable") | .spec.forProvider.visibility' "${DOCS}" | sort -u | paste -sd, -)"
if [ "${vis}" = "all,private,selected" ]; then
  record PASS "OrganizationVariable fixtures cover every visibility"
else
  record FAIL "OrganizationVariable fixtures cover every visibility" "found: ${vis}"
fi

# Informational: leaf fields no fixture sets.
leaf_total=0
leaf_unset=""
while IFS=$'\t' read -r kind path; do
  leaf_total=$((leaf_total + 1))
  [ -n "$(covering "${kind}" "${path}")" ] || leaf_unset+="${kind}${path#.spec.forProvider}; "
done < <(leaf_paths | sort -u)
record INFO "leaf fields set by no v1 fixture ($((leaf_total)) leaf fields in the schema)" "${leaf_unset:-none}"

summarize
