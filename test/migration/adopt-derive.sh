#!/usr/bin/env bash
# test/migration/adopt-derive.sh -- derives the adoption manifests from the v1
# fixtures. Sourced by adopt-common.sh, validate-fixtures.sh and selftest.sh; not
# executable on its own. Pure functions: no cluster and no GitHub call.
#
# The adoption scenario does not keep a second set of manifests that could drift
# from the v1 ones. Every object the baseline provider created is adopted by ONE
# new cluster-scoped managed resource derived from its v1 fixture:
#
#   observe   managementPolicies [Observe]; the create-time fields the candidate
#             lets an Observe-only object omit are removed; the external name is
#             set from the live baseline object; the fields the move to
#             crossplane-runtime v2 removes (publishConnectionDetailsTo,
#             providerRef) are replaced by providerConfigRef;
#   full      the v1 forProvider as it is (it matches GitHub: the baseline
#             created GitHub from it) under the default management policies.
#
# shellcheck shell=bash

# ADOPT_OMITTED_FIELDS: per kind, the create-time forProvider fields the candidate CRDs re-require
# by CEL unless managementPolicies is Observe-only. validate-fixtures.sh checks
# this list against the CEL rules of the candidate CRDs, so it cannot go stale.
ADOPT_OMITTED_FIELDS='{
  "ActionsSecretAccess": ["visibility"],
  "DependabotSecretAccess": ["visibility"],
  "Membership": ["role"],
  "Organization": ["description"],
  "OrganizationVariable": ["value", "visibility"],
  "OrganizationWebhook": ["url", "contentType", "events"],
  "RunnerGroup": ["visibility"]
}'

ADOPT_PROBE_NAME="pgh-mig-adopt-drift"
ADOPT_PROBE_SOURCE="Team/pgh-mig-team-parent"
ADOPT_PROBE_DESCRIPTION="pgh-mig declared description that differs from GitHub"

# derive_adoption <observe|full> <rendered-v1-dir> <external-names.json|-> <out-dir>
#
# Writes one YAML file per managed resource of the v1 fixtures (Secrets and other
# prerequisites are skipped) to <out-dir>. <external-names.json> maps
# "Kind/name" to {"externalName": "..."} as read from the live baseline objects
# (mr_state output); "-" derives the external names offline from the fixtures
# (a webhook gets the placeholder hook ID 1).
derive_adoption() {
  local mode="$1" src="$2" names="$3" out="$4" f doc kind name n=0 names_json='{}'
  case "${mode}" in observe | full) ;; *) die "derive_adoption: unknown mode ${mode}" ;; esac
  if [ "${names}" != "-" ]; then
    names_json="$(jq -c 'map({key: "\(.kind)/\(.name)", value: {externalName}}) | from_entries' "${names}")" \
      || die "cannot read the external names from ${names}"
  fi
  mkdir -p "${out}"
  rm -f "${out}"/*.yaml
  for f in "${src}"/*.yaml; do
    while IFS= read -r doc; do
      kind="$(jq -r '.kind' <<<"${doc}")"
      case "${kind}" in
        Organization | Membership | Team | Repository | OrganizationVariable | OrganizationWebhook | RunnerGroup | ActionsSecretAccess | DependabotSecretAccess) ;;
        *) continue ;;
      esac
      name="$(jq -r '.metadata.name' <<<"${doc}")"
      n=$((n + 1))
      jq --arg mode "${mode}" --argjson names "${names_json}" --argjson omitted "${ADOPT_OMITTED_FIELDS}" '
        . as $d
        | "\($d.kind)/\($d.metadata.name)" as $k
        | ((($names[$k].externalName // "") | if . == "" then null else . end)
            // $d.metadata.annotations["crossplane.io/external-name"]
            // (if $d.kind == "OrganizationWebhook" then "1" else $d.metadata.name end)) as $ext
        | .metadata = {name: $d.metadata.name, annotations: {"crossplane.io/external-name": $ext}}
        | .spec |= (
            del(.publishConnectionDetailsTo, .providerRef)
            | .providerConfigRef = {name: "default"}
            | if $mode == "observe"
              then .managementPolicies = ["Observe"]
                   | .forProvider |= reduce ($omitted[$d.kind] // [])[] as $field (.; del(.[$field]))
              else .managementPolicies = ["*"] end)' <<<"${doc}" \
        | yq -P '.' >"${out}/$(printf '%02d' "${n}")-$(printf '%s' "${kind}" | tr 'A-Z' 'a-z')-${name}.yaml" \
        || die "cannot derive ${kind}/${name}"
    done < <(yq -o=json -I=0 '.' "${f}")
  done
}

# derive_probe <derived-observe-dir> -- adds the drift probe to an observe set: a
# second Observe-only Team over the parent team whose declared description
# differs from GitHub's. An Observe-only object must report the difference
# (Ready=False) and write nothing. Prints nothing and adds nothing when the
# source team is not in the set.
derive_probe() {
  local dir="$1" src
  src="$(grep -l "^  name: ${ADOPT_PROBE_SOURCE#*/}\$" "${dir}"/*-team-*.yaml 2>/dev/null | head -n1)"
  [ -n "${src}" ] || return 0
  yq -P ".metadata.name = \"${ADOPT_PROBE_NAME}\" | .spec.forProvider.description = \"${ADOPT_PROBE_DESCRIPTION}\"" "${src}" \
    >"${dir}/99-team-${ADOPT_PROBE_NAME}.yaml"
}

# ---------------------------------------------------------------------------
# Expectation tables: evaluation helpers (pure, shared with selftest.sh)
# ---------------------------------------------------------------------------

# eval_expr <jq program> <json file> [<jq prefix program>]
# Prints the compact, key-sorted value of the program over the file (optionally
# after the prefix program), or ERROR when jq rejects it.
eval_expr() {
  local out
  out="$(jq -cS "${3:-.} | (${1})" "$2" 2>/dev/null)" || { echo ERROR; return 0; }
  printf '%s' "${out:-null}"
}

# nested_verdict <condition> <phase> <atProvider value> <snapshot value>
# PASS when the observation reports what the snapshot holds; NOT-MIRRORED when it
# reports nothing for a list the observation only fills while the spec declares
# visibility selected, and an Observe-only object does not declare it; FAIL
# otherwise, and when GitHub has nothing to compare.
nested_verdict() {
  local cond="$1" phase="$2" a="$3" s="$4"
  case "${s}" in '' | null | ERROR) echo FAIL; return 0 ;; esac
  if [ "${a}" = "${s}" ]; then echo PASS; return 0; fi
  if [ "${cond}" = visibility ] && [ "${phase}" = observe ]; then
    case "${a}" in '[]' | null) echo NOT-MIRRORED; return 0 ;; esac
  fi
  echo FAIL
}

# expectation_rows <table> -- the rows of an expectation table, comments dropped.
expectation_rows() {
  grep -v '^#' "$1" | grep -v '^[[:space:]]*$'
}

# snapshot_changed_paths <a.json> <b.json>
# One line per leaf value that differs between two snapshots: the path (keys
# joined with /), the value before and the value after, tab separated.
snapshot_changed_paths() {
  jq -nr --slurpfile a "$1" --slurpfile b "$2" '
    def flat: . as $v
      | reduce (paths | select(. as $p | ($v | getpath($p) | type) as $t | $t != "array" and $t != "object")) as $p
          ({}; .[($p | map(tostring) | join("/"))] = ($v | getpath($p)));
    ($a[0] | flat) as $A | ($b[0] | flat) as $B
    | ([($A | keys[]), ($B | keys[])] | unique[]) as $k
    | select($A[$k] != $B[$k])
    | "\($k)\t\($A[$k] | tojson)\t\($B[$k] | tojson)"'
}

# classify_changes <changed-paths file> <allowed-regexes file>
# Reads the output of snapshot_changed_paths and the allowed path regexes (one
# per line). Prints UNEXPECTED <path> for a value change no regex allows, and
# REWRITE <path> for a timestamp that moved on an object none of whose values
# changed (a write that changed nothing).
classify_changes() {
  local changes="$1" allowed="$2" values ts p prefix
  values="$(cut -f1 "${changes}" | grep -v '/_ts/' || true)"
  if [ -n "${values}" ]; then
    printf '%s\n' "${values}" | grep -Ev -f "${allowed}" | sed 's/^/UNEXPECTED /' || true
  fi
  ts="$(cut -f1 "${changes}" | grep '/_ts/' || true)"
  [ -n "${ts}" ] || return 0
  while IFS= read -r p; do
    prefix="${p%%/_ts/*}/"
    printf '%s\n' "${values}" | awk -v p="${prefix}" 'index($0, p) == 1 { found = 1 } END { exit !found }' \
      || echo "REWRITE ${p}"
  done <<<"${ts}"
}
