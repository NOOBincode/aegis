#!/usr/bin/env bash
# scripts/ai-up.sh —— 安全配置/轮换 Agent 的 LLM API key 与 ModelConfig。
#
# 安全设计：
#   - key 从不作为命令行参数（避免 ps/日志/历史泄漏），只经环境变量或 read -s 输入；
#   - 只在进程内存在，写入 0600 临时文件后由 kubectl --from-file 建 Secret，立即删除；
#   - 不 echo key、不写 git、不写普通日志；Secret 名固定 aegis-llm（namespace kagent）；
#   - 外部 AI 即数据出域：调用前经 internal/sanitize 脱敏，但提供商侧日志/留存是残差，
#     私域/气隙环境请把 AEGIS_LLM_BASE_URL 指向本地 vLLM/Ollama。
#
# 用法：
#   AEGIS_LLM_API_KEY=... bash scripts/ai-up.sh [--with-agent] [--restart]
#   bash scripts/ai-up.sh --with-agent          # 交互式输入（read -s）
# 可选环境变量：AEGIS_LLM_BASE_URL / AEGIS_LLM_MODEL / AEGIS_LLM_SECRET /
#               AEGIS_LLM_NAMESPACE / AEGIS_AGENT_NAME
# 退出码：0 成功；2 前置/参数错误；3 集群 apply/rollout 失败。
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="ai-up"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

WITH_AGENT=0
RESTART=0
for a in "$@"; do
  case "$a" in
    --with-agent) WITH_AGENT=1 ;;
    --restart) RESTART=1 ;;
    *) die 2 usage bad_flag "未知参数 $a（支持 --with-agent/--restart）" ;;
  esac
done

NS="${AEGIS_LLM_NAMESPACE:-kagent}"
SECRET="${AEGIS_LLM_SECRET:-aegis-llm}"
BASE_URL="${AEGIS_LLM_BASE_URL:-https://api.deepseek.com}"
MODEL="${AEGIS_LLM_MODEL:-deepseek-flash}"
AGENT_NAME="${AEGIS_AGENT_NAME:-aegis-diagnoser}"

[[ "$NS" =~ ^[a-z0-9-]+$ ]] || die 2 usage bad_namespace "namespace 非法：$NS"
[[ "$SECRET" =~ ^[a-z0-9-]+$ ]] || die 2 usage bad_secret "secret 名非法：$SECRET"
[[ "$BASE_URL" =~ ^https?://[A-Za-z0-9._:/-]+$ ]] || die 2 usage bad_base_url "base URL 非法：$BASE_URL"
[[ "$MODEL" =~ ^[A-Za-z0-9._:/-]+$ ]] || die 2 usage bad_model "model 非法：$MODEL"

kubectl get ns "$NS" >/dev/null 2>&1 || die 2 preflight missing_ns "namespace $NS 不存在；先 make up"

KEY="${AEGIS_LLM_API_KEY:-}"
if [ -z "$KEY" ]; then
  if [ -t 0 ]; then
    read -rsp "LLM API key（输入不回显）: " KEY
    echo
  else
    die 2 usage missing_key "AEGIS_LLM_API_KEY 未设置，且当前不是交互式终端"
  fi
fi
# 只允许常见 key 字符，拒绝换行/空白；长度下限仅防手误，不猜测具体供应商格式。
[[ "$KEY" =~ ^[A-Za-z0-9._~+/=-]{16,}$ ]] \
  || die 2 usage bad_key "API key 格式非法（长度/字符），拒绝继续；不会回显 key"

TMP="$(mktemp "${TMPDIR:-/tmp}/aegis-llm-key.XXXXXX")"
chmod 600 "$TMP"
cleanup() { rm -f "$TMP"; }
trap cleanup EXIT
printf '%s' "$KEY" >"$TMP"
unset KEY

info configure "component=llm" action=secret_apply "secret=${NS}/${SECRET}"
kubectl -n "$NS" create secret generic "$SECRET" \
  --from-file=KEY="$TMP" --dry-run=client -o yaml \
  | kubectl apply -f - >/dev/null \
  || die 3 configure secret_apply_fail "创建/更新 Secret 失败"

# ModelConfig 只引用 Secret，不包含 key 值。
cat <<EOF | kubectl apply -f - >/dev/null || die 3 configure modelconfig_apply_fail "ModelConfig apply 失败"
apiVersion: kagent.dev/v1alpha2
kind: ModelConfig
metadata:
  name: aegis-model
  namespace: ${NS}
  labels: {aegis.dev/component: agent}
spec:
  provider: OpenAI
  model: ${MODEL}
  openAI:
    baseUrl: ${BASE_URL}
  apiKeySecret: ${SECRET}
  apiKeySecretKey: KEY
EOF
info configure "component=modelconfig" result=applied "provider=OpenAI" "model=${MODEL}" "base_url=${BASE_URL}"

if [ "$WITH_AGENT" -eq 1 ]; then
  kubectl apply -f "${AEGIS_ROOT}/deploy/agent/agent.yaml" >/dev/null \
    || die 3 configure agent_apply_fail "Agent apply 失败"
  info configure "component=agent" result=applied "name=${AGENT_NAME}"
fi

if [ "$RESTART" -eq 1 ]; then
  if kubectl -n "$NS" get deploy kagent-controller >/dev/null 2>&1; then
    kubectl -n "$NS" rollout restart deploy/kagent-controller >/dev/null
    info configure "component=kagent-controller" action=rollout_restart
  fi
  if kubectl -n "$NS" get deploy "$AGENT_NAME" >/dev/null 2>&1; then
    kubectl -n "$NS" rollout restart "deploy/${AGENT_NAME}" >/dev/null
    info configure "component=${AGENT_NAME}" action=rollout_restart
  fi
fi

# 只做存在性检查，不读 Secret 值、不打印 key。
kubectl -n "$NS" get secret "$SECRET" >/dev/null 2>&1 \
  || die 3 configure secret_missing "Secret 未创建"
kubectl -n "$NS" get modelconfig aegis-model >/dev/null 2>&1 \
  || die 3 configure modelconfig_missing "ModelConfig 未创建"

info done result=ok "hint=key 已写入 ${NS}/${SECRET}；仓库/日志无 key；外部 AI 出域风险见 docs/data-egress.md"
