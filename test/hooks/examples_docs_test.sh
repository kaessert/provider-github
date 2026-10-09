#!/usr/bin/env bash
# examples_docs_test.sh -- every YAML document in examples/ has content.
#
# `crossplane xpkg build` parses every file under examples/ and rejects a
# document made only of comments ("did not find expected node content"), which
# fails `make build` for the whole provider. A trailing comment block after a
# final `---` separator is the usual way to write one, so this test splits each
# example on its separator lines and fails on any document with no
# non-comment, non-blank line.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

python3 - "${ROOT}/examples" <<'PY'
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
bad = []
for path in sorted(root.rglob("*.yaml")):
    docs = [[]]
    for line in path.read_text().splitlines():
        if line.rstrip() == "---":
            docs.append([])
        else:
            docs[-1].append(line)
    for i, doc in enumerate(docs):
        if i == 0 and not any(l.strip() for l in doc):
            continue  # a leading separator leaves an empty first document
        if not any(l.strip() and not l.lstrip().startswith("#") for l in doc):
            bad.append(f"{path.relative_to(root.parent)}: document {i} has only comments or is empty")
for b in bad:
    print("FAIL:", b)
sys.exit(1 if bad else 0)
PY
