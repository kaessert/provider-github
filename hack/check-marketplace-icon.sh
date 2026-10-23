#!/usr/bin/env bash
# hack/check-marketplace-icon.sh — fail unless the Upbound Marketplace logo
# would be accepted by Upbound's indexer. Copied verbatim into every provider;
# nothing in it varies per provider.
#
# Usage (from the provider root):
#   hack/check-marketplace-icon.sh [path]      # default: extensions/icons/icon.svg
#
# Run by release.yaml's security-gate before anything is pushed, by the
# release skill before a tag is created, and by the convention 0104 verify
# check. One implementation, three callers.
#
# Rules (the indexer's, no more). Every failure prints
#   FAIL [<rule>] <detail>
# and the exit status is 1 if any rule failed, 0 if all held.
#   exists     the file exists at exactly the path given
#   size       it is under 1 MB (1,048,576 bytes)
#   xml        it is well-formed XML whose root element is <svg>
#   square     equal width/height when both are present; a square viewBox when
#              one is present; and at least one of those two forms exists
#   deny-list  none of: <script  <foreignObject  <!DOCTYPE  <!ENTITY
#              xlink:href  an href= attribute  an on*= event attribute
#              (tokens matched case-insensitively; href and on* are read as
#              attributes of the parsed tree; an internal clip-path="url(#id)"
#              is not an href and passes)
#
# Needs only bash and python3.
set -euo pipefail

ICON="${1:-extensions/icons/icon.svg}"

exec python3 - "$ICON" <<'PY'
import os
import re
import sys
import xml.etree.ElementTree as ET

path = sys.argv[1]
MAX_BYTES = 1024 * 1024
failures = []


def fail(rule, detail):
    failures.append(rule)
    print(f"FAIL [{rule}] {path}: {detail}")


if not os.path.isfile(path):
    fail("exists", "no such file")
    sys.exit(1)

raw = open(path, "rb").read()

if len(raw) >= MAX_BYTES:
    fail("size", f"{len(raw)} bytes, must be under {MAX_BYTES}")

root = None
try:
    root = ET.fromstring(raw)
except ET.ParseError as e:
    fail("xml", f"not well-formed XML: {e}")
if root is not None and root.tag.rsplit("}", 1)[-1] != "svg":
    fail("xml", f"root element is <{root.tag}>, not <svg>")
    root = None


def num(s):
    m = re.fullmatch(r"\s*([0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?)\s*([a-zA-Z%]*)\s*", s)
    return (float(m.group(1)), m.group(2)) if m else None


if root is not None:
    w, h, vb = root.get("width"), root.get("height"), root.get("viewBox")
    have_wh = w is not None and h is not None
    if not have_wh and vb is None:
        fail("square", "neither width+height nor viewBox is present")
    if have_wh:
        nw, nh = num(w), num(h)
        if nw is None or nh is None or nw != nh:
            fail("square", f'width="{w}" and height="{h}" are not equal')
    if vb is not None:
        parts = [p for p in re.split(r"[\s,]+", vb.strip()) if p]
        try:
            vw, vh = float(parts[2]), float(parts[3])
            if len(parts) != 4 or abs(vw - vh) > 1e-9:
                raise ValueError
        except (ValueError, IndexError):
            fail("square", f'viewBox="{vb}" is not four numbers with equal width and height')

text = raw.decode("utf-8", errors="replace")
DENY = [
    ("<script", r"<script"),
    ("<foreignObject", r"<foreignObject"),
    ("<!DOCTYPE", r"<!DOCTYPE"),
    ("<!ENTITY", r"<!ENTITY"),
    ("xlink:href", r"xlink:href"),
]
for name, rx in DENY:
    if re.search(rx, text, re.IGNORECASE):
        fail("deny-list", f"contains {name}")

# href= and on*= are attributes, so they are read from the parsed tree: a
# word in text content or a comment is not one.
if root is not None:
    for el in root.iter():
        for attr in el.attrib:
            local = attr.rsplit("}", 1)[-1].lower()
            if local == "href":
                fail("deny-list", f"contains href= attribute on <{el.tag.rsplit('}', 1)[-1]}>")
            elif local.startswith("on"):
                fail("deny-list", f"contains on*= event attribute {local}= on <{el.tag.rsplit('}', 1)[-1]}>")

if failures:
    sys.exit(1)
print(f"PASS: {path}: exists, under 1 MB, well-formed <svg>, square, no deny-list token")
PY
