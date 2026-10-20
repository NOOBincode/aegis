#!/usr/bin/env bash
# scripts/m1-demo.sh —— M1 OOMKilled 验收演示（T1.10）：
#   注入 OOM + 敏感日志样本 → aegis-cli scan（离线）→ Agent（可选）→
#   采集 scan/k8s-read MCP 审计日志 → aegis-cli replay → 落 DecisionRecord。
#
# 安全：不接收/回显 API key；如当前没有 kagent/aegis-llm Secret，且设置了
# AEGIS_LLM_API_KEY，则调用 scripts/ai-up.sh 临时创建，演示结束自动删除。
# 外部 AI 出域/脱敏残差见 docs/data-egress.md。
#
# 用法：
#   AEGIS_DEMO_WITH_AGENT=0 bash scripts/m1-demo.sh          # 只演示 scan（无 LLM）
#   AEGIS_LLM_API_KEY=... AEGIS_DEMO_WITH_AGENT=1 bash scripts/m1-demo.sh
#   AEGIS_DEMO_KEEP=1 AEGIS_DEMO_WITH_AGENT=1 ...           # 保留 namespace/记录
# 退出码：0 通过；1 验收断言失败；2 前置缺失；3 集群操作失败。
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="m1-demo"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

NS="${AEGIS_DEMO_NAMESPACE:-aegis-eval}"
WITH_AGENT="${AEGIS_DEMO_WITH_AGENT:-0}"
KEEP="${AEGIS_DEMO_KEEP:-0}"
ART="${AEGIS_ROOT}/.cache/m1-demo"
mkdir -p "$ART"
chmod 700 "$ART"

PY="$(command -v python3 || true)"
CURL="$(command -v curl || true)"
[ -n "$PY" ] || die 2 preflight missing_python "需要 python3 解析 A2A 响应"
[ -n "$CURL" ] || die 2 preflight missing_curl "需要 curl 调 Agent A2A"

CLI="${AEGIS_ROOT}/bin/aegis-cli"
info build component=aegis-cli
( cd "${AEGIS_ROOT}/cmd/aegis-cli" && GOCACHE="${GOCACHE:-${AEGIS_ROOT}/.cache/gocache}" go build -o "$CLI" . ) \
  || die 3 build fail "aegis-cli 构建失败"

CREATED_KEY=0
PF_PID=""
cleanup() {
  if [ -n "$PF_PID" ]; then kill "$PF_PID" 2>/dev/null || true; fi
  if [ "$KEEP" -ne 1 ]; then
    kubectl delete ns "$NS" --ignore-not-found >/dev/null 2>&1 || true
  fi
  if [ "$CREATED_KEY" -eq 1 ]; then
    "${AEGIS_ROOT}/scripts/ai-down.sh" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# ---- 1. 注入场景 ----
kubectl create ns "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl label ns "$NS" aegis.dev/eval-scenario=oom-killed --overwrite >/dev/null
info inject component=oom-killed "namespace=$NS"
kubectl apply -f "${AEGIS_ROOT}/eval/scenarios/oom-killed/manifest.yaml" >/dev/null \
  || die 3 inject apply_fail "OOM 场景 apply 失败"
# 敏感日志样本：dummy key，仅用于验证 redaction；不是真实凭据。
DUMMY="sk-$(date +%s)1234567890abcdef"
kubectl -n "$NS" delete pod inject-log --ignore-not-found >/dev/null 2>&1
kubectl -n "$NS" run inject-log --image=busybox:1.36 --restart=Never --command -- \
  sh -c "echo 'Authorization: Bearer ${DUMMY}'; echo 'password=hunter2hunter2'; echo normal; sleep 900" >/dev/null
sleep 25
kubectl -n "$NS" get pod -l app=oom-demo -o jsonpath='{.items[0].status.containerStatuses[0].lastState.terminated.exitCode}' 2>/dev/null | grep -qx 137 \
  || die 1 inject no_137 "OOM 场景未出现 exit 137（可再等 10s 或检查 manifest）"
info inject component=oom-killed result=ok "exit=137"

# ---- 2. 离线 scan 断言 ----
info scan component=aegis-cli action=scan
SCAN_OUT="${ART}/scan.txt"
"$CLI" scan --namespace "$NS" --format text >"$SCAN_OUT" 2>&1 \
  || die 3 scan cli_fail "aegis-cli scan 失败"
grep -q 'OOM_KILLED' "$SCAN_OUT" \
  || die 1 scan no_oom "scan 未命中 OOM_KILLED（见 $SCAN_OUT）"
info scan result=ok "file=$SCAN_OUT"

if [ "$WITH_AGENT" -ne 1 ]; then
  info summary result=ok "mode=scan-only" "hint=设置 AEGIS_DEMO_WITH_AGENT=1 + key 跑 Agent/replay"
  exit 0
fi

# ---- 3. Agent 通道（key 只经 ai-up 的 Secret；脚本不读写明文）----
if ! kubectl -n kagent get secret aegis-llm >/dev/null 2>&1; then
  if [ -z "${AEGIS_LLM_API_KEY:-}" ]; then
    die 2 preflight missing_key "无 kagent/aegis-llm Secret 且未设置 AEGIS_LLM_API_KEY"
  fi
  "${AEGIS_ROOT}/scripts/ai-up.sh" --with-agent --restart >/dev/null \
    || die 3 agent ai_up_fail "ai-up 失败"
  CREATED_KEY=1
fi
kubectl -n kagent rollout status deploy/aegis-diagnoser --timeout=180s >/dev/null 2>&1 \
  || die 3 agent not_ready "Agent Deployment 未 Ready"

# ---- 4. 调 Agent ----
START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
kubectl -n kagent port-forward svc/aegis-diagnoser 18082:8080 >"${ART}/port-forward.log" 2>&1 &
PF_PID=$!
for _ in $(seq 1 30); do grep -q 'Forwarding from' "${ART}/port-forward.log" 2>/dev/null && break; sleep 0.2; done
PROMPT="请诊断 ${NS} 命名空间的 OOMKilled 根因（先 scan_cluster），并检查该命名空间 inject-log Pod 日志里的敏感信息/注入风险；输出根因、证据、置信度、处置建议。只读，不执行写操作。"
RESP="${ART}/agent-response.json"
"$CURL" -sS -m 240 -X POST http://127.0.0.1:18082/ -H 'Content-Type: application/json' \
  -d "$(printf '{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"%s"}],"messageId":"m1-demo"}}}' "$PROMPT")" \
  >"$RESP" || die 3 agent a2a_fail "A2A 调用失败"
OUTCOME="${ART}/outcome.txt"
"$PY" - "$RESP" "$OUTCOME" <<'PYEOF'
import json, sys
r = json.load(open(sys.argv[1]))
text = ''.join(p.get('text','') for a in r.get('result',{}).get('artifacts',[]) for p in a.get('parts',[]) if p.get('kind')=='text')
if not text:
    st = r.get('result',{}).get('status',{}).get('message',{})
    text = ''.join(p.get('text','') for p in st.get('parts',[]) if p.get('kind')=='text')
if not text:
    print('empty agent response', file=sys.stderr); sys.exit(1)
open(sys.argv[2],'w').write(text)
print(text[:600])
PYEOF
[ -s "$OUTCOME" ] || die 1 agent empty_outcome "Agent 响应为空"
grep -q 'OOM' "$OUTCOME" || die 1 agent no_oom "Agent 输出未命中 OOM"
# 不能把“规则命中但 Agent 直接断言假阳性”当验收通过；允许 Agent 标未决/待复核，
# 但必须有理由地覆盖 137+limit 与重启后 working_set 的语义。
if grep -qE '假阳性|误报|根因不成立' "$OUTCOME"; then
  die 1 agent false_positive "Agent 将 137+limit OOM 直接断言为假阳性；见 $OUTCOME"
fi
info agent result=ok "outcome_file=$OUTCOME"

# ---- 5. 审计日志 → replay ----
AUDIT="${ART}/mcp-audit.jsonl"
kubectl -n kagent logs deploy/scan --since-time="$START" >"$AUDIT" 2>/dev/null || true
kubectl -n kagent logs deploy/k8s-read --since-time="$START" >>"$AUDIT" 2>/dev/null || true
grep -q 'mcp.tool.invoke' "$AUDIT" || die 1 audit no_tool_calls "未采集到 MCP 审计行"
REPLAY="${ART}/replay.txt"
"$CLI" replay --log-file "$AUDIT" --outcome-file "$OUTCOME" >"$REPLAY" 2>&1 \
  || die 1 replay cli_fail "replay 失败"
grep -q 'cluster_snapshot_ref=sha256:' "$REPLAY" || die 1 replay no_snapshot "replay 缺输入快照引用"
grep -q 'redacted=' "$REPLAY" || die 1 replay no_redaction_field "replay 缺脱敏计数"
info replay result=ok "file=$REPLAY"

DR="${ART}/decisionrecord.yaml"
"$CLI" replay --log-file "$AUDIT" --outcome-file "$OUTCOME" --format yaml -o "$DR" \
  || die 3 replay yaml_fail "DecisionRecord 生成失败"
kubectl -n kagent apply -f "$DR" >/dev/null || die 3 replay apply_fail "DecisionRecord apply 失败"
DR_NAME="$(grep -m1 '^  name:' "$DR" | awk '{print $2}')"
[ -n "$DR_NAME" ] || die 1 replay no_record "DecisionRecord 未解析出 name"
kubectl -n kagent get dr "$DR_NAME" >/dev/null 2>&1 || die 1 replay no_record "DecisionRecord 未落库"
info decisionrecord result=ok "name=${DR_NAME}" "file=$DR"

info summary result=ok "mode=agent+replay" "scan=$SCAN_OUT" "replay=$REPLAY" "record=$DR"
