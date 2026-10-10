#!/usr/bin/env bash
# test/teardown.sh -- E2E teardown for provider-github.
#
# uptest runs this script (--teardown-script) after the delete step has
# passed. It removes the disposable objects test/setup.sh created: the
# organization secrets the SecretAccess and Organization examples manage and the
# template and workflow repositories the Repository branches, RunnerGroup and
# Organization examples need. Deleting an object that is already gone is not an
# error, so running it twice is safe.
#
# Required environment when E2E_ORG_SECRETS=true or E2E_REPO_FIXTURES=true: the
# GitHub App credentials and E2E_ORG named in test/org-secrets.sh. It does
# nothing otherwise.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
rc=0

if [ "${E2E_ORG_SECRETS:-}" = "true" ]; then
  "${here}/org-secrets.sh" delete || rc=1
fi

if [ "${E2E_REPO_FIXTURES:-}" = "true" ]; then
  "${here}/repo-fixtures.sh" delete || rc=1
fi

exit "${rc}"
