#!/usr/bin/env bash
# scripts/mcp-up.sh —— 把自研 MCP server / 只读 RBAC / 可选 Agent 部署进 kind（M1 工具链）。
# 前置：make up（集群+kagent）与 make mcp-images（镜像已 kind load）。
# 用法：mcp-up.sh [--with-agent] [--with-networkpolicy]
#   --with-agent         同时 apply deploy/agent（LLM 通道未就绪时 Agent 会不可用，默认不 apply）
#   --with-networkpolicy 同时 apply NetworkPolicy（本 kind 实测强制；换 CNI 后需复测）
# 退出码：0 成功；2 前置缺失；3 部署/就绪超时。
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="mcp-up"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

WITH_AGENT=0
WITH_NETPOL=0
for a in "$@"; do
  case "$a" in
    --with-agent) WITH_AGENT=1 ;;
    --with-networkpolicy) WITH_NETPOL=1 ;;
    *) die 2 usage bad_flag "未知参数 $a" ;;
  esac
done

kubectl get ns kagent >/dev/null 2>&1 || die 2 preflight missing_ns "kagent 命名空间不存在；先 make up"
kubectl -n kagent get deploy kagent-controller >/dev/null 2>&1 \
  || die 2 preflight missing_kagent "kagent-controller 不存在；先 make up"

apply() { # apply <file>...
  local f
  for f in "$@"; do
    info apply action=kubectl_apply "file=${f}"
    kubectl apply -f "${AEGIS_ROOT}/${f}" >/dev/null \
      || die 3 apply kubectl_apply_fail "apply ${f} 失败"
  done
}

apply \
  deploy/mcp-servers/k8s-read-rbac.yaml \
  deploy/mcp-servers/scan-rbac.yaml \
  deploy/mcp-servers/logs-rbac.yaml \
  deploy/mcp-servers/events-rbac.yaml \
  deploy/mcp-servers/k8s-read-mcpserver.yaml \
  deploy/mcp-servers/scan-mcpserver.yaml \
  deploy/mcp-servers/prom-mcpserver.yaml \
  deploy/mcp-servers/logs-mcpserver.yaml \
  deploy/mcp-servers/events-mcpserver.yaml

# MCPServer 由 kmcp controller 落实到 Deployment；等 rollout 才是"真的上线"。
for d in k8s-read scan prom logs events; do
  info wait_ready "component=${d}" action=rollout_wait
  kubectl -n kagent rollout status "deployment/${d}" --timeout=180s >/dev/null 2>&1 \
    || die 3 wait_ready timeout "kagent/${d} 180s 未 Ready；查 kubectl -n kagent describe deploy ${d}"
  info wait_ready "component=${d}" result=ready
done

# 最小权限自检：只读 yes、写/secret no。检查失败说明 RBAC 被别处放宽了。
can() { # can <verb> <resource> <sa> <want>
  local got
  got="$(kubectl auth can-i "$1" "$2" --as="system:serviceaccount:kagent:$3" -A 2>/dev/null | tail -1 || true)"
  [ "$got" = "$4" ] || die 3 rbac_assert "RBAC 断言失败" "kagent/$3 can-i $1 $2: want=$4 got=${got:-error}"
}
can list pods      k8s-read yes
can get  secrets   k8s-read no
can get  pods/log  k8s-read yes
can list pods      scan     yes
can get  secrets   scan     no
can create pods    scan     no
can list pods      logs     yes
can get  pods/log  logs     yes
can get  secrets   logs     no
can list events    events   yes
can get  secrets   events   no
info rbac_assert result=ok

if [ "$WITH_NETPOL" -eq 1 ]; then
  apply deploy/security/kagent-mcp-networkpolicy.yaml
  info netpol result=applied "hint=本 kind 实测 default→scan 被阻断、kagent→scan 放行；换 CNI 后用 scripts/security-smoke.sh 复测"
fi

if [ "$WITH_AGENT" -eq 1 ]; then
  apply deploy/agent/modelconfig.yaml deploy/agent/agent.yaml
  info apply "component=aegis-diagnoser" "hint=LLM 端点未就绪时 Agent 会处于不可用；用 kubectl -n kagent get agents 观察"
fi

info done result=ok
