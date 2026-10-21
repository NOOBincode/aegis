# aegis 当前可执行黑盒校验清单（M2，2026-10-21）

> 目的：让你在不读实现代码的前提下，按“外部行为”验证当前交互与功能。
> 适用环境：当前已存在的 `kind-aegis` 集群；**不要先执行 `make down` / `make up`**（见 §9 已知环境阻塞）。
> 本文件不包含真实密钥；所有 key 用环境/Secret 注入，不写入仓库/日志。

## 0. 当前环境事实（校验前先确认）

```bash
cd /home/user/aegis

kubectl get nodes
kubectl -n kagent get mcpserver
kubectl -n kagent get deploy gatekeeper gatekeeper-approval aegis-controller
kubectl -n kagent get agents
kubectl -n kagent get guardrailpolicy
kubectl -n kagent get secret aegis-llm 2>&1 || true
kubectl -n kagent get cr
```

期望：

- 3 个节点 `Ready`；
- 6 个 MCPServer：`scan/k8s-read/prom/logs/events/gatekeeper` = `True`；
- `gatekeeper`、`gatekeeper-approval`、`aegis-controller` = `1/1`；
- `aegis-diagnoser`/`aegis-operator`：`ACCEPTED=True`、`READY=False`（无 LLM key，预期）；
- `GuardrailPolicy/aegis-default` 存在；
- `secret/aegis-llm` **不存在**（预期；真实 LLM 未启用）；
- 无残留 ChangeRequest。

如果上述不符合，先不要继续做写流程，先记录并停止。

---

## 1. 本地测试与静态门禁（不触集群写）

```bash
make test-unit
make test-integration
make lint
make check-skills
bash scripts/check-secrets.sh --history
```

期望：

| 命令 | 期望 |
|---|---|
| `make test-unit` | 全 module 通过 |
| `make test-integration` | k8s-read / scan 集成通过（需要 kind） |
| `make lint` | 逐 module 0 issues |
| `make check-skills` | pass |
| `check-secrets --history` | clean |

附加（可选，耗时更长）：

```bash
make test        # -race
make bench       # L5 热点
```

---

## 2. 只读冒烟与网络隔离

```bash
make security-smoke
make redteam-netpol
```

期望：

- `security-smoke`：RBAC/kagent-tools/NetworkPolicy/5 个只读 MCP 工具面/Agent 13 工具全 pass；
- `redteam-netpol`：6 个入口（5 只读 + gatekeeper）均为：
  - default namespace → `000`（阻断）
  - kagent namespace → `200`（放行）

> gatekeeper 已部署时，脚本会自动把 `gatekeeper:8086` 加入探测。

---

## 3. M1 无 LLM 演示（替身路径）

```bash
AEGIS_DEMO_WITH_AGENT=0 make m1-demo
```

期望：

- 输出 `mode=scan-only`；
- `scan.txt` 命中 `OOM_KILLED`；
- 脚本退出码 0。

注意：

- 当前 kind/containerd 下 kubelet 只报 `reason=Error`，不是 `OOMKilled`；scan 给的是 **Warning/Medium/next_tier=true**，不是 critical。
- 这是已知 M1 环境例外，不能据此宣称“干净 OOMKilled 验收”。
- 需要真实 Agent 时再设置 `AEGIS_DEMO_WITH_AGENT=1` + key（见 §8）。

---

## 4. Gatekeeper MCP：R1 自动执行（黑盒）

### 4.1 准备测试对象和端口

```bash
kubectl delete ns aegis-blackbox --ignore-not-found
kubectl create ns aegis-blackbox

kubectl -n aegis-blackbox create deployment payments \
  --image=busybox:1.36 --replicas=1 -- sh -c 'sleep 600'

kubectl -n kagent port-forward svc/gatekeeper 18086:8086 >/tmp/aegis-pf-mcp.log 2>&1 &
PF_MCP=$!
for _ in $(seq 1 30); do grep -q Forwarding /tmp/aegis-pf-mcp.log 2>/dev/null && break; sleep 0.2; done
```

### 4.2 MCP 初始化

```bash
H=$(mktemp)
curl -sS -m 10 -D "$H" -o /dev/null -X POST http://127.0.0.1:18086/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"blackbox","version":"1"}}}'

SID=$(tr -d '\r' < "$H" | awk -F': ' 'tolower($1)=="mcp-session-id"{print $2}')
[ -n "$SID" ] || { echo "MCP initialize 失败"; exit 1; }

curl -sS -m 10 -o /dev/null -X POST http://127.0.0.1:18086/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

curl -sS -m 10 -X POST http://127.0.0.1:18086/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
```

期望：工具列表只有 `request_change`。

### 4.3 提交 R1 意图

```bash
curl -sS -m 20 -X POST http://127.0.0.1:18086/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_change","arguments":{"session_ref":"blackbox","idempotency_key":"bb-r1-1","intent":"scale payments to 2","source":"agent","requested_by":"alice","tool":"scale_deployment","verb":"patch","resource":"deployments","namespace":"aegis-blackbox","args":{"namespace":"aegis-blackbox","name":"payments","replicas":2,"from_replicas":1},"dry_run_passed":true,"blast_radius_pods":1}}}'
```

期望响应：

```json
{"allow":true,"created":true,"requires_approval":false,"reject":false}
```

等待执行：

```bash
for _ in $(seq 1 30); do
  n=$(kubectl -n kagent get cr -o jsonpath='{.items[0].status.state}' 2>/dev/null || true)
  r=$(kubectl -n aegis-blackbox get deploy payments -o jsonpath='{.spec.replicas}' 2>/dev/null || true)
  [ "$n" = "Committed" ] && [ "$r" = "2" ] && break
  sleep 1
done
kubectl -n kagent get cr
kubectl -n aegis-blackbox get deploy payments -o jsonpath='{.spec.replicas}{"\n"}'
```

期望：CR `Committed`，Deployment `2`。

---

## 5. Gatekeeper MCP：R2 审批后执行

### 5.1 提交 R2 意图（+2，不是 +1）

```bash
curl -sS -m 20 -X POST http://127.0.0.1:18086/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"request_change","arguments":{"session_ref":"blackbox","idempotency_key":"bb-r2-1","intent":"scale payments to 4","source":"agent","requested_by":"bob","tool":"scale_deployment","verb":"patch","resource":"deployments","namespace":"aegis-blackbox","args":{"namespace":"aegis-blackbox","name":"payments","replicas":4,"from_replicas":2},"dry_run_passed":true,"blast_radius_pods":1}}}'
```

期望：

```json
{"allow":false,"created":true,"requires_approval":true,"reject":false}
```

记录返回的 `cr_name`，确认：

```bash
kubectl -n kagent get cr <cr-name> -o jsonpath='{.status.state}{"\n"}'   # AwaitingApproval
kubectl -n aegis-blackbox get deploy payments -o jsonpath='{.spec.replicas}{"\n"}' # 仍为 2，未执行
```

### 5.2 审批端点

```bash
kubectl -n kagent port-forward svc/gatekeeper-approval 18085:8085 >/tmp/aegis-pf-approval.log 2>&1 &
PF_APPROVAL=$!
for _ in $(seq 1 30); do grep -q Forwarding /tmp/aegis-pf-approval.log 2>/dev/null && break; sleep 0.2; done
```

构建 CLI：

```bash
GOCACHE="$PWD/.cache/gocache" go build -o .cache/aegis-cli ./cmd/aegis-cli
```

注入审批 HMAC（只放在当前 shell 环境，不回显）：

```bash
export AEGIS_APPROVAL_SECRET="$(kubectl -n kagent get secret aegis-gatekeeper -o jsonpath='{.data.secret}' | base64 -d)"
```

以 `alice` 审批 `bob` 发起的 CR：

```bash
.cache/aegis-cli approve \
  --endpoint http://127.0.0.1:18085/v1/approval \
  --cr "kagent/<cr-name>" \
  --actor alice \
  --note "blackbox approval"
```

期望：

```text
state=Approved approver=alice
```

等待执行：

```bash
for _ in $(seq 1 30); do
  n=$(kubectl -n kagent get cr <cr-name> -o jsonpath='{.status.state}' 2>/dev/null || true)
  r=$(kubectl -n aegis-blackbox get deploy payments -o jsonpath='{.spec.replicas}' 2>/dev/null || true)
  [ "$n" = "Committed" ] && [ "$r" = "4" ] && break
  sleep 1
done
kubectl -n kagent get cr <cr-name>
```

期望：CR `Committed`，Deployment `4`，`status.approval=Approved`。

---

## 6. 负向行为（必须 fail-closed）

### 6.1 策略爆炸半径超限

用同一个 MCP session 请求：

```json
{"session_ref":"blackbox","idempotency_key":"bb-policy-1","intent":"too big",
 "source":"agent","requested_by":"alice","tool":"scale_deployment","verb":"patch",
 "resource":"deployments","namespace":"aegis-blackbox",
 "args":{"namespace":"aegis-blackbox","name":"payments","replicas":5,"from_replicas":4},
 "dry_run_passed":true,"blast_radius_pods":11}
```

期望：

```json
{"reject":true,"created":false}
```

且 `kubectl -n kagent get cr` 不新增。

### 6.2 R3 保护命名空间

把 `namespace` 改为 `kube-system`，args 同步；期望 `reject=true`，不落 CR。

### 6.3 HPA 字段所有权

```bash
kubectl -n aegis-blackbox autoscale deployment payments --min=1 --max=5
```

再提交一次 `scale_deployment`（新的 idempotency key）；期望：

```json
{"reject":true,"created":false, "reasons":["字段所有权 hpa/payments：直写必须走 owner API，当前拒绝"]}
```

清理 HPA：

```bash
kubectl -n aegis-blackbox delete hpa payments
```

### 6.4 自批拒绝

发起一个 R2 CR，`requested_by` 为 `alice`；用 `--actor alice` 审批：

期望 CLI 非零退出，服务端返回：

```text
self approval rejected
```

CR 保持 `AwaitingApproval`，不执行。

### 6.5 审批重放/篡改（单测）

```bash
go test ./internal/gatekeeper -run 'TestApprovalServerRejects(Replay|Tampered)'
```

期望通过：第二次重放 401，篡改 401。

---

## 7. mTLS（可选，当前集群未启用）

当前 in-cluster 审批 Service 是 **HTTP**（仅 kagent 内 + port-forward），没有启用证书。代码支持 mTLS：

```bash
make mtls-certs
```

本地启动 gatekeeper 并带：

```bash
--tls-cert .cache/mtls/server.crt \
--tls-key  .cache/mtls/server.key \
--tls-ca   .cache/mtls/ca.crt
```

CLI 用：

```bash
--endpoint https://127.0.0.1:18085/v1/approval \
--tls-ca .cache/mtls/ca.crt \
--tls-cert .cache/mtls/client.crt \
--tls-key .cache/mtls/client.key \
--tls-server-name aegis-gatekeeper
```

这是**本地附加测试**；当前集群内审批不是 mTLS。不要把 `.cache/mtls` 下私钥提交或上传。

---

## 7.1 CLI 交互会话（无子命令）

先构建：

```bash
GOCACHE="$PWD/.cache/gocache" go build -o bin/aegis-cli ./cmd/aegis-cli
```

直接运行（不需要 `chat` 子命令）：

```bash
bin/aegis-cli
```

TTY 下会进入全屏 TUI；管道/非 TTY 自动回退行式 session。`AEGIS_TUI=force` 可强制 TUI（截图/脚本用）。

当前没有 LLM key，因此 Agent 对话会失败；但**本地 scan 在会话里可用**：

```text
aegis> /scan aegis-eval
# 或
aegis> /scan aegis-eval json
aegis> /exit
```

期望：

- 打印 `aegis 交互式安全运维会话`；
- `/scan` 走本地 Tier-0，不调用 LLM；
- 自然语言输入在无 key 时返回“Agent 未连接”，不会误执行写操作；
- `/help`、`/status`、`/agent <url>`、`/namespace <ns>` 可用。

有 key + Agent Ready 后，同一会话里直接输入自然语言即可：

```bash
kubectl -n kagent port-forward svc/aegis-diagnoser 18082:8080
bin/aegis-cli
# aegis> 诊断 aegis-eval 的 OOM
```

写操作仍必须经 gatekeeper `request_change` + 审批；会话本身只读。

> `bin/aegis-cli chat` 仍保留兼容，但默认入口是 `bin/aegis-cli`。

---

## 8. 真实 LLM / Agent（当前不可用）

当前 `kagent/aegis-llm` 不存在，`aegis-diagnoser` 和 `aegis-operator` 都 `Ready=False`。因此：

- 不要跑 `AEGIS_DEMO_WITH_AGENT=1 make m1-demo`；
- 如需跑，必须提供新 key，并且 key 只经环境/Secret：

```bash
AEGIS_LLM_API_KEY='<key>' bash scripts/ai-up.sh --with-agent --restart
AEGIS_DEMO_WITH_AGENT=1 bash scripts/m1-demo.sh
bash scripts/ai-down.sh
```

结束后必须确认：

```bash
kubectl -n kagent get secret aegis-llm 2>&1 || true
bash scripts/check-secrets.sh --history
```

**不要**把 key 贴到命令历史/PR/日志；本清单不提供真实 key。

---

## 9. 已知不可执行/环境阻塞

| 项 | 现状 |
|---|---|
| `make up` | 当前宿主 `fs.inotify.max_user_instances=128`，且本地 kubectl 为 v1.37.1，与钉死 v1.31.0 不一致；`make up` 预检会失败，当前不要执行 |
| `make down` | 会销毁当前 kind 集群；除非你准备重新搭建，不要执行 |
| `make mcp-images` | 会重建并 kind load 镜像，耗时；需先 `set -a; . deploy/versions.env; set +a` |
| `make gatekeeper-up` / `controller-up` | 幂等 apply；可在当前集群重跑 |
| `make mcp-up` | 幂等 apply 5 个只读 server；不含 gatekeeper/controller |
| 真实 GitOps PR | `open_change_pr` 代码/镜像未部署为 MCPServer，且没有 GitHub token；不是当前黑盒入口 |
| harness H1 journal | 只有单测；没有 controller 接线/黑盒入口 |
| HPA owner API 翻译 | 当前只拒绝直写，不自动改 HPA |
| controller-runtime/envtest | 未接 |

---

## 10. 清理

```bash
kubectl -n kagent delete cr --all --ignore-not-found
# 若 finalizer 卡住，逐个：
# kubectl -n kagent patch cr <name> --type=merge -p '{"metadata":{"finalizers":[]}}'

kubectl delete ns aegis-blackbox --ignore-not-found
kill "${PF_MCP:-}" "${PF_APPROVAL:-}" 2>/dev/null || true
unset AEGIS_APPROVAL_SECRET
rm -f /tmp/aegis-pf-mcp.log /tmp/aegis-pf-approval.log
```

---

## 11. 通过标准（当前版本）

| 检查 | 通过标准 |
|---|---|
| 单元/集成/lint | 全绿 |
| security-smoke | pass |
| redteam-netpol | 6 个入口均隔离正确 |
| m1-demo（无 Agent） | scan-only 通过 |
| R1 | allow + CR Committed + replicas 2 |
| R2 | AwaitingApproval → Approved → Committed + replicas 4 |
| 策略超限 | reject + 不落 CR |
| R3 | reject + 不落 CR |
| HPA owner | reject + 不落 CR |
| 自批 | 403 self approval rejected |
| Agent/GitOps/harness | 当前不作为黑盒通过项 |

**最后强调**：本清单是当前真实可交互能力，不代表 M2 全矩阵通过。Agent 真实模型、完整 GitOps、controller-runtime/envtest、harness H2/H3 仍未完成。
