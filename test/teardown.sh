#!/usr/bin/env bash
# test/teardown.sh -- E2E teardown for provider-github.
#
# uptest runs this script (--teardown-script) after the delete step has
# passed. It removes the disposable organization secrets test/setup.sh created
# for the SecretAccess examples. Deleting a secret that is already gone is not
# an error, so running it twice is safe.
#
# Required environment when E2E_ORG_SECRETS=true: the GitHub App credentials and
# E2E_ORG named in test/org-secrets.sh. It does nothing otherwise.
set -euo pipefail

if [ "${E2E_ORG_SECRETS:-}" = "true" ]; then
  "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/org-secrets.sh" delete
fi
