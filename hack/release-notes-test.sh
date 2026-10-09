#!/usr/bin/env bash
# hack/release-notes-test.sh — offline tests for hack/release-notes.sh.
#
# Feeds fixed pull request payloads through the renderer via RELNOTES_FIXTURE,
# so no network, no repository and no credentials are involved. Covers the
# behaviours the format promises: a batched pull request produces one bullet
# per line, NONE omits, a missing block falls back to the title, categories
# come from labels or from a title prefix, and every bullet is attributed.
#
# The fixture holds the repository's whole merged history; RELNOTES_FIXTURE_RANGE
# names the subset belonging to this release. That split is what lets a
# returning contributor (one with an earlier merged pull request outside the
# range) be told apart from a first-time one.
#
# Usage: hack/release-notes-test.sh      (from the repository root)
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/release-notes.sh"
fails=0

check() {
  local name="$1" expected="$2" actual="$3"
  if printf '%s' "$actual" | grep -qF -- "$expected"; then
    echo "PASS: $name"
  else
    echo "FAIL: $name"
    echo "  expected to find: $expected"
    echo "  in:"
    printf '%s\n' "$actual" | sed 's/^/    /'
    fails=$((fails + 1))
  fi
}

refute() {
  local name="$1" unexpected="$2" actual="$3"
  if printf '%s' "$actual" | grep -qF -- "$unexpected"; then
    echo "FAIL: $name"
    echo "  did not expect to find: $unexpected"
    fails=$((fails + 1))
  else
    echo "PASS: $name"
  fi
}

OUT=$(RELNOTES_FIXTURE="$HERE/testdata/release-notes-prs.json" \
      RELNOTES_FIXTURE_RANGE='[11,12,13,14,15,16,17,18,19]' \
      RELNOTES_FIXTURE_PREV='v0.1.0' \
      GITHUB_REPOSITORY='upbound/provider-infobloxnios' \
      "$SCRIPT" v0.2.0)

echo "--- rendered ---"
printf '%s\n' "$OUT" | sed 's/^/  /'
echo "----------------"

check "single-line block renders as a bullet" \
  "* HttpLoadbalancer no longer loops" "$OUT"

check "batched pull request renders line 1" \
  "* Bound the manager cache footprint" "$OUT"
check "batched pull request renders line 2" \
  "* Record the minimum credential scope" "$OUT"
check "batched pull request renders line 3" \
  "* Bypass the Secret cache" "$OUT"

refute "NONE omits the pull request" "bump a lint pin" "$OUT"

check "missing block falls back to the title" \
  "* docs: clarify the connection secret example by @newperson" "$OUT"

check "label 'bug' categorises as a fix"      "### Bug Fixes" "$OUT"
check "label 'enhancement' categorises"       "### Features" "$OUT"
check "title prefix 'docs:' categorises"      "### Documentation" "$OUT"
check "unrecognised change lands in Other"    "### Other Changes" "$OUT"

# Attribution and the two trailing sections are what GitHub's own notes give a
# reader, and this script replaces those notes rather than supplementing them.
# Dropping them silently would uncredit exactly the people most owed credit.
check "bullets carry author and pull request URL" \
  "by @acontributor in https://github.com/upbound/provider-infobloxnios/pull/11" "$OUT"

check "new contributors section exists"       "### New Contributors" "$OUT"
check "first-time contributor is credited" \
  "* @acontributor made their first contribution in" "$OUT"
check "contributor with no block is still credited" \
  "* @newperson made their first contribution in" "$OUT"
refute "returning contributor is NOT called new" \
  "* @kaessert made their first contribution in" "$OUT"

check "full changelog link spans the release range" \
  "**Full Changelog**: https://github.com/upbound/provider-infobloxnios/compare/v0.1.0...v0.2.0" "$OUT"

# NONE is per line, not per body. The pull request template ships a NONE block
# as its default, so a contributor who writes their note above it without
# deleting it has two blocks -- the shape that published the literal word NONE
# as a release-note bullet in v0.2.0-rc3.
check "real lines survive alongside a leftover NONE block" \
  "* First real note line" "$OUT"
check "second real line survives too" \
  "* Second real note line" "$OUT"
refute "NONE is never published as a bullet" \
  "* NONE by @" "$OUT"

refute "lowercase none omits just as NONE does" \
  "lowercase none should still omit" "$OUT"

check "an empty block falls back to the title rather than vanishing" \
  "* chore: an empty block, most likely a slip by @" "$OUT"

# A leading "-" or "*" is a list marker only when followed by a separator.
# "* *Breaking:**" shipped in provider-infobloxnios v0.4.0's published release
# body because an unconditional [-*]? ate the first "*" of a bold-prefixed
# bullet -- these cases reproduce that exact input (PR #19 below mirrors the
# ZoneDelegated.spec.forProvider.delegateTo bullet cited in the defect) plus
# every other marker shape the format allows, asserted end to end.
check "dash-prefixed bullet loses its marker exactly once" \
  "* Dash-prefixed bullet loses its marker" "$OUT"
refute "dash-prefixed bullet does not keep the dash" \
  "* - Dash-prefixed" "$OUT"

check "star-prefixed bullet loses its marker exactly once" \
  "* Star-prefixed bullet loses its marker" "$OUT"
refute "star-prefixed bullet does not keep a doubled marker" \
  "* * Star-prefixed" "$OUT"

check "bare bullet with no marker is untouched" \
  "* Bare bullet keeps no marker to lose" "$OUT"

check "bold-prefixed bullet keeps both asterisks" \
  "* **Bold:** Bold-prefixed bullet keeps both asterisks" "$OUT"
refute "bold-prefixed bullet is not left one asterisk short" \
  "* *Bold:** Bold-prefixed" "$OUT"

check "italic-prefixed bullet keeps its single asterisks" \
  "* *Italic-prefixed bullet* keeps its single asterisks" "$OUT"

check "the published defect's own bullet renders bold, not eaten" \
  "* **Breaking:** \`ZoneDelegated.spec.forProvider.delegateTo\` becomes immutable" "$OUT"
refute "the published defect does not recur" \
  "* *Breaking:** \`ZoneDelegated" "$OUT"

# ── Baseline selection ──────────────────────────────────────────────────────
# pick_prev decides what a release is measured against. It is not reachable
# through the fixture path above, so it is exercised directly by sourcing the
# script. Getting this wrong is silent and expensive: measuring a candidate
# against the previous candidate reports only the delta between them, and
# leaves the FINAL release reporting almost nothing, because by then its
# immediate predecessor is its own last candidate.
# shellcheck source=hack/release-notes.sh
source "$SCRIPT"

pick() { TAG="$1"; jq -c . "$2" | pick_prev; }

check "candidate skips earlier candidates for the last stable release" \
  "v0.1.0" "$(pick v0.2.0-rc3 "$HERE/testdata/release-notes-releases.json")"

check "second candidate does not measure against the first" \
  "v0.1.0" "$(pick v0.2.0-rc2 "$HERE/testdata/release-notes-releases.json")"

check "final release measures against last stable, not its own candidates" \
  "v0.1.0" "$(pick v0.2.0 "$HERE/testdata/release-notes-releases.json")"

check "a draft release is never chosen as the baseline" \
  "v0.1.0" "$(pick v0.3.0 "$HERE/testdata/release-notes-releases.json")"

check "with no stable release yet, falls back to newest candidate" \
  "v0.1.0-rc2" "$(pick v0.1.0-rc3 "$HERE/testdata/release-notes-releases-nostable.json")"

refute "the tag being released is never its own baseline" \
  "v0.2.0-rc2" "$(pick v0.2.0-rc2 "$HERE/testdata/release-notes-releases.json")"

# A backport release on an older line publishes AFTER a newer minor by
# construction (infobloxnios v0.1.2 published after v0.2.2), so date order is
# never the right order -- the rule is the highest stable semantic version
# strictly below $TAG.
check "backport published later is not chosen over a newer minor" \
  "v0.2.2" "$(pick v0.3.0 "$HERE/testdata/release-notes-releases-backport.json")"
refute "the later-published backport does not win on date" \
  "v0.1.2" "$(pick v0.3.0 "$HERE/testdata/release-notes-releases-backport.json")"
check "the backport line itself measures against its own predecessor" \
  "v0.1.1" "$(pick v0.1.2 "$HERE/testdata/release-notes-releases-backport.json")"
check "the newer minor line is unaffected by the backport's existence" \
  "v0.2.1" "$(pick v0.2.2 "$HERE/testdata/release-notes-releases-backport.json")"

# hack/release-notes.sh:138 used to pass the full merged-PR JSON through argv
# via --argjson, which blows Linux's 131072-byte MAX_ARG_STRLEN cap on a
# single argv string and silently produced no notes (jq: Argument list too
# long). This fixture is 235384 bytes.
set +e
BIG_OUT=$(RELNOTES_FIXTURE="$HERE/testdata/release-notes-prs-large.json" \
          RELNOTES_FIXTURE_RANGE='[9999]' \
          RELNOTES_FIXTURE_PREV='v0.1.0' \
          GITHUB_REPOSITORY='upbound/provider-infobloxnios' \
          "$SCRIPT" v0.2.0 2>&1)
BIG_RC=$?
set -e
if [ "$BIG_RC" -eq 0 ]; then
  echo "PASS: an oversized all_merged_prs payload does not exit non-zero"
else
  echo "FAIL: an oversized all_merged_prs payload does not exit non-zero"
  echo "  exit code: $BIG_RC"
  printf '%s\n' "$BIG_OUT" | sed 's/^/    /'
  fails=$((fails + 1))
fi
check "notes assemble past the argv limit" \
  "Assembled notes correctly even when merged-PR JSON exceeds the argv limit" "$BIG_OUT"

echo
if [ "$fails" -eq 0 ]; then
  echo "release-notes-test: all checks passed"
else
  echo "release-notes-test: $fails check(s) failed"
  exit 1
fi
