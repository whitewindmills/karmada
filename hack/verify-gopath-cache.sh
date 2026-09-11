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
source hack/util.sh

# Model Go's default cache resolution without creating or downloading any cache files.
function go() {
  if [[ "$1" == "env" && "$2" == "GOMODCACHE" ]]; then
    printf '%s\n' "${GOMODCACHE:-${GOPATH}/pkg/mod}"
    return
  fi
  echo "Unexpected Go invocation in GOPATH cache regression: $*" >&2
  return 1
}

for mode in default explicit; do
  (
    export GOPATH="/original-gopath"
    if [[ "${mode}" == "default" ]]; then
      unset GOMODCACHE
      expected="/original-gopath/pkg/mod"
    else
      export GOMODCACHE="/explicit-module-cache"
      expected="${GOMODCACHE}"
    fi
    for temporary_path in "/temporary-gopath" "/another-temporary-gopath"; do
      util::set_gopath "${temporary_path}"
      actual=$(go env GOMODCACHE)
      if [[ "${GOPATH}" != "${temporary_path}" || "${actual}" != "${expected}" ]]; then
        echo "${mode} cache moved while switching GOPATH: ${actual}, expected ${expected}" >&2
        exit 1
      fi
    done
  )
done

echo "Temporary GOPATH changes preserve default and explicit module caches."
