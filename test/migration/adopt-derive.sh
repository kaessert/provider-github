#!/usr/bin/env bash
# test/migration/adopt-derive.sh -- derives the adoption manifests from the v1
# fixtures. Sourced by adopt-common.sh, validate-fixtures.sh and selftest.sh; not
# executable on its own. Pure functions: no cluster and no GitHub call.
#
# The adoption scenarios do not keep a second set of manifests that could drift
# from the v1 ones. Every object the baseline provider created is adopted by ONE
# new managed resource derived from its v1 fixture, cluster-scoped (scenario (b)) or
# namespaced (scenario (c)):
#
#   observe   managementPolicies [Observe]; the create-time fields the candidate
#             lets an Observe-only object omit are removed; the external name is
#             set from the live baseline object; the fields the move to
#             crossplane-runtime v2 removes (publishConnectionDetailsTo,
#             providerRef) are replaced by providerConfigRef;
#   full      the v1 forProvider as it is (it matches GitHub: the baseline
#             created GitHub from it) under the default management policies.
#
# The namespaced scope also derives the objects that sit beside the adopted set: the
# Observe-only twins that hold their references as Ref and Selector fields, the
# cluster-scoped Observe-only twins of namespaced objects, and the two Teams of the
# both-scopes probe, and holds the words of the both-scopes verdicts.
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

# adopt_target <Kind> <name> -- where a derived object of the namespaced scope lives and
# how it reaches GitHub: "<namespace> <ProviderConfig kind> <ProviderConfig name>".
# Namespace A holds the organization, its membership, the teams and the two repositories
# the teams are granted access to, reached through the namespaced ProviderConfig of that
# namespace; namespace B holds everything else, reached through a ClusterProviderConfig.
# Objects that refer to one another by Ref or Selector sit in namespace A, because a
# reference resolves inside the namespace of the object that holds it.
adopt_target() {
  case "$1/$2" in
    Organization/* | Membership/* | Team/* | Repository/pgh-mig-repo-main | Repository/pgh-mig-repo-rules)
      printf '%s ProviderConfig %s' "${ADOPT_NS_A}" "${ADOPT_PC_NAME}" ;;
    *)
      printf '%s ClusterProviderConfig %s' "${ADOPT_NS_B}" "${ADOPT_CPC_NAME}" ;;
  esac
}

# ADOPT_TARGET_LABEL is on every derived namespaced object (its own name), so a Selector
# can pick exactly one object of a namespace.
ADOPT_TARGET_LABEL="pgh-mig-target"

# derive_adoption <observe|full> <rendered-v1-dir> <external-names.json|-> <out-dir> [cluster|namespaced]
#
# Writes one YAML file per managed resource of the v1 fixtures (Secrets and other
# prerequisites are skipped) to <out-dir>. <external-names.json> maps
# "Kind/name" to {"externalName": "..."} as read from the live baseline objects
# (mr_state output); "-" derives the external names offline from the fixtures
# (a webhook gets the placeholder hook ID 1).
#
# The namespaced scope moves each object to its group (organizations.github.m...),
# to the namespace and ProviderConfig that adopt_target names, labels it for Selectors,
# reduces writeConnectionSecretToRef to its name (the Secret is in the object's
# namespace) and points secretKeyRef at that namespace (the provider reads the Secret
# from the object's namespace whatever the field says). A namespaced kind has no
# deletionPolicy: an object the v1 fixture kept with deletionPolicy Orphan (a Membership
# must never be deleted with its managed resource) leaves Delete out of its management
# policies instead.
derive_adoption() {
  local mode="$1" src="$2" names="$3" out="$4" scope="${5:-cluster}" f doc kind name n=0 names_json='{}' ns pckind pcname
  case "${mode}" in observe | full) ;; *) die "derive_adoption: unknown mode ${mode}" ;; esac
  case "${scope}" in cluster | namespaced) ;; *) die "derive_adoption: unknown scope ${scope}" ;; esac
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
      ns="" pckind="" pcname=""
      [ "${scope}" = cluster ] || read -r ns pckind pcname <<<"$(adopt_target "${kind}" "${name}")"
      jq --arg mode "${mode}" --arg scope "${scope}" --arg ns "${ns}" --arg pckind "${pckind}" --arg pcname "${pcname}" \
        --arg label "${ADOPT_TARGET_LABEL}" --argjson names "${names_json}" --argjson omitted "${ADOPT_OMITTED_FIELDS}" '
        . as $d
        | "\($d.kind)/\($d.metadata.name)" as $k
        | ((($names[$k].externalName // "") | if . == "" then null else . end)
            // $d.metadata.annotations["crossplane.io/external-name"]
            // (if $d.kind == "OrganizationWebhook" then "1" else $d.metadata.name end)) as $ext
        | .metadata = ({name: $d.metadata.name, annotations: {"crossplane.io/external-name": $ext}}
            + (if $scope == "namespaced" then {namespace: $ns, labels: {($label): $d.metadata.name}} else {} end))
        | (if $scope == "namespaced" then .apiVersion |= sub("\\.github\\.crossplane\\.io/"; ".github.m.crossplane.io/") else . end)
        | .spec |= (
            ((.deletionPolicy // "Delete") == "Orphan") as $orphan
            | del(.publishConnectionDetailsTo, .providerRef)
            | (if $scope == "namespaced" then del(.deletionPolicy) else . end)
            | .providerConfigRef = (if $scope == "namespaced" then {kind: $pckind, name: $pcname} else {name: "default"} end)
            | (if $scope == "namespaced" and has("writeConnectionSecretToRef") then .writeConnectionSecretToRef |= {name} else . end)
            | (if $scope == "namespaced"
               then .forProvider |= walk(if type == "object" and (.secretKeyRef | type) == "object" then .secretKeyRef.namespace = $ns else . end)
               else . end)
            | if $mode == "observe"
              then .managementPolicies = ["Observe"]
                   | .forProvider |= reduce ($omitted[$d.kind] // [])[] as $field (.; del(.[$field]))
              else .managementPolicies = (if $scope == "namespaced" and $orphan then ["Observe", "Create", "Update", "LateInitialize"] else ["*"] end) end)' <<<"${doc}" \
        | yq -P '.' >"${out}/$(printf '%02d' "${n}")-$(printf '%s' "${kind}" | tr 'A-Z' 'a-z')-${name}.yaml" \
        || die "cannot derive ${kind}/${name}"
    done < <(yq -o=json -I=0 '.' "${f}")
  done
}

# adopt_have <dir> <Kind> <name> -- true when <dir> holds the derived manifest of the object.
adopt_have() {
  compgen -G "$1/*-$(printf '%s' "$2" | tr 'A-Z' 'a-z')-$3.yaml" >/dev/null
}

# adopt_file <dir> <Kind> <name> -- the path of the derived manifest of the object.
adopt_file() {
  compgen -G "$1/*-$(printf '%s' "$2" | tr 'A-Z' 'a-z')-$3.yaml" | head -n1
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
  yq -P ".metadata.name = \"${ADOPT_PROBE_NAME}\" | del(.metadata.labels) | .spec.forProvider.description = \"${ADOPT_PROBE_DESCRIPTION}\"" "${src}" \
    >"${dir}/99-team-${ADOPT_PROBE_NAME}.yaml"
}

# ---------------------------------------------------------------------------
# Objects that exist beside the adopted set (namespaced scope)
# ---------------------------------------------------------------------------

# Names of the helper objects. The adopted set never uses these prefixes, which is how
# the settle checks tell them apart (ADOPT_AUX_RE also matches the drift probe).
# shellcheck disable=SC2034  # read by adopt-common.sh
ADOPT_AUX_RE="^(${ADOPT_PROBE_NAME}|pgh-mig-(ref|cl|both)-.*)\$"
ADOPT_BOTH_TEAM="pgh-mig-both-team"
ADOPT_BOTH_DESC_NS="pgh-mig both scopes: written by the namespaced object"
ADOPT_BOTH_DESC_CLUSTER="pgh-mig both scopes: written by the cluster-scoped object"

# One cluster-scoped Observe-only twin per kind, over an object of the adopted set.
ADOPT_TWIN_OBJECTS="Organization/pgh-mig-org Membership/pgh-mig-membership Team/pgh-mig-team-parent Repository/pgh-mig-repo-template OrganizationVariable/pgh-mig-var-all OrganizationWebhook/pgh-mig-hook-plain RunnerGroup/pgh-mig-rg-selected ActionsSecretAccess/pgh-mig-actions-secret-access DependabotSecretAccess/pgh-mig-dependabot-secret-access"

# Helpers of the reference twins: a plain-string field replaced by the Ref (a named object
# of the same namespace) or the Selector (the object labelled with that name) that points
# at the object holding the value.
ADOPT_REF_JQ='
def viaref($f; $r; $t): if has($f) then del(.[$f]) | .[$r] = {name: $t} else . end;
def viaref_self($f; $r): if has($f) then .[$f] as $v | del(.[$f]) | .[$r] = {name: $v} else . end;
def viasel($f; $s; $t): if has($f) then del(.[$f]) | .[$s] = {matchLabels: {"pgh-mig-target": $t}} else . end;
def viasel_self($f; $s): if has($f) then .[$f] as $v | del(.[$f]) | .[$s] = {matchLabels: {"pgh-mig-target": $v}} else . end;
'

# The reference twins, one per line, fields separated by ^: kind, source object, twin,
# resolvable, targets that must be in the adopted set, jq transform of the source, check
# paths (;-separated).
ADOPT_REF_TWINS='Team^pgh-mig-team-child^pgh-mig-ref-team^yes^Organization/pgh-mig-org Team/pgh-mig-team-parent Membership/pgh-mig-membership^.spec.forProvider |= (viaref("org"; "orgRef"; "pgh-mig-org") | viaref("parent"; "parentRef"; "pgh-mig-team-parent") | .members |= map(viaref("user"; "userRef"; "pgh-mig-membership")))^.spec.forProvider.org;.spec.forProvider.parent;[.spec.forProvider.members[].user]
Membership^pgh-mig-membership^pgh-mig-ref-membership^yes^Organization/pgh-mig-org^.spec.forProvider |= viaref("org"; "orgRef"; "pgh-mig-org")^.spec.forProvider.org
Organization^pgh-mig-org^pgh-mig-ref-org^yes^Repository/pgh-mig-repo-main Repository/pgh-mig-repo-rules^.spec.forProvider |= (.actions.enabledRepos |= map(viaref_self("repo"; "repoRef")) | .secrets.actionsSecrets |= map(.repositoryAccessList |= map(viaref_self("repo"; "repoRef"))) | .secrets.dependabotSecrets |= map(.repositoryAccessList |= map(viaref_self("repo"; "repoRef"))))^[.spec.forProvider.actions.enabledRepos[].repo];[.spec.forProvider.secrets.actionsSecrets[].repositoryAccessList[].repo];[.spec.forProvider.secrets.dependabotSecrets[].repositoryAccessList[].repo]
Repository^pgh-mig-repo-main^pgh-mig-ref-repo^yes^Organization/pgh-mig-org Team/pgh-mig-team-child Membership/pgh-mig-membership^.spec.forProvider |= (viasel("org"; "orgSelector"; "pgh-mig-org") | .permissions.teams |= map(viasel_self("team"; "teamSelector")) | .permissions.users |= map(viasel("user"; "userSelector"; "pgh-mig-membership")))^.spec.forProvider.org;[.spec.forProvider.permissions.teams[].team];[.spec.forProvider.permissions.users[].user]
OrganizationVariable^pgh-mig-var-all^pgh-mig-ref-xns-var^no^Organization/pgh-mig-org^.spec.forProvider |= viaref("org"; "orgRef"; "pgh-mig-org")^.spec.forProvider.org'

# derive_ref_twins <derived-namespaced-observe-dir> <out-dir>
#
# Writes the Observe-only twins that hold their references as Ref or Selector fields, over
# objects of the adopted set (a twin and its source name the same GitHub object):
#   pgh-mig-ref-team        Team: orgRef, parentRef, members[].userRef
#   pgh-mig-ref-membership  Membership: orgRef
#   pgh-mig-ref-org         Organization: repoRef in the Actions and secrets repository lists
#   pgh-mig-ref-repo        Repository: orgSelector, teamSelector, userSelector
#   pgh-mig-ref-xns-var     OrganizationVariable in the OTHER namespace: an orgRef naming an
#                           Organization that exists only in namespace A (must NOT resolve)
# A twin is skipped when its source or a target is not in the adopted set. Also writes
# <out-dir>/checks.tsv (twin, kind, namespace, resolvable yes|no, jq path over the twin, the
# value the plain-string source declares for that path; null where the path must stay unset)
# and <out-dir>/names.meta.
derive_ref_twins() {
  local src="$1" out="$2" kind source twin resolvable needs filter paths t srcf path want ns doc
  mkdir -p "${out}"
  rm -f "${out}"/*.yaml "${out}/checks.tsv" "${out}/names.meta"
  : >"${out}/checks.tsv"
  while IFS='^' read -r kind source twin resolvable needs filter paths; do
    [ -n "${kind}" ] || continue
    adopt_have "${src}" "${kind}" "${source}" || continue
    for t in ${needs}; do adopt_have "${src}" "${t%%/*}" "${t#*/}" || continue 2; done
    srcf="$(adopt_file "${src}" "${kind}" "${source}")"
    doc="$(yq -o=json -I=0 '.' "${srcf}")"
    jq --arg name "${twin}" "${ADOPT_REF_JQ} .metadata.name = \$name | del(.metadata.labels) | ${filter}" <<<"${doc}" \
      | yq -P '.' >"${out}/$(basename "${srcf}" | sed -E 's/^([0-9]+)-.*/\1/')-$(printf '%s' "${kind}" | tr 'A-Z' 'a-z')-${twin}.yaml" \
      || die "cannot derive ${kind}/${twin}"
    ns="$(jq -r '.metadata.namespace' <<<"${doc}")"
    while IFS= read -r path; do
      [ -n "${path}" ] || continue
      if [ "${resolvable}" = yes ]; then want="$(jq -c "${path}" <<<"${doc}")"; else want=null; fi
      printf '%s\t%s\t%s\t%s\t%s\t%s\n' "${twin}" "${kind}" "${ns}" "${resolvable}" "${path}" "${want}" >>"${out}/checks.tsv"
    done < <(tr ';' '\n' <<<"${paths}")
  done <<<"${ADOPT_REF_TWINS}"
  jq -Rn '[inputs | split("\t")] | {resolvable: ([.[] | select(.[3] == "yes") | .[0]] | unique), unresolvable: ([.[] | select(.[3] == "no") | .[0]] | unique)}' \
    "${out}/checks.tsv" >"${out}/names.meta"
}

# derive_cluster_twins <rendered-v1-dir> <external-names.json|-> <present-dir> <out-dir>
# The cluster-scoped Observe-only twin of one object per kind (ADOPT_TWIN_OBJECTS): same
# external name, so the twin and the namespaced object observe one GitHub object. Only
# objects <present-dir> holds a manifest for are twinned. The twin is named
# pgh-mig-cl-<name>.
derive_cluster_twins() {
  local src="$1" names="$2" present="$3" out="$4" tmp o kind name f
  tmp="$(mktemp -d)"
  derive_adoption observe "${src}" "${names}" "${tmp}" cluster
  mkdir -p "${out}"
  rm -f "${out}"/*.yaml
  for o in ${ADOPT_TWIN_OBJECTS}; do
    kind="${o%%/*}"
    name="${o#*/}"
    adopt_have "${present}" "${kind}" "${name}" || continue
    f="$(adopt_file "${tmp}" "${kind}" "${name}")"
    [ -n "${f}" ] || continue
    yq -P ".metadata.name = \"pgh-mig-cl-${name}\"" "${f}" \
      >"${out}/$(basename "${f}" | sed -E 's/^([0-9]+)-.*/\1/')-$(printf '%s' "${kind}" | tr 'A-Z' 'a-z')-pgh-mig-cl-${name}.yaml" \
      || die "cannot derive the cluster-scoped twin of ${kind}/${name}"
  done
  rm -rf "${tmp}"
}

# derive_both_teams <out-dir> -- one Team under full management in each scope, over ONE
# GitHub team, each declaring a different description. Neither deletes the team with its
# object (deletionPolicy Orphan on the cluster-scoped one, no Delete in the management
# policies of the namespaced one): the cleanup sweep removes it. The namespaced object
# creates the team; the cluster-scoped one adopts it by external name.
derive_both_teams() {
  local out="$1"
  mkdir -p "${out}"
  rm -f "${out}"/*.yaml
  cat >"${out}/10-team-namespaced.yaml" <<YAML
apiVersion: organizations.github.m.crossplane.io/v1alpha1
kind: Team
metadata:
  name: ${ADOPT_BOTH_TEAM}
  namespace: ${ADOPT_NS_A}
  annotations:
    crossplane.io/external-name: ${ADOPT_BOTH_TEAM}
spec:
  managementPolicies:
    - Observe
    - Create
    - Update
    - LateInitialize
  forProvider:
    org: ${MIGRATION_ORG}
    description: "${ADOPT_BOTH_DESC_NS}"
    privacy: closed
  providerConfigRef:
    kind: ProviderConfig
    name: ${ADOPT_PC_NAME}
YAML
  cat >"${out}/20-team-cluster.yaml" <<YAML
apiVersion: organizations.github.crossplane.io/v1alpha1
kind: Team
metadata:
  name: pgh-mig-cl-both-team
  annotations:
    crossplane.io/external-name: ${ADOPT_BOTH_TEAM}
spec:
  deletionPolicy: Orphan
  forProvider:
    org: ${MIGRATION_ORG}
    description: "${ADOPT_BOTH_DESC_CLUSTER}"
    privacy: closed
  providerConfigRef:
    name: default
YAML
}

# ref_value_equal <json a> <json b> -- equal, compared case-insensitively on every string
# (GitHub logins and the declared ones differ in case at times).
ref_value_equal() {
  jq -en --argjson a "$1" --argjson b "$2" 'def d: walk(if type == "string" then ascii_downcase else . end); ($a | d) == ($b | d)' >/dev/null 2>&1
}

# both_scopes_observe_verdict <twins> <twins Synced=True> <namespaced objects that lost Synced>
both_scopes_observe_verdict() {
  if [ "$1" -eq 0 ]; then
    echo "not measured: no cluster-scoped twin was applied"
  elif [ "$2" -eq "$1" ] && [ "$3" -eq 0 ]; then
    echo "no guard against two scopes observing one object: all $1 cluster-scoped Observe-only twins reached Synced=True beside their namespaced objects, and no namespaced object lost Synced"
  else
    echo "the second scope is treated differently: $2 of $1 cluster-scoped Observe-only twins are Synced=True and $3 namespaced object(s) lost Synced (the messages are in the table)"
  fi
}

# both_scopes_write_verdict <namespaced updates> <cluster updates> <namespaced Synced> <cluster Synced>
# The updates are the ones each controller issued for the one Team, from the moment the second
# object was applied.
both_scopes_write_verdict() {
  if [ "$1" -ge 1 ] && [ "$2" -ge 1 ]; then
    echo "NO GUARD: a cluster-scoped and a namespaced Team managed one GitHub team at once and both wrote (namespaced $1 updates, cluster-scoped $2 updates): each controller overwrote the other's description"
  elif [ "$1" -eq 0 ] && [ "$2" -eq 0 ]; then
    echo "inconclusive: neither controller issued an update in the window (namespaced Synced=$3, cluster-scoped Synced=$4)"
  elif [ "$1" -ge 1 ]; then
    echo "ONE SCOPE WROTE: only the namespaced controller issued updates ($1 against 0); the cluster-scoped object reports Synced=$4, so something kept it from writing"
  else
    echo "ONE SCOPE WROTE: only the cluster-scoped controller issued updates (0 against $2); the namespaced object reports Synced=$3, so something kept it from writing"
  fi
}

# both_scopes_write_status <write verdict> -- how the report classifies the verdict. One GitHub
# object is managed by exactly one managed resource in exactly one scope, so two scopes naming it
# both writing is the predicted outcome (PASS). One side not writing means something kept it from
# writing: cross-scope behaviour nobody documented (WARN). Anything else is INFO.
both_scopes_write_status() {
  case "$1" in
    "NO GUARD"*) echo PASS ;;
    "ONE SCOPE WROTE"*) echo WARN ;;
    *) echo INFO ;;
  esac
}

# both_scopes_summary <observe verdict> <write verdict> -- the plain answer to the question
# whether the provider guards against two scopes managing one object.
both_scopes_summary() {
  case "$2" in
    "NO GUARD"*) echo "No, as documented. One GitHub object is managed by exactly one managed resource in exactly one scope; the provider has no guard that stops a cluster-scoped and a namespaced managed resource from managing the same GitHub object at once, and the two controllers both wrote to the same team, as two cluster-scoped resources naming one object would. Adopt into one scope at a time." ;;
    "ONE SCOPE WROTE"*) echo "Something stopped one scope from writing, which is not documented behaviour (see the write verdict above); the Observe-only probe: $1." ;;
    "inconclusive"*) echo "Not established: the full-management probe was inconclusive; the Observe-only probe: $1." ;;
    *) echo "Not measured: the full-management probe did not run; the Observe-only probe: ${1:-not measured}." ;;
  esac
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

# team_echo_allowed <snapshot after the change> <team slug> <new description, JSON> -- the allowed-path
# regexes (one per line) for the places GitHub echoes a changed team's description: the PARENT object
# of a child team wherever the child appears under branch protection
# (repos/<repo>/branchProtection/<branch>/.../teams/<n>/parent/description). Only a path whose parent
# is the changed team, and whose value is the one the harness set, is allowed; the description of a
# team that is not the changed one, a parent echo holding another value, and every other path are not.
team_echo_allowed() {
  jq -r --arg slug "$2" --argjson v "$3" '
    . as $s
    | [paths | select(length >= 6 and .[0] == "repos" and .[2] == "branchProtection"
        and .[-1] == "description" and .[-2] == "parent" and .[-4] == "teams" and (.[-3] | type) == "number")]
    | .[] | select(. as $p | ($s | getpath($p[0:-1] + ["slug"])) == $slug and ($s | getpath($p)) == $v)
    | map(tostring) | join("/")' "$1" | sed -e 's/[][\.*^$+?(){}|]/\\&/g' -e 's/^/^/' -e 's/$/$/'
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
