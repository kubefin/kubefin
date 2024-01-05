#!/usr/bin/env bash

# Copyright 2023 The KubeFin Authors
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

KUBEFIN_GO_PACKAGE="kubefin.dev/kubefin"

KUBEFIN_TARGET_SOURCE=(
  kubefin-agent=cmd/kubefin-agent
  kubefin-cost-analyzer=cmd/kubefin-cost-analyzer
)

function get_mimir_server_ip() {
  kubeconfig=$1
  server_node_ip=$(kubectl get node --kubeconfig="${kubeconfig}" -ojson | jq .items[0].status.addresses[0].address | sed 's/"//g')
  echo "${server_node_ip}"
}

function get_mimir_server_port() {
  kubeconfig=$1
  server_node_port=$(kubectl get svc -nkubefin-system mimir --kubeconfig="${kubeconfig}" -ojson | jq .spec.ports[0].nodePort)
  echo "${server_node_port}"
}

function init_primary_config() {
  cloud_provider=$1
  cluster_name=$2
  multi_cluster_enable=$3
  dashboard_tag=$4

  mimir_service_type="ClusterIP"
  if [[ "${multi_cluster_enable}" == "true" ]]; then
    mimir_service_type="LoadBalancer"
  fi

  rm -rf config_primary
  cp -r config_template config_primary

  sed -i'' -e "s/{REMOTE_WRITE_ADDRESS}/http:\/\/mimir.kubefin-system.svc.cluster.local:9009\/api\/v1\/push/g" config_primary/core/configmap/otel.yaml
  sed -i'' -e "s/{KUBEFIN_AGENT_IMAGE}/ko:\/\/kubefin.dev\/kubefin\/cmd\/kubefin-agent/g" config_primary/core/deployments/kubefin-agent.yaml
  sed -i'' -e "s/{CLOUD_PROVIDER}/${cloud_provider}/g" config_primary/core/deployments/kubefin-agent.yaml
  sed -i'' -e "s/{CLUSTER_ID}//g" config_primary/core/deployments/kubefin-agent.yaml
  sed -i'' -e "s/{CLUSTER_NAME}/${cluster_name}/g" config_primary/core/deployments/kubefin-agent.yaml
  sed -i'' -e "s/{MIMIR_SERVICE_TYPE}/${mimir_service_type}/g" config_primary/third_party/mimir.yaml
  sed -i'' -e "s/{KUBEFIN_DASHBOARD_VERSION}/${dashboard_tag}/" config_primary/core/deployments/kubefin-cost-analyzer.yaml
}

function init_secondary_config() {
  cloud_provider=$1
  cluster_name=$2
  metrics_push_addr=$3

  rm -rf config_secondary
  cp -r config_template config_secondary

  rm -rf config_secondary/third_party
  rm -rf config_secondary/core/deployments/kubefin-cost-analyzer.yaml

  sed -i'' -e "s/{REMOTE_WRITE_ADDRESS}/${metrics_push_addr}/g" config_secondary/core/configmap/otel.yaml
  sed -i'' -e "s/{KUBEFIN_AGENT_IMAGE}/ko:\/\/kubefin.dev\/kubefin\/cmd\/kubefin-agent/g" config_secondary/core/deployments/kubefin-agent.yaml
  sed -i'' -e "s/{CLOUD_PROVIDER}/${cloud_provider}/g" config_secondary/core/deployments/kubefin-agent.yaml
  sed -i'' -e "s/{CLUSTER_ID}//g" config_secondary/core/deployments/kubefin-agent.yaml
  sed -i'' -e "s/{CLUSTER_NAME}/${cluster_name}/g" config_secondary/core/deployments/kubefin-agent.yaml
}

function get_build_arch() {
  platform=$(uname -m)

  if [[ "$platform" == "aarch64" || "$platform" == "arm64" ]]; then
    echo "arm64"
  else
    echo "amd64"
  fi
}

function echo_info() {
  echo -e "\033[34m[INFO] $1\033[0m"
}

function echo_note() {
  echo -e "\033[33m$1\033[0m"
}

function cmd_must_exist {
    local CMD=$(command -v ${1})
    if [[ ! -x ${CMD} ]]; then
      echo "Please install ${1} and verify they are in \$PATH."
      exit 1
    fi
}

function create_gopath_tree() {
  # $1: the root path of repo
  local repo_root=$1
  # $2: go path
  local go_path=$2

  local kubefin_go_package="kubefin.dev/kubefin"

  local go_pkg_dir="${go_path}/src/${kubefin_go_package}"
  go_pkg_dir=$(dirname "${go_pkg_dir}")

  mkdir -p "${go_pkg_dir}"

  if [[ ! -e "${go_pkg_dir}" || "$(readlink "${go_pkg_dir}")" != "${repo_root}" ]]; then
    ln -snf "${repo_root}" "${go_pkg_dir}"
  fi
}

function get_target_source() {
  local target=$1
  for s in "${KUBEFIN_TARGET_SOURCE[@]}"; do
    if [[ "$s" == ${target}=* ]]; then
      echo "${s##${target}=}"
      return
    fi
  done
}

function version_ldflags() {
  # Git information
  GIT_VERSION=$(get_version)
  GIT_COMMIT_HASH=$(git rev-parse HEAD)
  if git_status=$(git status --porcelain 2>/dev/null) && [[ -z ${git_status} ]]; then
    GIT_TREESTATE="clean"
  else
    GIT_TREESTATE="dirty"
  fi
  BUILDDATE=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
  LDFLAGS="-X kubefin.dev/kubefin/pkg/version.gitVersion=${GIT_VERSION} \
                        -X kubefin.dev/kubefin/pkg/version.gitCommit=${GIT_COMMIT_HASH} \
                        -X kubefin.dev/kubefin/pkg/version.gitTreeState=${GIT_TREESTATE} \
                        -X kubefin.dev/kubefin/pkg/version.buildDate=${BUILDDATE}"
  echo $LDFLAGS
}

function get_version() {
  git describe --tags --dirty
}

function host_platform() {
  echo "$(go env GOHOSTOS)/$(go env GOHOSTARCH)"
}
