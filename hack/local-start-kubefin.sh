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

set -o errexit
set -o pipefail

export KO_DOCKER_REPO=kind.local
K8S_VERSION=${K8S_VERSION:-v1.27.3}
TMP_DIR=$(mktemp -d)
REPO_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
source "${REPO_ROOT}"/hack/utils.sh

SYS_ARCH=${SYS_ARCH:-linux/$(get_build_arch)}

function cleanup() {
    rm -rf "${TMP_DIR}"
}

trap cleanup EXIT SIGINT

# check necessary tools
cmd_must_exist "go"
cmd_must_exist "kind"
cmd_must_exist "docker"
cmd_must_exist "ko"
cmd_must_exist "kubectl"

# setup kind cluster
echo_info "Start to setup kind cluster..."
clusters=$(kind get clusters)

if [[ ! $clusters =~ 'kubefin-server' ]]
then
  kind create cluster --name kubefin-server --kubeconfig=${HOME}/.kube/kubefin-server.config --image kindest/node:"${K8S_VERSION}"
fi

if [[ ! $clusters =~ 'cluster-1' ]]
then
  kind create cluster --name cluster-1 --kubeconfig=${HOME}/.kube/cluster-1.config --image kindest/node:"${K8S_VERSION}"
fi

# Build dashboard image for primary cluster
docker build -f "${REPO_ROOT}"/dashboard/Dockerfile -t kubefin/kubefin-dashboard:devel "${REPO_ROOT}"/dashboard

# Download necessary images(speed up the launch time)
docker pull otel/opentelemetry-collector-contrib:0.72.0
docker pull grafana/grafana:9.1.0
docker pull grafana/mimir:2.6.0

kind load docker-image otel/opentelemetry-collector-contrib:0.72.0 --name kubefin-server
kind load docker-image otel/opentelemetry-collector-contrib:0.72.0 --name cluster-1
kind load docker-image grafana/grafana:9.1.0 --name kubefin-server
kind load docker-image grafana/mimir:2.6.0 --name kubefin-server
kind load docker-image kubefin/kubefin-dashboard:devel --name kubefin-server

# install metrics server
echo_info "Start to install metrics server..."
wget https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.6.2/components.yaml -O "${TMP_DIR}"/components.yaml
# ignore k8s certificate checking, if not, metrics-server could not startup
sed -i'' -e 's/args:/args:\n        - --kubelet-insecure-tls/g' "${TMP_DIR}"/components.yaml

kubectl apply -f "${TMP_DIR}"/components.yaml --kubeconfig=${HOME}/.kube/kubefin-server.config
kubectl apply -f "${TMP_DIR}"/components.yaml --kubeconfig=${HOME}/.kube/cluster-1.config

# Setup primary cluster
echo_info "Start to setup primary cluster..."
hack/init-primary-config.sh auto kubefin-server true "devel"

export KIND_CLUSTER_NAME=kubefin-server
export KUBECONFIG=${HOME}/.kube/kubefin-server.config
ko apply --selector kubefin.dev/crd-install=true -Rf config_primary
kubectl wait --for=condition=Established --all crd
ko apply -Rf config_primary --platform="${SYS_ARCH}"

echo_info "Wait mimir to be ready..."
kubectl wait --for=condition=Ready pod -nkubefin-system mimir-0 --kubeconfig="${HOME}"/.kube/kubefin-server.config --timeout=600s

server_node_ip=$(get_mimir_server_ip "${HOME}"/.kube/kubefin-server.config)
server_node_port=$(get_mimir_server_port "${HOME}"/.kube/kubefin-server.config)

# Setup secondary cluster
echo_info "Start to setup secondary cluster..."
hack/init-secondary-config.sh auto cluster-1 http://"${server_node_ip}:${server_node_port}"/api/v1/push

export KIND_CLUSTER_NAME=cluster-1
export KUBECONFIG=${HOME}/.kube/cluster-1.config
ko apply --selector kubefin.dev/crd-install=true -Rf config_secondary
kubectl wait --for=condition=Established --all crd
ko apply -Rf config_secondary --platform="${SYS_ARCH}"

echo_info "Wait KubeFin get ready..."
kubectl rollout status deployment/kubefin-agent -nkubefin-system --kubeconfig="${HOME}"/.kube/kubefin-server.config --timeout=600s
kubectl rollout status deployment/kubefin-cost-analyzer -nkubefin-system --kubeconfig="${HOME}"/.kube/kubefin-server.config --timeout=600s

echo_info "Run the following command to export the API and Web UI:"
echo_note "kubectl port-forward -nkubefin-system svc/kubefin-cost-analyzer-service --kubeconfig=${HOME}/.kube/kubefin-server.config --address='0.0.0.0' 8080 3000"
