#!/usr/bin/env bash
# hack/e2e-wait-crds.sh — block until every CRD this provider's package owns is
# Established AND served by API discovery. Copied verbatim into every provider;
# nothing in it varies per provider.
#
# Run by the `e2e-wait-crds` Makefile target, which is a prerequisite of
# `uptest`, so `make e2e` (the nest's e2e_run and upstream CI alike) waits here
# after the provider package is deployed and before test/setup.sh or chainsaw
# apply anything.
#
# WHY: `local.xpkg.deploy.provider.%` (build/makelib/local.xpkg.mk) returns
# before Crossplane's package manager has registered the provider's CRDs, and it
# registers them one at a time. An apply that races it fails with `no matches
# for kind ... ensure CRDs are installed first` or `failed to get restmapping`.
#
# WHAT IT WAITS FOR: the CRD names in package/crds/ (the CRDs the package
# installs), so no kind is hand-listed. Each must be
#   1. Established — read with jsonpath, which yields an EMPTY string for an
#      absent CRD and for a nil `.status`, so a CRD still being written is simply
#      pending. A bare `kubectl wait --for=condition=Established` errors at once
#      in both of those states; it is never used here.
#   2. Served — present in `kubectl api-resources` (an uncached discovery call),
#      which is what the REST mapper chainsaw applies through reads.
#
# The wait is bounded by CRD_WAIT_SECONDS (default 600) and fails loudly,
# naming every CRD that missed the deadline. There is no fixed sleep.
#
# Usage: hack/e2e-wait-crds.sh [--list]
#   --list   print the derived CRD names and exit; needs no cluster.
# Environment:
#   KUBECTL           kubectl binary (default kubectl)
#   CRD_WAIT_SECONDS  deadline for the whole wait (default 600)
#   E2E_CRD_DIR       directory holding the package's CRDs (default ../package/crds)
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CRD_DIR="${E2E_CRD_DIR:-${HERE}/../package/crds}"
CRD_WAIT_SECONDS="${CRD_WAIT_SECONDS:-600}"
POLL_SECONDS="${E2E_CRD_POLL_SECONDS:-2}"

# One CRD name per input document: the first `name:` under the top-level
# `metadata:` of every document whose kind is CustomResourceDefinition.
derive_crds() {
  local f
  for f in "${CRD_DIR}"/*.yaml; do
    [ -e "${f}" ] || continue
    awk '
      function flush() { if (iscrd && name != "") print name; iscrd = 0; name = ""; inmeta = 0 }
      /^---/ { flush(); next }
      /^kind:[[:space:]]*CustomResourceDefinition[[:space:]]*$/ { iscrd = 1 }
      /^metadata:/ { inmeta = 1; next }
      inmeta && /^[^[:space:]#]/ { inmeta = 0 }
      inmeta && name == "" && /^  name:[[:space:]]/ { name = $2 }
      END { flush() }
    ' "${f}"
  done | sort -u
}

EXPECTED="$(derive_crds)"
if [ -z "${EXPECTED}" ]; then
  echo "ERROR: e2e-wait-crds: found no CustomResourceDefinition in ${CRD_DIR}; cannot tell which CRDs to wait for." >&2
  exit 2
fi

if [ "${1:-}" = "--list" ]; then
  printf '%s\n' "${EXPECTED}"
  exit 0
fi

total="$(wc -l <<<"${EXPECTED}" | tr -d ' ')"
echo "==> Waiting for all ${total} package CRDs to be Established and served by API discovery..."

deadline=$((SECONDS + CRD_WAIT_SECONDS))
while :; do
  # `name True` per Established CRD. jsonpath prints nothing for a missing
  # `.status`, so an unfinished CRD is absent from this list, never an error.
  ESTABLISHED="$(${KUBECTL} get crd -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.conditions[?(@.type=="Established")].status}{"\n"}{end}' 2>/dev/null | awk '$2 == "True" { print $1 }' | sort -u || true)"
  SERVED="$(${KUBECTL} api-resources --cached=false -o name 2>/dev/null | sort -u || true)"

  NOT_ESTABLISHED="$(comm -23 <(printf '%s\n' "${EXPECTED}") <(printf '%s\n' "${ESTABLISHED}"))"
  # api-resources names are <plural>.<group>, identical to the CRD name; core
  # types have no group, which no CRD name can collide with.
  NOT_SERVED="$(comm -23 <(printf '%s\n' "${EXPECTED}") <(printf '%s\n' "${SERVED}"))"

  if [ -z "${NOT_ESTABLISHED}" ] && [ -z "${NOT_SERVED}" ]; then
    echo "    OK: every package CRD (${total}) is Established and served."
    exit 0
  fi

  if [ "${SECONDS}" -ge "${deadline}" ]; then
    echo "ERROR: e2e-wait-crds: not every package CRD was Established and served within ${CRD_WAIT_SECONDS}s." >&2
    if [ -n "${NOT_ESTABLISHED}" ]; then
      echo "  not Established ($(wc -l <<<"${NOT_ESTABLISHED}" | tr -d ' ')):" >&2
      head -n 50 <<<"${NOT_ESTABLISHED}" | sed 's/^/    /' >&2
    fi
    if [ -n "${NOT_SERVED}" ]; then
      echo "  not served by API discovery ($(wc -l <<<"${NOT_SERVED}" | tr -d ' ')):" >&2
      head -n 50 <<<"${NOT_SERVED}" | sed 's/^/    /' >&2
    fi
    exit 1
  fi
  sleep "${POLL_SECONDS}"
done
