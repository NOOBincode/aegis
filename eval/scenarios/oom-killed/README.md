# 场景 01：OOMKilled（M1 第 1 号）

> 所属：L4 eval 场景库（T1.10 起点，格式供 M3 沿用）｜ 依赖：kind + scan server/`aegis-cli scan`
> 不变量：I1（判定不经 LLM）、I4（集群返回数据包裹）、I11（Tier-0 规则先行）

## 1. 场景语义

容器以 **awk 作为 PID 1**，在 `memory.limit=128Mi` 下分配大数组，触发容器级 cgroup OOM。
本 kind（containerd + cgroup v2）上内核 dmesg 明确记录 `Memory cgroup out of memory`，但
kubelet 的 `lastState.terminated.reason` 仍为 **Error**（不是 OOMKilled）；因此这是“真实 OOM、
K8s reason 不可靠”的场景，而不是“非 OOM”证据。Tier-0 对 137+limit 只能给 **Warning/Medium/
next_tier=true**，必须由 Agent 用 dmesg/上次实例日志/指标复核，不能仅凭当前 working_set
（重启后样本）断言假阳性。

## 2. 执行

```bash
# 注入（幂等）
kubectl apply -f eval/scenarios/oom-killed/manifest.yaml
kubectl -n aegis-eval get pods -w
# 本地 CLI（KUBECONFIG 指向 kind）
aegis-cli scan --namespace aegis-eval --format text
# 或经 kagent scan MCP（scan server 已部署时）
#   scan_cluster {namespace: "aegis-eval"}
```

## 3. 期望

- `aegis-cli scan` / `scan_cluster` 输出 `OOM_KILLED`，对象为 `Pod/aegis-eval/oom-demo-*`；
- 本 kind 上置信度 **Medium/Warning**、`next_tier=true`（reason=Error 不能结案）；
- 报告不含写动作建议，只有只读处置建议（提高 limit / 排查泄漏 / 改 HPA/VPA，I9）；
- Agent 应标记“疑似 OOM/待复核”或根因命中 OOM 家族，**不得**仅凭 reason=Error + 重启后低
  working_set 断言“假阳性/根因不成立”；
- M1 完整验收还要求 Agent 报告 + `aegis-cli replay`；LLM 通道就绪后跑 `scripts/m1-demo.sh`。

## 4. 清理

```bash
kubectl delete namespace aegis-eval --ignore-not-found
```

## 5. 断言（当前可机检部分）

`expected.json` 固定 `rule_ids_any=["OOM_KILLED"]`、namespace/kind 与“建议不触发写”。
M3 起由 `eval/graders` 消费；当前用 `aegis-cli scan`/`scan_cluster` 人工/脚本比对。

## 6. 实机验证（2026-10-18，kind）

- 2026-10-20 复测：Pod `aegis-eval/oom-demo-*` 被 cgroup OOM killer 杀死，`exitCode=137`、
  `reason=Error`；节点 dmesg 记录 `Memory cgroup out of memory: Killed process ... (awk)`
  与 `memory.oom.group set`，证明是真实 OOM，不是脚本自杀。
- scan 命中 `OOM_KILLED`，**Medium/Warning/next_tier=true**，证据显式写
  “kubelet reason=Error（非 OOMKilled）；137 仅证明 SIGKILL，需 L3 复核”。
- 2026-10-20 LLM 复测曾把该场景误判为“假阳性”，原因是 Agent 只看 reason=Error 与重启后
  working_set 240KiB；这是错误归因。Agent systemMessage 已加约束，`m1-demo.sh` 已拒绝
  “假阳性/误报/根因不成立”作为验收通过。
- 尚未完成：干净 `reason=OOMKilled` 的注入在 kind/containerd v2 上不可得；M1 验收以
  “疑似 OOM + dmesg 证据 + Agent 不反向断言”口径执行。
