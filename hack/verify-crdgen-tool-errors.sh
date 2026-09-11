#!/usr/bin/env bash
# Copyright 2026 The Karmada Authors.
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

set -o errexit
set -o nounset
set -o pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

function go() {
  if [[ "$1" != "install" ]]; then
    command go "$@"
    return
  fi
  if [[ "$2" == "${CRDGEN_TEST_FAILING_TOOL}"@* ]]; then
    echo "simulated installation failure: ${CRDGEN_TEST_FAILING_TOOL}" >&2
    return 42
  fi
}
export -f go

for tool in "sigs.k8s.io/controller-tools/cmd/controller-gen" "github.com/mikefarah/yq/v4"; do
  export CRDGEN_TEST_FAILING_TOOL="${tool}"
  status=0
  output=$(bash hack/update-crdgen.sh 2>&1) || status=$?
  if [[ "${status}" -ne 42 || "${output}" != *"simulated installation failure: ${tool}"* ]]; then
    echo "CRD generation lost the installation failure for ${tool} (exit ${status}):" >&2
    echo "${output}" >&2
    exit 1
  fi
done

echo "CRD tool installation failures retain their exit status and diagnostics."
