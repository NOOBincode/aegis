#!/usr/bin/env bash
# scripts/controller-up.sh —— 部署 in-cluster controller（CRD reconcile + ScaleExecutor）。
# 前置：kind + kagent + aegis/controller:<版本> 镜像已 kind load。
set -euo pipefail
AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="controller-up"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err
kubectl get ns kagent >/dev/null 2>&1 || die 2 preflight missing_kagent "kagent 不存在；先 make up"
kubectl apply -f "${AEGIS_ROOT}/deploy/controller/rbac.yaml" >/dev/null
kubectl apply -f "${AEGIS_ROOT}/deploy/controller/deployment.yaml" >/dev/null
kubectl -n kagent rollout status deployment/aegis-controller --timeout=180s >/dev/null 2>&1 \
  || die 3 wait_ready timeout "kagent/aegis-controller 180s 未 Ready；查 kubectl -n kagent describe deploy aegis-controller"
info wait_ready component=aegis-controller result=ready
