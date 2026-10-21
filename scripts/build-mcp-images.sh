#!/usr/bin/env bash
# scripts/build-mcp-images.sh —— 构建自研 MCP server 镜像并注入 kind（幂等可重跑）。
# 纪律：镜像 tag 与 deploy/mcp-servers/*.yaml 的 image 必须同步；改代码不换 tag 会被
# imagePullPolicy: IfNotPresent + kind load 的旧镜像掩盖（T1.2 曾因此服务端仍是空壳）。
# 退出码：0 成功；2 前置缺失；3 构建/注入失败。
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="build-mcp-images"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

CLUSTER_NAME="${AEGIS_CLUSTER_NAME:-aegis}"
K8S_READ_IMAGE="${AEGIS_K8S_READ_IMAGE:-}"
SCAN_IMAGE="${AEGIS_SCAN_IMAGE:-}"
PROM_IMAGE="${AEGIS_PROM_IMAGE:-}"
LOGS_IMAGE="${AEGIS_LOGS_IMAGE:-}"
EVENTS_IMAGE="${AEGIS_EVENTS_IMAGE:-}"
GATEKEEPER_IMAGE="${AEGIS_GATEKEEPER_IMAGE:-}"
CONTROLLER_IMAGE="${AEGIS_CONTROLLER_IMAGE:-}"
GITOPS_IMAGE="${AEGIS_GITOPS_IMAGE:-}"

[ -n "$K8S_READ_IMAGE" ] || die 2 preflight missing_var "AEGIS_K8S_READ_IMAGE 未设置（deploy/versions.env）"
[ -n "$SCAN_IMAGE" ] || die 2 preflight missing_var "AEGIS_SCAN_IMAGE 未设置（deploy/versions.env）"
[ -n "$PROM_IMAGE" ] || die 2 preflight missing_var "AEGIS_PROM_IMAGE 未设置（deploy/versions.env）"
[ -n "$LOGS_IMAGE" ] || die 2 preflight missing_var "AEGIS_LOGS_IMAGE 未设置（deploy/versions.env）"
[ -n "$EVENTS_IMAGE" ] || die 2 preflight missing_var "AEGIS_EVENTS_IMAGE 未设置（deploy/versions.env）"
[ -n "$GATEKEEPER_IMAGE" ] || die 2 preflight missing_var "AEGIS_GATEKEEPER_IMAGE 未设置（deploy/versions.env）"
[ -n "$CONTROLLER_IMAGE" ] || die 2 preflight missing_var "AEGIS_CONTROLLER_IMAGE 未设置（deploy/versions.env）"
[ -n "$GITOPS_IMAGE" ] || die 2 preflight missing_var "AEGIS_GITOPS_IMAGE 未设置（deploy/versions.env）"
cluster_exists "$CLUSTER_NAME" || die 2 preflight missing_cluster "kind 集群 ${CLUSTER_NAME} 不存在；先 make up"

build_one() { # build_one <module> <binary> <image> <dockerfile>
  local mod="$1" bin="$2" image="$3" df="$4"
  info build "component=${image}" action=go_build
  ( cd "${AEGIS_ROOT}/${mod}" && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "bin/${bin}" . ) \
    || die 3 build go_build_fail "构建 ${mod} 失败"
  info build "component=${image}" action=docker_build
  docker build -t "$image" -f "${AEGIS_ROOT}/${df}" "${AEGIS_ROOT}/${mod}" \
    || die 3 build docker_build_fail "构建镜像 ${image} 失败"
  info build "component=${image}" action=kind_load
  kind load docker-image "$image" --name "$CLUSTER_NAME" \
    || die 3 build kind_load_fail "kind load ${image} 失败"
  info build "component=${image}" result=ready
}

build_one mcp-servers/k8s-read k8s-read "$K8S_READ_IMAGE" mcp-servers/k8s-read/Dockerfile
build_one mcp-servers/scan scan "$SCAN_IMAGE" mcp-servers/scan/Dockerfile
build_one mcp-servers/prom prom "$PROM_IMAGE" mcp-servers/prom/Dockerfile
build_one mcp-servers/logs logs "$LOGS_IMAGE" mcp-servers/logs/Dockerfile
build_one mcp-servers/events events "$EVENTS_IMAGE" mcp-servers/events/Dockerfile
build_one mcp-servers/gitops gitops "$GITOPS_IMAGE" mcp-servers/gitops/Dockerfile

# gatekeeper 不是 MCP 模块，但同属“自研服务镜像”；单独构建以复用同一 tag 纪律。
info build "component=${GATEKEEPER_IMAGE}" action=go_build
( cd "${AEGIS_ROOT}" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o cmd/gatekeeper/bin/gatekeeper ./cmd/gatekeeper ) \
  || die 3 build go_build_fail "构建 cmd/gatekeeper 失败"
docker build -t "$GATEKEEPER_IMAGE" -f "${AEGIS_ROOT}/cmd/gatekeeper/Dockerfile" "${AEGIS_ROOT}/cmd/gatekeeper" \
  || die 3 build docker_build_fail "构建镜像 ${GATEKEEPER_IMAGE} 失败"
kind load docker-image "$GATEKEEPER_IMAGE" --name "$CLUSTER_NAME" \
  || die 3 build kind_load_fail "kind load ${GATEKEEPER_IMAGE} 失败"

info build "component=${CONTROLLER_IMAGE}" action=go_build
( cd "${AEGIS_ROOT}" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o cmd/controller/bin/controller ./cmd/controller ) \
  || die 3 build go_build_fail "构建 cmd/controller 失败"
docker build -t "$CONTROLLER_IMAGE" -f "${AEGIS_ROOT}/cmd/controller/Dockerfile" "${AEGIS_ROOT}/cmd/controller" \
  || die 3 build docker_build_fail "构建镜像 ${CONTROLLER_IMAGE} 失败"
kind load docker-image "$CONTROLLER_IMAGE" --name "$CLUSTER_NAME" \
  || die 3 build kind_load_fail "kind load ${CONTROLLER_IMAGE} 失败"
# scan 的镜像 tag 未变（0.1.0），kind load 后必须显式 rollout restart 才会用新镜像；
# k8s-read 的 tag 已升 0.2.0，由 mcp-up apply MCPServer 时更新 deployment。
for d in scan prom logs events gitops gatekeeper aegis-controller; do
  if kubectl -n kagent get deploy "$d" >/dev/null 2>&1; then
    info build component="$d" action=rollout_restart
    kubectl -n kagent rollout restart "deploy/$d" >/dev/null 2>&1 || true
  fi
done
info done result=ok "k8s_read=${K8S_READ_IMAGE}" "scan=${SCAN_IMAGE}" "prom=${PROM_IMAGE}" "logs=${LOGS_IMAGE}" "events=${EVENTS_IMAGE}" "gitops=${GITOPS_IMAGE}" "gatekeeper=${GATEKEEPER_IMAGE}" "controller=${CONTROLLER_IMAGE}" "gitops=${GITOPS_IMAGE}"
