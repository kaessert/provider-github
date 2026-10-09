#!/usr/bin/env bash
# check-xpkg-base-consistency.sh — every platform manifest in an xpkg index
# (or in a set of pre-push .xpkg files) must reference the same
# io.crossplane.xpkg:base layer. hack/normalize-xpkg-base-layer.go is what
# converges them; this is the check that fails the build/audit loudly when
# something upstream of it still let two platforms diverge — see that
# file's header for the root cause (a non-transitive YAML key comparator in
# the xpkg CLI's own serializer).
#
# USAGE
#   check-xpkg-base-consistency.sh registry <index-ref>
#   check-xpkg-base-consistency.sh local <xpkg-file> [<xpkg-file> ...]
#
#   registry  audits an already-published multi-arch tag. Needs `crane` and
#             `jq`. This is the only mode that can see what the registry
#             actually accepted; a build-time check can only compare files
#             that have not been pushed yet.
#   local     audits pre-push .xpkg files directly (the Docker-save tarballs
#             `crossplane xpkg build` / `up xpkg build` emit). This is the
#             mode wired into `xpkg.push.up` in the Makefile — a gate that
#             only runs after the push cannot stop a burned tag.
#
# A local .xpkg's manifest.json carries no OCI annotations at all (that is
# added when the CLI pushes), so local mode identifies the base layer by
# structure instead: it is the one layer whose entire content is a single
# file named "package.yaml". registry mode reads the annotation directly,
# because a pushed index has one.
#
# EXIT 0  PASS (or SKIP: fewer than 2 platforms to compare — nothing can
#         diverge)
# EXIT 1  FAIL (divergence found) or a usage/lookup error the caller must
#         fix before this check can run at all
set -euo pipefail

usage() {
  echo "usage: $0 registry <index-ref>" >&2
  echo "       $0 local <xpkg-file> [<xpkg-file> ...]" >&2
  exit 1
}

find_base_layer_diffid() {
  # Prints the base layer's uncompressed-content sha256 (its diffID) for one
  # local .xpkg tar, identified by structure: the one layer whose entire
  # content is a single file named "package.yaml".
  local xpkg="$1"
  local manifest layer names
  manifest=$(tar -xOf "$xpkg" manifest.json)
  while IFS= read -r layer; do
    names=$(tar -xOf "$xpkg" "$layer" 2>/dev/null | tar -tz 2>/dev/null || true)
    if [ "$names" = "package.yaml" ]; then
      tar -xOf "$xpkg" "$layer" | zcat | sha256sum | awk '{print $1}'
      return 0
    fi
  done < <(echo "$manifest" | jq -r '.[0].Layers[]')
  return 1
}

local_mode() {
  local files=("$@")
  if [ "${#files[@]}" -lt 2 ]; then
    echo "SKIP: ${#files[@]} platform package(s) given; nothing to compare"
    exit 0
  fi

  local names=() diffids=()
  local f diffid
  for f in "${files[@]}"; do
    [ -f "$f" ] || { echo "FAIL: $f not found"; exit 1; }
    if ! diffid=$(find_base_layer_diffid "$f"); then
      echo "FAIL: $f carries no layer whose sole content is package.yaml — cannot identify the xpkg base layer"
      exit 1
    fi
    names+=("$f")
    diffids+=("$diffid")
  done

  local rc=0 i
  for ((i = 1; i < ${#files[@]}; i++)); do
    if [ "${diffids[$i]}" != "${diffids[0]}" ]; then
      echo "FAIL: base layer diverges — ${names[0]}=sha256:${diffids[0]} vs ${names[$i]}=sha256:${diffids[$i]}"
      rc=1
    fi
  done
  if [ "$rc" -eq 0 ]; then
    echo "PASS: base layer consistent across ${#files[@]} platform package(s) (sha256:${diffids[0]})"
  fi
  return "$rc"
}

registry_mode() {
  local img="$1"
  command -v crane >/dev/null 2>&1 || { echo "FAIL: crane not found on PATH"; exit 1; }
  command -v jq >/dev/null 2>&1 || { echo "FAIL: jq not found on PATH"; exit 1; }

  local repo="${img%:*}"
  repo="${repo%@*}"

  local digs
  digs=$(crane manifest "$img" \
    | jq -r '.manifests[] | select(.platform.architecture != null and .platform.architecture != "unknown") | .digest')
  if [ -z "$digs" ]; then
    echo "FAIL: $img — no platform manifests found in the index"
    exit 1
  fi

  local n=0 base_digests=""
  local d bd
  while IFS= read -r d; do
    n=$((n + 1))
    bd=$(crane manifest "$repo@$d" \
      | jq -r '.layers[] | select(.annotations."io.crossplane.xpkg" == "base") | .digest')
    if [ -z "$bd" ]; then
      echo "FAIL: $repo@$d — no layer annotated io.crossplane.xpkg=base"
      exit 1
    fi
    base_digests="$base_digests$bd"$'\n'
  done <<<"$digs"

  local uniq
  uniq=$(printf '%s' "$base_digests" | sed '/^$/d' | sort -u)
  local uniq_count
  uniq_count=$(printf '%s\n' "$uniq" | sed '/^$/d' | wc -l)

  if [ "$uniq_count" -gt 1 ]; then
    echo "FAIL: $img — base layer digest diverges across $n platform manifest(s):"
    printf '%s\n' "$uniq" | sed 's/^/  /'
    return 1
  fi
  echo "PASS: $img — base layer consistent across $n platform manifest(s) ($uniq)"
  return 0
}

[ "$#" -ge 2 ] || usage
mode="$1"
shift
case "$mode" in
  registry) [ "$#" -eq 1 ] || usage; registry_mode "$1" ;;
  local) local_mode "$@" ;;
  *) usage ;;
esac
