#!/usr/bin/env bash
# scripts/ai-down.sh —— 删除 Agent 的 LLM key（Secret），保留 ModelConfig/Agent。
# 用于轮换/止损：删除后 Agent 立即不可用，但不会误删审计记录或工具 RBAC。
# 用法：ai-down.sh
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="ai-down"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

NS="${AEGIS_LLM_NAMESPACE:-kagent}"
SECRET="${AEGIS_LLM_SECRET:-aegis-llm}"
kubectl -n "$NS" delete secret "$SECRET" --ignore-not-found >/dev/null
info teardown "component=llm-key" result=deleted "secret=${NS}/${SECRET}" "hint=Agent 立即不可用；恢复用 scripts/ai-up.sh"
