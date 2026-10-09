#!/usr/bin/env bash
# hack/release-notes.sh — assemble a release's notes from the release-note
# blocks contributors write in their pull request descriptions.
#
# Usage: hack/release-notes.sh <tag> [previous-tag]     (e.g. v0.2.0 v0.1.0)
#
# Why not GitHub's own "generate release notes" API: it renders one bullet per
# merged pull request, using the pull request TITLE. That is a good summary
# when one pull request is one change, and a poor one when a pull request
# carries a batch of independent changes -- the batch collapses to a single
# line naming none of them.
#
# Reading the description instead removes the coupling between how work is
# grouped into pull requests and how much detail the notes can carry. A
# description is unbounded, so one pull request can document fifty changes.
#
# The format is the one Kubernetes uses:
#
#     ```release-note
#     Fixed a converge loop when two mutually exclusive fields are both set
#     ```
#
# Every non-empty line inside the block becomes one bullet, so a batched pull
# request lists each change on its own line. NONE on a line drops that line,
# and a pull request whose blocks say only NONE is omitted from the notes
# entirely. A pull request with NO block falls back to its title -- which is
# what the notes would have said anyway, so a contributor who has never seen
# this format loses nothing by not using it.
#
# NONE is handled per line, not per block, because the pull request template
# ships a NONE block as its default: a contributor who writes their note above
# it without deleting it has two blocks, and treating NONE as a whole-body
# verdict would publish the word NONE as a bullet.
#
# Requires: gh, jq. GH_TOKEN must be set, and in a workflow the job needs
#   permissions:
#     pull-requests: read
#
# Offline testing: set RELNOTES_FIXTURE to a file holding a JSON array of pull
# request objects and no network calls are made. hack/release-notes-test.sh
# uses this to exercise the rendering without a repository.
set -euo pipefail

TAG=""
PREV=""
REPO=""

resolve_args() {
  TAG="${1:-}"
  PREV="${2:-}"
  [ -n "$TAG" ] || { echo "usage: $0 <tag> [previous-tag]" >&2; exit 2; }

  REPO="${GITHUB_REPOSITORY:-}"
  if [ -z "$REPO" ] && [ -z "${RELNOTES_FIXTURE:-}" ]; then
    REPO=$(git config --get remote.origin.url \
           | sed -e 's|.*github\.com[:/]||' -e 's|\.git$||')
  fi
  # Only reachable under RELNOTES_FIXTURE with GITHUB_REPOSITORY unset; keeps
  # rendered links well-formed rather than emitting github.com//pull/N.
  REPO="${REPO:-owner/repo}"
}

# ── Discovery ───────────────────────────────────────────────────────────────
# Which pull requests belong to this release: those whose merge commit is in
# the range previous-tag..tag. Matching on the merge commit rather than on a
# date range is what makes this correct under all three merge strategies --
# merge commit, squash and rebase each leave a commit in the range, and each
# is what the API reports as merge_commit_sha.

releases_json() {
  if [ -n "${RELNOTES_RELEASES_FIXTURE:-}" ]; then
    cat "$RELNOTES_RELEASES_FIXTURE"
    return
  fi
  gh api "repos/$REPO/releases?per_page=100"
}

# Choose the baseline this release is measured against. Pre-releases are
# skipped: nobody runs a release candidate, so the meaningful baseline for
# both a candidate and a final release is the last STABLE release.
#
# Measuring against the previous candidate instead would make each candidate
# report only its delta from the one before, and -- worse -- would leave the
# final release reporting only what changed since its last candidate, which is
# usually nothing. Notes are cumulative across a candidate series by design.
#
# A provider whose only releases are candidates has no stable baseline, so it
# falls back to the most recent release of any kind rather than producing
# nothing.
#
# "Newest" is decided by semantic version, not by publication date. A backport
# release on an older line publishes out of chronological order by
# construction -- e.g. a v0.1.x fix published the day after v0.2.2 -- so date
# order is never the right order. Within one core version (a run of release
# candidates for the same target), published_at is still the correct
# tie-break: those really do land in chronological order.
pick_prev() {
  jq -r --arg tag "$TAG" '
    def semver:
      capture("^v?(?<M>[0-9]+)\\.(?<m>[0-9]+)\\.(?<p>[0-9]+)")
      | [ (.M | tonumber), (.m | tonumber), (.p | tonumber) ];
    ($tag | semver) as $tagver
    | [ .[] | select(.draft | not) | select(.tag_name != $tag) ] as $rel
    | ( [ $rel[] | select(.prerelease | not)
                 | select((.tag_name | semver) as $v | $v < $tagver) ]
        | sort_by([ (.tag_name | semver), .published_at ]) | reverse | .[0].tag_name ) as $stable
    | ( $stable
        // ( $rel | sort_by([ (.tag_name | semver), .published_at ]) | reverse | .[0].tag_name )
        // empty )
  '
}

resolve_prev() {
  [ -n "$PREV" ] && { echo "$PREV"; return; }
  releases_json | pick_prev
}

range_shas() {
  local prev="$1"
  gh api "repos/$REPO/compare/$prev...$TAG" --jq '.commits[].sha'
}

all_merged_prs() {
  if [ -n "${RELNOTES_FIXTURE:-}" ]; then
    cat "$RELNOTES_FIXTURE"
    return
  fi
  # Every merged pull request in the repository, not just this release's.
  # First-contribution detection needs each author's full history: a
  # contributor is new when their EARLIEST merged pull request falls inside
  # this release, which cannot be decided from the release's own slice.
  gh api "repos/$REPO/pulls?state=closed&per_page=100&sort=updated&direction=desc" \
     --paginate --jq '.[] | select(.merged_at != null)' | jq -s .
}

collect() {
  local all prev shas nums
  all=$(all_merged_prs)
  if [ -n "${RELNOTES_FIXTURE:-}" ]; then
    prev="${RELNOTES_FIXTURE_PREV:-v0.0.0}"
    nums="${RELNOTES_FIXTURE_RANGE:-$(printf '%s' "$all" | jq -c '[ .[].number ]')}"
  else
    prev=$(resolve_prev)
    [ -n "$prev" ] || { echo "release-notes: no previous release found" >&2; return 1; }
    echo "release-notes: range $prev...$TAG" >&2
    shas=$(range_shas "$prev" | jq -R . | jq -s .)
    nums=$(printf '%s' "$all" | jq -c --argjson shas "$shas" \
           '[ .[] | select(.merge_commit_sha as $s | $shas | index($s)) | .number ]')
  fi
  # $all can hold every merged pull request in the repository -- easily past
  # Linux's 131072-byte MAX_ARG_STRLEN cap on a single argv string, which
  # --argjson would violate here. Stream it on stdin instead, exactly as the
  # $shas pass above already does.
  printf '%s' "$all" | jq --argjson nums "$nums" --arg prev "$prev" \
     '{ all: ., nums: $nums, prev: $prev }'
}

# ── Rendering ───────────────────────────────────────────────────────────────
# Category comes from the pull request's labels when it has a recognised one,
# and from a conventional-commit prefix on its title otherwise. Neither is
# required: an unlabelled pull request with a free-form title lands under
# "Other Changes" rather than being dropped.
#
# Attribution and the two trailing sections mirror what GitHub's own notes
# produce, because these notes replace them on both surfaces they reach. Pull
# requests are referenced by full URL rather than #N: the package registry
# renders this same text outside GitHub, where #N is not a link.

render() {
  jq -r --arg repo "$REPO" --arg tag "$TAG" '
    def blocks:
      [ (.body // "") | scan("(?s)```release-notes?[ \t]*\r?\n(.*?)```") | .[0] ];

    def entries:
      blocks as $b
      # A leading "-" or "*" is only a list marker when it is followed by a
      # separator: "- text" and "* text" lose it, but "**Bold:** text" and
      # "*italic* text" keep both marks, because their "*" is not followed by
      # whitespace. An unconditional [-*]? here ate the first "*" of a
      # bold-prefixed bullet and shipped "* *Breaking:**" in a published
      # release.
      | ([ $b[] | gsub("\r"; "") | split("\n")[]
           | gsub("^\\s*(?:[-*][ \t]+)?|\\s+$"; "") | select(length > 0) ]) as $all
      | ($all | map(select(ascii_upcase != "NONE"))) as $lines
      | if   ($b | length) == 0     then [ .title ]
        elif ($all | length) == 0   then [ .title ]
        elif ($lines | length) == 0 then []
        else $lines end;

    def category:
      ([ .labels[]?.name ] | map(ascii_downcase)) as $l
      | if   ($l | any(. == "bug" or . == "kind/bug" or . == "fix"))            then "Bug Fixes"
        elif ($l | any(. == "enhancement" or . == "feature" or . == "kind/feature")) then "Features"
        elif ($l | any(. == "documentation" or . == "docs"))                    then "Documentation"
        elif (.title | test("^fix(\\(|:|!)"; "i"))                              then "Bug Fixes"
        elif (.title | test("^feat(\\(|:|!)"; "i"))                             then "Features"
        elif (.title | test("^docs(\\(|:|!)"; "i"))                             then "Documentation"
        else "Other Changes" end;

    def prurl: "https://github.com/\($repo)/pull/\(.)";

    .all as $all | .nums as $nums | .prev as $prev
    | ($all | map(select(.number as $n | $nums | index($n) != null))) as $rng

    | ($rng | map({ cat: category, entries: entries,
                    user: (.user.login // "unknown"), number: .number })
            | map(select(.entries | length > 0))) as $items

    | ([ $items[] | . as $i | $i.entries[]
         | "* \(.) by @\($i.user) in \($i.number | prurl)" as $line
         | { cat: $i.cat, line: $line } ]) as $flat

    | (reduce $flat[] as $e ({}; .[$e.cat] = ((.[$e.cat] // []) + [$e.line]))) as $byCat

    | ([ "Features", "Bug Fixes", "Documentation", "Other Changes" ]
       | map(select($byCat[.]) | "### \(.)\n" + ($byCat[.] | join("\n")))) as $sections

    # A contributor is new when the earliest merged pull request they have in
    # the whole repository is one of this release'"'"'s.
    | ([ $rng[] | .user.login // "unknown" ] | unique) as $authors
    | ([ $authors[] as $a
         | ($all | map(select((.user.login // "unknown") == $a))
                 | sort_by(.merged_at // "") | .[0]) as $first
         | select($first != null and ($nums | index($first.number) != null))
         | "* @\($a) made their first contribution in \($first.number | prurl)" ]) as $newc

    | ($sections
       + (if ($newc | length) > 0
          then [ "### New Contributors\n" + ($newc | join("\n")) ] else [] end)
       + (if ($prev | length) > 0
          then [ "**Full Changelog**: https://github.com/\($repo)/compare/\($prev)...\($tag)" ]
          else [] end))
      | join("\n\n")
  '
}

# ── Main ────────────────────────────────────────────────────────────────────

main() {
  resolve_args "$@"

  local notes
  notes=$(collect | render)

  if [ -z "${notes//[[:space:]]/}" ]; then
    echo "release-notes: no pull requests contributed notes for $TAG" >&2
    return 1
  fi

  printf '%s\n' "$notes"
}

# Run only when executed. Sourcing exposes the functions above without running
# anything, which is how hack/release-notes-test.sh exercises pick_prev
# directly -- the selection rule this script gets wrong most easily, and the
# one that cannot be reached through the fixture path.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
