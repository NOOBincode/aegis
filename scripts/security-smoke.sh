#!/usr/bin/env bash
# scripts/security-smoke.sh —— M1 安全冒烟/渗透基线（只读，不修改集群）。
# 覆盖：
#   1. 工具 SA 最小权限（can-i 矩阵）
#   2. kagent-tools cluster-admin 暴露必须不存在
#   3. MCP 入站 NetworkPolicy 存在
#   4. 两个 MCP server 的工具清单不含写动词；Agent 白名单只含只读工具
# 退出码：0 全过；1 有失败；2 前置缺失。
# 用法：security-smoke.sh
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="security-smoke"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

FAIL=0
ok()   { info "$1" "${@:2}"; }
bad()  { fail "$1" "${@:2}"; FAIL=1; }

kubectl get ns kagent >/dev/null 2>&1 || die 2 preflight missing_ns "kagent namespace 不存在"

# ---- 1. can-i 矩阵 ----
can() { # can <verb> <resource> <sa> <want>
  local got
  got="$(kubectl auth can-i "$1" "$2" --as="system:serviceaccount:kagent:$3" -A 2>/dev/null | tail -1 || true)"
  if [ "$got" = "$4" ]; then ok "rbac" "sa=$3" "verb=$1" "resource=$2" "want=$4"; else
    bad "rbac" "sa=$3" "verb=$1" "resource=$2" "want=$4" "got=${got:-error}"
  fi
}
can list pods     k8s-read yes
can get  pods/log k8s-read yes
can get  secrets  k8s-read no
can get  secrets  scan     no
can list pods     scan     yes
can create pods   scan     no
can delete pods   scan     no

# ---- 2. kagent-tools 暴露面 ----
if kubectl get clusterrolebinding kagent-tools-cluster-admin-rolebinding >/dev/null 2>&1; then
  bad "kagent_tools" "reason=clusterrolebinding_still_exists"
else
  ok "kagent_tools" "clusterrolebinding=absent"
fi
if kubectl -n kagent get deploy kagent-tools >/dev/null 2>&1; then
  bad "kagent_tools" "reason=deployment_still_exists"
else
  ok "kagent_tools" "deployment=absent"
fi

# ---- 3. NetworkPolicy ----
if kubectl -n kagent get networkpolicy aegis-mcp-ingress-kagent-only >/dev/null 2>&1; then
  ok "netpol" "name=aegis-mcp-ingress-kagent-only"
else
  bad "netpol" "reason=missing"
fi

# ---- 4. MCP 工具清单 / Agent 白名单 ----
TMP="$(mktemp -d)"
PF_PIDS=()
cleanup() {
  for p in "${PF_PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$TMP"
}
trap cleanup EXIT

start_pf() { # start_pf <deploy> <localport> <remoteport>
  kubectl -n kagent port-forward "deploy/$1" "$2:$3" >"$TMP/pf-$1.log" 2>&1 &
  PF_PIDS+=("$!")
  for _ in $(seq 1 30); do
    grep -q 'Forwarding from' "$TMP/pf-$1.log" 2>/dev/null && return 0
    sleep 0.2
  done
  die 2 preflight port_forward_fail "kagent/$1 端口转发未就绪"
}

mcp_tools() { # mcp_tools <localport> <outfile>
  local port="$1" out="$2" h="$TMP/h" sid
  : >"$h"
  curl -sS -m 10 -D "$h" -o /dev/null -X POST "http://127.0.0.1:${port}/mcp" \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"security-smoke","version":"1"}}}'
  sid="$(grep -i '^Mcp-Session-Id:' "$h" | tr -d '\r' | cut -d' ' -f2)"
  [ -n "$sid" ] || die 2 mcp session_missing "未取得 MCP session id"
  curl -sS -m 10 -o /dev/null -X POST "http://127.0.0.1:${port}/mcp" \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $sid" -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  curl -sS -m 15 -X POST "http://127.0.0.1:${port}/mcp" \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $sid" -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' >"$out"
}

start_pf k8s-read 18080 8080
start_pf scan 18081 8081
start_pf prom 18083 8082
start_pf logs 18084 8083
start_pf events 18085 8084
mcp_tools 18080 "$TMP/k8s-read.tools"
mcp_tools 18081 "$TMP/scan.tools"
mcp_tools 18083 "$TMP/prom.tools"
mcp_tools 18084 "$TMP/logs.tools"
mcp_tools 18085 "$TMP/events.tools"

# 工具名按 read 白名单核对；出现写/执行词立即失败。
k8s_read_expected="describe_deployment describe_pod get_deployments get_events get_nodes get_pod_logs get_pods get_resource_metrics"
got_names="$(grep -o '"name":"[^"]*"' "$TMP/k8s-read.tools" | cut -d'"' -f4 | sort -u | tr '\n' ' ' | sed 's/ $//')"
if [ "$got_names" = "$k8s_read_expected" ]; then
  ok "tools" "server=k8s-read" "count=8"
else
  bad "tools" "server=k8s-read" "got=${got_names:-none}" "want=${k8s_read_expected}"
fi
if grep -Eq '"name":"(scale_|restart_|delete_|patch_|create_|apply_|exec_|shell)' "$TMP/k8s-read.tools"; then
  bad "tools" "server=k8s-read" "reason=write_tool_present"
else
  ok "tools" "server=k8s-read" "write_tools=absent"
fi

scan_expected="scan_cluster"
scan_got="$(grep -o '"name":"[^"]*"' "$TMP/scan.tools" | cut -d'"' -f4 | sort -u | tr '\n' ' ' | sed 's/ $//')"
if [ "$scan_got" = "$scan_expected" ]; then
  ok "tools" "server=scan" "count=1"
else
  bad "tools" "server=scan" "got=${scan_got:-none}" "want=${scan_expected}"
fi

check_tools() { # check_tools <server> <file> <expected...>
  local server="$1" file="$2"; shift 2
  local want got
  want="$(printf '%s\n' "$@" | sort -u | tr '\n' ' ' | sed 's/ $//')"
  got="$(grep -o '"name":"[^"]*"' "$file" | cut -d'"' -f4 | sort -u | tr '\n' ' ' | sed 's/ $//')"
  if [ "$got" = "$want" ]; then ok "tools" "server=$server" "count=$(echo "$want" | wc -w)"; else
    bad "tools" "server=$server" "got=${got:-none}" "want=${want}"
  fi
  if grep -Eq '"name":"(scale_|restart_|delete_|patch_|create_|apply_|exec_|shell)' "$file"; then
    bad "tools" "server=$server" "reason=write_tool_present"
  fi
}
check_tools prom "$TMP/prom.tools" prom_query prom_range
check_tools logs "$TMP/logs.tools" aggregate_logs
check_tools events "$TMP/events.tools" get_event_timeline

# Agent 白名单只允许 scan_cluster + 只读 k8s-read 工具。
agent_tools="$(kubectl -n kagent get agent aegis-diagnoser -o jsonpath='{range .spec.declarative.tools[*]}{.mcpServer.toolNames[*]}{"\n"}{end}' 2>/dev/null | tr '\n' ' ')"
for name in $agent_tools; do
  case "$name" in
    scan_cluster|get_pods|get_deployments|get_nodes|get_events|get_pod_logs|get_resource_metrics|describe_pod|describe_deployment|prom_query|prom_range|aggregate_logs|get_event_timeline) : ;;
    *) bad "agent_tools" "reason=unexpected_tool" "tool=${name}" ;;
  esac
done
ok "agent_tools" "checked=$(echo "$agent_tools" | wc -w)"

if [ "$FAIL" -ne 0 ]; then
  log error summary fail
  exit 1
fi
log info summary pass
