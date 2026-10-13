#!/usr/bin/env bash

# Copyright 2022 The Crossplane Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Adds a managed resource type in both API scopes: the cluster-scoped and the
# namespaced API packages, and one controller package serving both.
# Required environment: PROVIDER, GROUP, KIND, PROJECT_REPO, GOMPLATE.
# Run `make generate` afterwards to produce the deepcopy and managed-resource
# methods, the CRDs and the controller registration.
set -euo pipefail

APIVERSION="${APIVERSION:-v1alpha1}"
echo "Adding type ${KIND} to group ${GROUP} with version ${APIVERSION}"

export GROUP
export KIND
export APIVERSION
export PROVIDER
export PROJECT_REPO

kind_lower=$(echo "${KIND}" | tr "[:upper:]" "[:lower:]")
group_lower=$(echo "${GROUP}" | tr "[:upper:]" "[:lower:]")

# The plural the CRD gets, which the RBAC markers must name.
case "${kind_lower}" in
  *s|*x|*ch|*sh) PLURAL="${kind_lower}es" ;;
  *[!aeiou]y) PLURAL="${kind_lower%y}ies" ;;
  *) PLURAL="${kind_lower}s" ;;
esac
export PLURAL

for scope in cluster namespaced; do
  export SCOPE="${scope}"
  api_dir="apis/${scope}/${group_lower}/${APIVERSION}"
  mkdir -p "${api_dir}"
  ${GOMPLATE} < "hack/helpers/apis/GROUP_LOWER/APIVERSION/KIND_LOWER_types.go.tmpl" > "${api_dir}/${kind_lower}_types.go"
  # The package files are shared by every kind in the group: keep existing ones.
  for f in doc groupversion_info; do
    if [ ! -e "${api_dir}/${f}.go" ]; then
      ${GOMPLATE} < "hack/helpers/apis/GROUP_LOWER/APIVERSION/${f}.go.tmpl" > "${api_dir}/${f}.go"
    fi
  done
done

mkdir -p "internal/controller/${kind_lower}"
for f in KIND_LOWER cluster namespaced rbac KIND_LOWER_test; do
  out="${f//KIND_LOWER/${kind_lower}}"
  ${GOMPLATE} < "hack/helpers/controller/KIND_LOWER/${f}.go.tmpl" > "internal/controller/${kind_lower}/${out}.go"
done

# The templates cannot align struct fields and constants for every kind name.
gofmt -w "apis/cluster/${group_lower}/${APIVERSION}" "apis/namespaced/${group_lower}/${APIVERSION}" "internal/controller/${kind_lower}"
