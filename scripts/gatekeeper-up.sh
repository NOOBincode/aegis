#!/usr/bin/env bash
# scripts/gatekeeper-up.sh —— 部署 gatekeeper MCPServer（request_change 写意图入口）。
# 注：本脚本部署的是 MCP-only 形态（request_change 可用）；审批端点需要 HMAC secret，
# 当前 kagent MCPServer 的 secretRefs 挂载未生效，审批请走独立 Deployment/port-forward。
# 前置：kind + kagent + 镜像 aegis/gatekeeper:<版本> 已 kind load。
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="gatekeeper-up"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

kubectl get ns kagent >/dev/null 2>&1 || die 2 preflight missing_kagent "kagent 不存在；先 make up"
kubectl -n kagent get deploy kagent-controller >/dev/null 2>&1 || die 2 preflight missing_controller "kagent-controller 不存在；先 make up"


# 审批 Secret：HMAC 对称密钥，仅集群内；不打印值、不落 git。
if ! kubectl -n kagent get secret aegis-gatekeeper >/dev/null 2>&1; then
  value="$(openssl rand -base64 32 | tr -d '\n=' | tr '+/' '-_')"
  kubectl -n kagent create secret generic aegis-gatekeeper --from-literal=secret="$value" >/dev/null
  unset value
  info secret action=created name=aegis-gatekeeper result=ok
else
  info secret action=exists name=aegis-gatekeeper result=ok
fi

kubectl apply -f "${AEGIS_ROOT}/deploy/gatekeeper/rbac.yaml" >/dev/null
kubectl apply -f "${AEGIS_ROOT}/deploy/gatekeeper/mcpserver.yaml" >/dev/null
kubectl apply -f "${AEGIS_ROOT}/deploy/gatekeeper/approval-deployment.yaml" >/dev/null
kubectl apply -f "${AEGIS_ROOT}/deploy/security/kagent-mcp-networkpolicy.yaml" >/dev/null
info apply result=ok

kubectl -n kagent rollout status deployment/gatekeeper --timeout=180s >/dev/null 2>&1 \
  || die 3 wait_ready timeout "kagent/gatekeeper 180s 未 Ready；查 kubectl -n kagent describe deploy gatekeeper"
kubectl -n kagent rollout status deployment/gatekeeper-approval --timeout=180s >/dev/null 2>&1 \
  || die 3 wait_ready timeout "kagent/gatekeeper-approval 180s 未 Ready；查 kubectl -n kagent describe deploy gatekeeper-approval"
info wait_ready component=gatekeeper result=ready
info wait_ready component=gatekeeper-approval result=ready
