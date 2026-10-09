#!/usr/bin/env bash
# run-update-tester.sh -- generic post-assert hook for uptest.
#
# Every post-assert-*.sh file in this directory is a symlink to this script. It
# resolves the repository root and execs the pinned crossplane-update-tester
# module's `hook` subcommand, which derives the example manifest from its own
# invocation name ($0, the symlink name -- bash does not resolve symlinks before
# `basename`) and runs the post-assert sequence for it.
#
# Symlink naming (the slug is the example directory name):
#   post-assert-<slug>.sh            -> examples/<slug>/<slug>.yaml
#   post-assert-<slug>-namespaced.sh -> examples/<slug>/<slug>-namespaced.yaml
#
# Adding a resource only needs symlinks, no new script and no chmod:
#   cd test/hooks && ln -s run-update-tester.sh post-assert-<slug>.sh
#   cd test/hooks && ln -s run-update-tester.sh post-assert-<slug>-namespaced.sh
#
# uptest resolves a hook annotation path relative to the example manifest's own
# directory, so an example references this directory as
# ../../test/hooks/<name>.sh. A bare test/hooks/... path resolves under
# examples/ and the hook silently never runs.
#
# --skip-converge drops the per-resource convergence steps. Convergence is
# observed once for the whole run by test/hooks/converge-barrier.sh, which the
# Makefile passes to uptest as --post-assert-script. The per-field `run` step
# stays here because it patches resources and cannot interleave.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

exec go -C "$ROOT/tools/update-tester" tool crossplane-update-tester \
    hook "$(basename "$0")" --root "$ROOT" --skip-converge
