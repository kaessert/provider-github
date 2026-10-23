#!/usr/bin/env bash
# hack/check-marketplace-icon-test.sh — offline tests for
# hack/check-marketplace-icon.sh.
#
# Builds one fixture per rule in a scratch directory and asserts that every
# invalid fixture fails naming the rule it broke, and every valid one passes.
# Nothing is read from the repository and nothing is written outside the
# scratch directory.
#
# Usage: hack/check-marketplace-icon-test.sh      (from anywhere)
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/check-marketplace-icon.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fails=0

NS='xmlns="http://www.w3.org/2000/svg"'
XL='xmlns:xlink="http://www.w3.org/1999/xlink"'
BODY='<rect width="10" height="10"/>'

svg() { printf '<svg %s %s>%s</svg>\n' "$NS" "$1" "${2:-$BODY}"; }

# expect_pass NAME FILE
expect_pass() {
  local out rc=0
  out="$("$SCRIPT" "$2" 2>&1)" || rc=$?
  if [ "$rc" -eq 0 ] && grep -q '^PASS:' <<<"$out"; then
    echo "PASS: $1"
  else
    echo "FAIL: $1 (rc=$rc)"; sed 's/^/    /' <<<"$out"; fails=$((fails + 1))
  fi
}

# expect_fail NAME FILE RULE [DETAIL]
expect_fail() {
  local out rc=0 want="FAIL \[$3\]"
  out="$("$SCRIPT" "$2" 2>&1)" || rc=$?
  if [ "$rc" -ne 0 ] && grep -q -- "$want" <<<"$out" && { [ -z "${4:-}" ] || grep -qF -- "$4" <<<"$out"; }; then
    echo "PASS: $1"
  else
    echo "FAIL: $1 (rc=$rc, wanted $want ${4:-})"; sed 's/^/    /' <<<"$out"; fails=$((fails + 1))
  fi
}

# ── valid ────────────────────────────────────────────────────────────
svg 'width="100" height="100" viewBox="0 0 48 48"' >"$TMP/ok.svg"
expect_pass "valid: width, height and viewBox" "$TMP/ok.svg"

svg 'width="100" height="100"' >"$TMP/ok-wh.svg"
expect_pass "valid: width and height only" "$TMP/ok-wh.svg"

svg 'viewBox="-2.88 -2.88 53.76 53.76"' >"$TMP/ok-vb.svg"
expect_pass "valid: viewBox only, fractional and negative origin" "$TMP/ok-vb.svg"

svg 'width="100px" height="100px"' >"$TMP/ok-px.svg"
expect_pass "valid: width and height with matching units" "$TMP/ok-px.svg"

svg 'width="100" height="100" viewBox="0 0 48 48"' \
  '<g clip-path="url(#x)"><rect width="10" height="10"/></g><defs><clipPath id="x"><rect width="5" height="5"/></clipPath></defs>' \
  >"$TMP/ok-clip.svg"
expect_pass "valid: internal clip-path=\"url(#x)\" is not an href" "$TMP/ok-clip.svg"

# ── exists ───────────────────────────────────────────────────────────
expect_fail "exists: missing file" "$TMP/nope.svg" exists
mkdir "$TMP/adir.svg"
expect_fail "exists: a directory is not the file" "$TMP/adir.svg" exists
(cd "$TMP" && mkdir -p empty && cd empty && rc=0 && out="$("$SCRIPT" 2>&1)" || rc=$?
  if [ "${rc:-0}" -ne 0 ] && grep -q 'FAIL \[exists\] extensions/icons/icon.svg' <<<"$out"; then
    echo "PASS: exists: default path is extensions/icons/icon.svg"
  else
    echo "FAIL: exists: default path (rc=${rc:-0})"; sed 's/^/    /' <<<"$out"; exit 1
  fi) || fails=$((fails + 1))

# ── size ─────────────────────────────────────────────────────────────
python3 - "$TMP/big.svg" <<'PY'
import sys
pad = "<!-- " + "x" * (1024 * 1024) + " -->"
open(sys.argv[1], "w").write(f'<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1">{pad}</svg>')
PY
expect_fail "size: 1 MB or more" "$TMP/big.svg" size

# ── xml ──────────────────────────────────────────────────────────────
printf 'this is not xml at all\n' >"$TMP/notxml.svg"
expect_fail "xml: not XML" "$TMP/notxml.svg" xml
printf '<svg %s width="1" height="1"><rect></svg>\n' "$NS" >"$TMP/broken.svg"
expect_fail "xml: unclosed element" "$TMP/broken.svg" xml
printf '<html %s width="1" height="1"/>\n' "$NS" >"$TMP/html.svg"
expect_fail "xml: root is not <svg>" "$TMP/html.svg" xml "not <svg>"
: >"$TMP/empty.svg"
expect_fail "xml: empty file" "$TMP/empty.svg" xml

# ── square ───────────────────────────────────────────────────────────
svg 'width="100" height="50"' >"$TMP/sq-wh.svg"
expect_fail "square: width != height" "$TMP/sq-wh.svg" square
svg 'viewBox="0 0 48 24"' >"$TMP/sq-vb.svg"
expect_fail "square: viewBox not square" "$TMP/sq-vb.svg" square
svg 'width="100" height="100" viewBox="0 0 48 24"' >"$TMP/sq-mix.svg"
expect_fail "square: square width/height does not excuse a rectangular viewBox" "$TMP/sq-mix.svg" square
svg 'viewBox="0 0 48"' >"$TMP/sq-vb3.svg"
expect_fail "square: viewBox with three numbers" "$TMP/sq-vb3.svg" square
svg '' >"$TMP/sq-none.svg"
expect_fail "square: neither width+height nor viewBox" "$TMP/sq-none.svg" square "neither"
svg 'width="100"' >"$TMP/sq-w-only.svg"
expect_fail "square: width without height and no viewBox" "$TMP/sq-w-only.svg" square

# ── deny-list ────────────────────────────────────────────────────────
SQ='width="10" height="10"'
svg "$SQ" '<script>alert(1)</script>' >"$TMP/d-script.svg"
expect_fail "deny-list: <script" "$TMP/d-script.svg" deny-list "<script"
svg "$SQ" '<foreignObject><div/></foreignObject>' >"$TMP/d-fo.svg"
expect_fail "deny-list: <foreignObject" "$TMP/d-fo.svg" deny-list "<foreignObject"
printf '<!DOCTYPE svg>\n<svg %s %s/>\n' "$NS" "$SQ" >"$TMP/d-doctype.svg"
expect_fail "deny-list: <!DOCTYPE" "$TMP/d-doctype.svg" deny-list "<!DOCTYPE"
printf '<!DOCTYPE svg [<!ENTITY a "b">]>\n<svg %s %s/>\n' "$NS" "$SQ" >"$TMP/d-entity.svg"
expect_fail "deny-list: <!ENTITY" "$TMP/d-entity.svg" deny-list "<!ENTITY"
printf '<svg %s %s %s><use xlink:href="#a"/></svg>\n' "$NS" "$XL" "$SQ" >"$TMP/d-xlink.svg"
expect_fail "deny-list: xlink:href" "$TMP/d-xlink.svg" deny-list "xlink:href"
printf '<svg %s %s><use href="https://example.com/a.svg#a"/></svg>\n' "$NS" "$SQ" >"$TMP/d-href.svg"
expect_fail "deny-list: href= attribute" "$TMP/d-href.svg" deny-list "href="
printf '<svg %s %s><a href="https://example.com"><rect/></a></svg>\n' "$NS" "$SQ" >"$TMP/d-a.svg"
expect_fail "deny-list: href= on an anchor" "$TMP/d-a.svg" deny-list "href="
printf '<svg %s %s onload="alert(1)"/>\n' "$NS" "$SQ" >"$TMP/d-onload.svg"
expect_fail "deny-list: onload= event attribute" "$TMP/d-onload.svg" deny-list "on*="
printf '<svg %s %s><rect onclick="x()"/></svg>\n' "$NS" "$SQ" >"$TMP/d-onclick.svg"
expect_fail "deny-list: onclick= event attribute" "$TMP/d-onclick.svg" deny-list "on*="
printf '<svg %s %s><SCRIPT>1</SCRIPT></svg>\n' "$NS" "$SQ" >"$TMP/d-upper.svg"
expect_fail "deny-list: matched case-insensitively" "$TMP/d-upper.svg" deny-list "<script"

# A word in text content is not an attribute.
printf '<svg %s %s><title>this href= and onward</title></svg>\n' "$NS" "$SQ" >"$TMP/ok-text.svg"
expect_pass "valid: href= in text content is not an attribute" "$TMP/ok-text.svg"

# ── every rule is reported, not only the first ───────────────────────
svg 'width="1" height="2"' '<script/>' >"$TMP/multi.svg"
expect_fail "multi: square failure reported" "$TMP/multi.svg" square
expect_fail "multi: deny-list failure reported alongside" "$TMP/multi.svg" deny-list

if [ "$fails" -ne 0 ]; then
  echo "$fails check(s) FAILED"
  exit 1
fi
echo "all check-marketplace-icon tests passed"
