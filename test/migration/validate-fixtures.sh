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
#      namespaced scope and nothing else;
#   6. the manifests the adoption scenario derives from the v1 fixtures
#      (adopt-derive.sh) validate against the candidate CRDs and their CEL rules,
#      in both modes (Observe-only, and the default management policies), and the
#      list of create-time fields the Observe-only derivation omits is exactly the
#      list of fields the candidate CRDs re-require;
#   7. the same for the namespaced scope, which also moves the objects into two
#      namespaces (one per kind of ProviderConfig), and the objects that sit beside the
#      adopted set: the reference twins (Ref and Selector fields in place of plain
#      strings, one that must not resolve), the cluster-scoped twins (one per kind) and
#      the two Teams of the both-scopes probe.
#
# Baseline CRDs come from MIGRATION_BASELINE_CRDS (a directory), else from
# MIGRATION_BASELINE_DIR/package/crds (a checkout of the baseline tag), else
# from this repository's history at MIGRATION_BASELINE_CRDS_REF (default
# 13ff658, the commit the baseline tag's CRDs are identical to).
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${HERE}/lib.sh"
# shellcheck source=adopt-derive.sh
. "${HERE}/adopt-derive.sh"
# Offline checks render the fixtures with the sample values, never with values a
# previous live run discovered.
# shellcheck disable=SC2034  # read by render and load_runtime_env in lib.sh
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

# ---------------------------------------------------------------------------
# The manifests the adoption scenario derives from the v1 fixtures
# ---------------------------------------------------------------------------
derive_adoption observe "${WORK}/rendered/v1" - "${WORK}/rendered/derived-observe"
derive_probe "${WORK}/rendered/derived-observe"
derive_adoption full "${WORK}/rendered/v1" - "${WORK}/rendered/derived-full"
kc_check "derived Observe-only adoption manifests validate against the candidate CRDs" "${WORK}/schemas-candidate" "${WORK}/rendered/derived-observe"
kc_check "derived fully managed adoption manifests validate against the candidate CRDs" "${WORK}/schemas-candidate" "${WORK}/rendered/derived-full"
cel_check "CEL rules of the candidate CRDs hold for the derived Observe-only manifests" "${CAND_CRDS}" "${WORK}/rendered/derived-observe"
cel_check "CEL rules of the candidate CRDs hold for the derived fully managed manifests" "${CAND_CRDS}" "${WORK}/rendered/derived-full"

# Nothing the baseline manages is left out of the derivation.
v1_count="$(for f in "${WORK}"/rendered/v1/*.yaml; do yq -o=json -I=0 '.' "${f}"; done | jq -r 'select(.kind != "Secret") | .kind' | wc -l)"
obs_count="$(find "${WORK}/rendered/derived-observe" -name '*.yaml' ! -name "*-${ADOPT_PROBE_NAME}.yaml" | wc -l)"
full_count="$(find "${WORK}/rendered/derived-full" -name '*.yaml' | wc -l)"
if [ "${v1_count}" -gt 0 ] && [ "${v1_count}" -eq "${obs_count}" ] && [ "${v1_count}" -eq "${full_count}" ]; then
  record PASS "one adoption manifest is derived per v1 managed resource (${v1_count})"
else
  record FAIL "one adoption manifest is derived per v1 managed resource" "v1 ${v1_count}, observe ${obs_count}, full ${full_count}"
fi

# The fields an Observe-only derivation omits are the ones the CEL rules re-require.
cel_fields="$(for crd in "${CAND_CRDS}"/organizations.github.crossplane.io_*.yaml; do
  yq -o=json '.' "${crd}" | jq -c '.spec.names.kind as $k
    | [.. | objects | select(has("x-kubernetes-validations")) | .["x-kubernetes-validations"][].rule
       | capture("has\\(self\\.spec\\.forProvider\\.(?<f>[A-Za-z0-9]+)\\)$").f] as $f
    | select(($f | length) > 0) | {($k): ($f | sort)}'
done | jq -sS 'add')"
want_fields="$(jq -S 'map_values(sort)' <<<"${ADOPT_OMITTED_FIELDS}")"
if [ "${cel_fields}" = "${want_fields}" ]; then
  record PASS "the fields the Observe-only derivation omits are exactly the ones the candidate CRDs re-require ($(jq '[.[] | length] | add' <<<"${want_fields}") fields)"
else
  record FAIL "the fields the Observe-only derivation omits are exactly the ones the candidate CRDs re-require" "CRDs: $(jq -c . <<<"${cel_fields}") derivation: $(jq -c . <<<"${want_fields}")"
fi

# An Observe-only manifest carries none of the omitted fields, no removed field, and the Observe policy.
bad_obs="$(for f in "${WORK}"/rendered/derived-observe/*.yaml; do
  yq -o=json -I=0 '.' "${f}" | jq -r --argjson omitted "${ADOPT_OMITTED_FIELDS}" '
    . as $d | [ (($omitted[$d.kind] // [])[] | select(. as $x | $d.spec.forProvider | has($x))),
                (if $d.spec.managementPolicies == ["Observe"] then empty else "managementPolicies" end),
                (if ($d.spec | has("publishConnectionDetailsTo") or has("providerRef")) then "removed field" else empty end) ]
    | select(length > 0) | "\($d.kind)/\($d.metadata.name): \(join(","))"'
done)"
if [ -z "${bad_obs}" ]; then
  record PASS "derived Observe-only manifests are Observe-only and omit the create-time fields"
else
  record FAIL "derived Observe-only manifests are Observe-only and omit the create-time fields" "$(printf '%s' "${bad_obs}" | head -3 | tr '\n' ';')"
fi

# ---------------------------------------------------------------------------
# The namespaced scope: the derived manifests, the reference twins, the cluster-scoped
# twins and the two Teams of the both-scopes probe
# ---------------------------------------------------------------------------
derive_adoption observe "${WORK}/rendered/v1" - "${WORK}/rendered/derived-ns-observe" namespaced
derive_probe "${WORK}/rendered/derived-ns-observe"
derive_adoption full "${WORK}/rendered/v1" - "${WORK}/rendered/derived-ns-full" namespaced
derive_ref_twins "${WORK}/rendered/derived-ns-observe" "${WORK}/rendered/ref-twins"
derive_cluster_twins "${WORK}/rendered/v1" - "${WORK}/rendered/derived-ns-observe" "${WORK}/rendered/cluster-twins"
derive_both_teams "${WORK}/rendered/both-teams"
kc_check "derived namespaced Observe-only manifests validate against the candidate CRDs" "${WORK}/schemas-candidate" "${WORK}/rendered/derived-ns-observe"
kc_check "derived namespaced fully managed manifests validate against the candidate CRDs" "${WORK}/schemas-candidate" "${WORK}/rendered/derived-ns-full"
kc_check "reference twins validate against the candidate CRDs" "${WORK}/schemas-candidate" "${WORK}/rendered/ref-twins"
kc_check "cluster-scoped twins validate against the candidate CRDs" "${WORK}/schemas-candidate" "${WORK}/rendered/cluster-twins"
kc_check "the Teams of the both-scopes probe validate against the candidate CRDs" "${WORK}/schemas-candidate" "${WORK}/rendered/both-teams"
cel_check "CEL rules of the candidate CRDs hold for the derived namespaced Observe-only manifests" "${CAND_CRDS}" "${WORK}/rendered/derived-ns-observe"
cel_check "CEL rules of the candidate CRDs hold for the derived namespaced fully managed manifests" "${CAND_CRDS}" "${WORK}/rendered/derived-ns-full"
cel_check "CEL rules of the candidate CRDs hold for the reference twins" "${CAND_CRDS}" "${WORK}/rendered/ref-twins"
cel_check "CEL rules of the candidate CRDs hold for the cluster-scoped twins" "${CAND_CRDS}" "${WORK}/rendered/cluster-twins"
cel_check "CEL rules of the candidate CRDs hold for the Teams of the both-scopes probe" "${CAND_CRDS}" "${WORK}/rendered/both-teams"

# No derived twin holds a connection secret reference: the object a twin duplicates is adopted
# and owns its Secret, and two managed resources must not publish into one Secret.
twin_secrets="$(grep -l writeConnectionSecretToRef "${WORK}"/rendered/ref-twins/*.yaml "${WORK}"/rendered/cluster-twins/*.yaml "${WORK}"/rendered/both-teams/*.yaml 2>/dev/null | xargs -r -n1 basename | paste -sd, -)"
if [ -z "${twin_secrets}" ] && [ -n "$(compgen -G "${WORK}/rendered/ref-twins/*.yaml")" ]; then
  record PASS "no reference twin, cluster-scoped twin or both-scopes Team holds a connection secret reference"
else
  record FAIL "no reference twin, cluster-scoped twin or both-scopes Team holds a connection secret reference" "${twin_secrets:-no reference twin was derived}"
fi

ns_obs_count="$(find "${WORK}/rendered/derived-ns-observe" -name '*.yaml' ! -name "*-${ADOPT_PROBE_NAME}.yaml" | wc -l)"
ns_full_count="$(find "${WORK}/rendered/derived-ns-full" -name '*.yaml' | wc -l)"
if [ "${v1_count}" -gt 0 ] && [ "${v1_count}" -eq "${ns_obs_count}" ] && [ "${v1_count}" -eq "${ns_full_count}" ]; then
  record PASS "one namespaced adoption manifest is derived per v1 managed resource (${v1_count})"
else
  record FAIL "one namespaced adoption manifest is derived per v1 managed resource" "v1 ${v1_count}, observe ${ns_obs_count}, full ${ns_full_count}"
fi

# Every derived namespaced object: its group, the namespace and ProviderConfig kind adopt_target
# gives it, a label of its own name for Selectors, a connection secret reference that is a bare
# name, secretKeyRef pointing at its own namespace; both namespaces and both ProviderConfig
# kinds are used; names are unique across the namespaces (the report keys on Kind/name).
bad_ns="$(for d in derived-ns-observe derived-ns-full; do
  for f in "${WORK}/rendered/${d}"/*.yaml; do
    yq -o=json -I=0 '.' "${f}" | jq -r --arg a "${ADOPT_NS_A}" --arg b "${ADOPT_NS_B}" '
      . as $d
      | [ (if ($d.apiVersion | test("\\.github\\.m\\.crossplane\\.io/")) then empty else "group" end),
          (if ([$a, $b] | index($d.metadata.namespace)) != null then empty else "namespace" end),
          (if $d.spec.providerConfigRef.kind == (if $d.metadata.namespace == $a then "ProviderConfig" else "ClusterProviderConfig" end) then empty else "providerConfigRef.kind" end),
          (if $d.metadata.labels["pgh-mig-target"] == $d.metadata.name or $d.metadata.name == "pgh-mig-adopt-drift" then empty else "target label" end),
          (if ($d.spec.writeConnectionSecretToRef // {name: "x"} | keys) == ["name"] then empty else "writeConnectionSecretToRef" end),
          ([$d.spec.forProvider | .. | objects | select(has("secretKeyRef")) | .secretKeyRef.namespace | select(. != $d.metadata.namespace)] | if length == 0 then empty else "secretKeyRef namespace" end) ]
      | select(length > 0) | "\($d.kind)/\($d.metadata.name): \(join(","))"'
  done
done)"
ns_used="$(for f in "${WORK}"/rendered/derived-ns-observe/*.yaml; do yq -o=json -I=0 '.' "${f}" | jq -r '"\(.metadata.namespace) \(.spec.providerConfigRef.kind)"'; done | sort -u | paste -sd, -)"
dup_names="$(for f in "${WORK}"/rendered/derived-ns-observe/*.yaml; do yq -o=json -I=0 '.' "${f}" | jq -r '.metadata.name'; done | sort | uniq -d | paste -sd, -)"
if [ -z "${bad_ns}" ] && [ -z "${dup_names}" ] && [ "${ns_used}" = "${ADOPT_NS_A} ProviderConfig,${ADOPT_NS_B} ClusterProviderConfig" ]; then
  record PASS "derived namespaced manifests sit in two namespaces, one per ProviderConfig kind, with unique names, own-namespace secret references and a target label"
else
  record FAIL "derived namespaced manifests sit in two namespaces, one per ProviderConfig kind, with unique names, own-namespace secret references and a target label" \
    "${bad_ns:+$(printf '%s' "${bad_ns}" | head -3 | tr '\n' ';')}${dup_names:+duplicate names: ${dup_names}; }used: ${ns_used}"
fi

# The omitted create-time fields are the ones the namespaced CRDs re-require as well.
ns_cel_fields="$(for crd in "${CAND_CRDS}"/organizations.github.m.crossplane.io_*.yaml; do
  yq -o=json '.' "${crd}" | jq -c '.spec.names.kind as $k
    | [.. | objects | select(has("x-kubernetes-validations")) | .["x-kubernetes-validations"][].rule
       | capture("has\\(self\\.spec\\.forProvider\\.(?<f>[A-Za-z0-9]+)\\)$").f] as $f
    | select(($f | length) > 0) | {($k): ($f | sort)}'
done | jq -sS 'add')"
if [ "${ns_cel_fields}" = "${want_fields}" ]; then
  record PASS "the namespaced CRDs re-require exactly the fields the Observe-only derivation omits"
else
  record FAIL "the namespaced CRDs re-require exactly the fields the Observe-only derivation omits" "CRDs: $(jq -c . <<<"${ns_cel_fields}") derivation: $(jq -c . <<<"${want_fields}")"
fi
bad_ns_obs="$(for f in "${WORK}"/rendered/derived-ns-observe/*.yaml; do
  yq -o=json -I=0 '.' "${f}" | jq -r --argjson omitted "${ADOPT_OMITTED_FIELDS}" '
    . as $d | [ (($omitted[$d.kind] // [])[] | select(. as $x | $d.spec.forProvider | has($x))),
                (if $d.spec.managementPolicies == ["Observe"] then empty else "managementPolicies" end),
                (if ($d.spec | has("publishConnectionDetailsTo") or has("providerRef")) then "removed field" else empty end) ]
    | select(length > 0) | "\($d.kind)/\($d.metadata.name): \(join(","))"'
done)"
if [ -z "${bad_ns_obs}" ]; then
  record PASS "derived namespaced Observe-only manifests are Observe-only and omit the create-time fields"
else
  record FAIL "derived namespaced Observe-only manifests are Observe-only and omit the create-time fields" "$(printf '%s' "${bad_ns_obs}" | head -3 | tr '\n' ';')"
fi

# The reference twins: Observe-only, every one holds at least one Ref or Selector, the plain
# strings they replace are gone, no twin carries the target label (a Selector would find it),
# each twin names the GitHub object of its source, and the cross-namespace twin sits in the
# namespace that does NOT hold the Organization it names.
twin_count="$(find "${WORK}/rendered/ref-twins" -name '*.yaml' | wc -l)"
bad_twin="$(while IFS=$'\t' read -r twin kind _ _ path _; do
  doc="$(yq -o=json -I=0 '.' "$(adopt_file "${WORK}/rendered/ref-twins" "${kind}" "${twin}")")"
  got="$(jq -c "${path}" <<<"${doc}")"
  case "${got}" in null | '[null'*) ;; *) echo "${twin} ${path} still holds ${got}" ;; esac
done <"${WORK}/rendered/ref-twins/checks.tsv"
for f in "${WORK}"/rendered/ref-twins/*.yaml; do
  yq -o=json -I=0 '.' "${f}" | jq -r '
    [ (if ([.. | objects | keys[] | select(test("Ref$|Selector$") and . != "secretKeyRef")] | length) > 0 then empty else "no Ref or Selector" end),
      (if .spec.managementPolicies == ["Observe"] then empty else "managementPolicies" end),
      (if (.metadata.labels // {}) | has("pgh-mig-target") then "target label" else empty end) ]
    | select(length > 0) | "\(.metadata.name): \(join(","))"'
done)"
xns_ns="$(jq -r 'select(.metadata.name == "pgh-mig-ref-xns-var") | .metadata.namespace' <(yq -o=json -I=0 '.' "${WORK}"/rendered/ref-twins/*-pgh-mig-ref-xns-var.yaml) 2>/dev/null)"
xns_target="$(jq -r 'select(.spec.forProvider.orgRef.name != null) | .spec.forProvider.orgRef.name' <(yq -o=json -I=0 '.' "${WORK}"/rendered/ref-twins/*-pgh-mig-ref-xns-var.yaml) 2>/dev/null)"
xns_target_ns="$(jq -r --arg n "${xns_target}" 'select(.kind == "Organization" and .metadata.name == $n) | .metadata.namespace' <(for f in "${WORK}"/rendered/derived-ns-observe/*.yaml; do yq -o=json -I=0 '.' "${f}"; done))"
if [ "${twin_count}" -eq 5 ] && [ -z "${bad_twin}" ] && [ -n "${xns_ns}" ] && [ -n "${xns_target_ns}" ] && [ "${xns_ns}" != "${xns_target_ns}" ]; then
  record PASS "five Observe-only reference twins replace plain strings with Ref and Selector fields; the cross-namespace twin names an object of the other namespace (${xns_ns} -> ${xns_target_ns})"
else
  record FAIL "five Observe-only reference twins replace plain strings with Ref and Selector fields; the cross-namespace twin names an object of the other namespace" \
    "twins ${twin_count}; ${bad_twin:+$(printf '%s' "${bad_twin}" | head -3 | tr '\n' ';')}; cross-namespace twin in '${xns_ns}', target in '${xns_target_ns}'"
fi

# The reference fields are the ones the candidate CRDs carry (so a renamed field is caught here).
ref_fields_missing="$(for fld in orgRef orgSelector parentRef userRef repoRef teamSelector userSelector; do
  grep -qE "^ +${fld}:" "${CAND_CRDS}"/organizations.github.m.crossplane.io_*.yaml || echo "${fld}"
done | paste -sd, -)"
if [ -z "${ref_fields_missing}" ]; then
  record PASS "the Ref and Selector fields the twins use exist in the namespaced candidate CRDs"
else
  record FAIL "the Ref and Selector fields the twins use exist in the namespaced candidate CRDs" "missing: ${ref_fields_missing}"
fi

# The cluster-scoped twins: one per kind, Observe-only, the external name of the source.
twin_kinds="$(for f in "${WORK}"/rendered/cluster-twins/*.yaml; do yq -o=json -I=0 '.' "${f}" | jq -r '.kind'; done | sort | uniq -c | awk '{print $1}' | sort -u | paste -sd, -)"
bad_cl="$(for f in "${WORK}"/rendered/cluster-twins/*.yaml; do
  yq -o=json -I=0 '.' "${f}" | jq -r '
    (.metadata.name | ltrimstr("pgh-mig-cl-")) as $src
    | [ (if (.apiVersion | test("\\.github\\.crossplane\\.io/")) then empty else "group" end),
        (if (.metadata.name | startswith("pgh-mig-cl-")) then empty else "name" end),
        (if .spec.managementPolicies == ["Observe"] then empty else "managementPolicies" end),
        (if .metadata.annotations["crossplane.io/external-name"] != null then empty else "external name" end) ]
    | select(length > 0) | "\(.kind)/\(.metadata.name): \(join(","))"'
done)"
cl_total="$(find "${WORK}/rendered/cluster-twins" -name '*.yaml' | wc -l)"
if [ "${cl_total}" -eq 9 ] && [ "${twin_kinds}" = 1 ] && [ -z "${bad_cl}" ]; then
  record PASS "one cluster-scoped Observe-only twin per kind (9), named pgh-mig-cl-<object>, with the external name of its source"
else
  record FAIL "one cluster-scoped Observe-only twin per kind (9), named pgh-mig-cl-<object>, with the external name of its source" "twins ${cl_total}, per kind ${twin_kinds}; ${bad_cl}"
fi

# The two Teams of the both-scopes probe: one GitHub team, two scopes, two descriptions, Orphan on both.
bt="$(for f in "${WORK}"/rendered/both-teams/*.yaml; do yq -o=json -I=0 '.' "${f}"; done | jq -s -r '
  if (length == 2)
     and (map(.metadata.annotations["crossplane.io/external-name"]) | unique | length == 1)
     and (map(.spec.forProvider.description) | unique | length == 2)
     and (map(select(.apiVersion | test("\\.m\\.") | not) | .spec.deletionPolicy) == ["Orphan"])
     and (map(select(.apiVersion | test("\\.m\\.")) | .spec.managementPolicies | index("Delete")) == [null])
     and (map(select(.apiVersion | test("\\.m\\.")) | .spec | has("deletionPolicy")) == [false])
  then "ok" else "bad" end')"
if [ "${bt}" = ok ]; then
  record PASS "the both-scopes Teams name one GitHub team, declare different descriptions and neither deletes the team"
else
  record FAIL "the both-scopes Teams name one GitHub team, declare different descriptions and neither deletes the team"
fi

summarize
