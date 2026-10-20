#!/usr/bin/env bash
# scripts/redteam-netpol.sh —— M2 T2.12 网络层红队：验证 5 个自研 MCP server 的
# NetworkPolicy 只允许 kagent 命名空间入站，default 命名空间直连必须被阻断。
# 只读探测（MCP initialize），不调用任何工具；结束后清理临时 namespace。
# 前置：kind + 5 个 MCPServer 已部署；curlimages/curl 镜像在 kind 节点可用。
# 退出码：0 通过；1 断言失败；2 前置缺失；3 集群操作失败。
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="redteam-netpol"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

NS_PROBE="${AEGIS_REDTEAM_NS:-aegis-redteam}"
IMAGE="${AEGIS_CURL_IMAGE:-curlimages/curl:8.10.1}"
NODE="${AEGIS_REDTEAM_NODE:-aegis-worker}"

kubectl get ns kagent >/dev/null 2>&1 || die 2 preflight missing_kagent "kagent 不存在；先 make up"
kubectl -n kagent get svc scan k8s-read prom logs events >/dev/null 2>&1 \
  || die 2 preflight missing_servers "5 个 MCP Service 未齐；先 make mcp-up"

cleanup() { kubectl delete ns "$NS_PROBE" --ignore-not-found >/dev/null 2>&1 || true; }
trap cleanup EXIT

kubectl create ns "$NS_PROBE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
cat > /tmp/redteam-probe.yaml <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: probe
  namespace: ${NS_PROBE}
spec:
  nodeName: ${NODE}
  restartPolicy: Never
  containers:
    - name: curl
      image: ${IMAGE}
      imagePullPolicy: IfNotPresent
      command:
        - sh
        - -c
        - |
          for svc in "scan 8081" "k8s-read 8080" "prom 8082" "logs 8083" "events 8084"; do
            set -- \$svc
            name=\$1; port=\$2
            code=\$(curl -sS -m 3 -o /dev/null -w '%{http_code}' -X POST "http://\$name.kagent.svc:\$port/mcp" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"redteam","version":"1"}}}' 2>/dev/null || echo 000)
            echo "\$name=\$code"
          done
YAML
# default 命名空间探针：应全部被 NetworkPolicy 阻断。
sed "s/namespace: ${NS_PROBE}/namespace: default/" /tmp/redteam-probe.yaml > /tmp/redteam-default.yaml
kubectl apply -f /tmp/redteam-default.yaml >/dev/null || die 3 probe default_apply_fail
# kagent 命名空间探针：应全部放行。
sed "s/namespace: ${NS_PROBE}/namespace: kagent/" /tmp/redteam-probe.yaml > /tmp/redteam-kagent.yaml
kubectl apply -f /tmp/redteam-kagent.yaml >/dev/null || die 3 probe kagent_apply_fail
rm -f /tmp/redteam-probe.yaml /tmp/redteam-default.yaml /tmp/redteam-kagent.yaml

for ns in default kagent; do
  kubectl -n "$ns" wait --for=condition=Ready pod/probe --timeout=90s >/dev/null 2>&1 || true
done
sleep 3
DEFAULT_OUT="$(kubectl -n default logs probe 2>/dev/null || true)"
KAGENT_OUT="$(kubectl -n kagent logs probe 2>/dev/null || true)"

fail=0
for svc in scan k8s-read prom logs events; do
  d="$(printf '%s\n' "$DEFAULT_OUT" | awk -F= -v s="$svc" '$1==s{print $2}' | tail -1)"
  k="$(printf '%s\n' "$KAGENT_OUT" | awk -F= -v s="$svc" '$1==s{print $2}' | tail -1)"
  if [ "$d" = "200" ]; then fail "netpol" "server=$svc" "default=200" "reason=not_blocked"; fail=1; else info netpol "server=$svc" "default=${d:-000}" "result=blocked"; fi
  if [ "$k" != "200" ]; then fail "netpol" "server=$svc" "kagent=${k:-none}" "reason=not_allowed"; fail=1; else info netpol "server=$svc" "kagent=200" "result=allowed"; fi
done

kubectl -n default delete pod probe --ignore-not-found >/dev/null 2>&1 || true
kubectl -n kagent delete pod probe --ignore-not-found >/dev/null 2>&1 || true
if [ "$fail" -ne 0 ]; then log error summary fail; exit 1; fi
log info summary pass
