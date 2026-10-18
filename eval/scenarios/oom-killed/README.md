# 场景 01：OOMKilled（M1 第 1 号）

> 所属：L4 eval 场景库（T1.10 起点，格式供 M3 沿用）｜ 依赖：kind + scan server/`aegis-cli scan`
> 不变量：I1（判定不经 LLM）、I4（集群返回数据包裹）、I11（Tier-0 规则先行）

## 1. 场景语义

容器设 `memory: 32Mi` 限制，循环写 `/dev/shm`（tmpfs 页计入 cgroup memory），触发容器级
OOMKilled。kubelet 可能在 `state.terminated.reason` 或 `lastState.terminated.reason` 标注
`OOMKilled`；若只暴露裸退出码 137，scan 的 137 推断链仍应命中（Medium 置信度）。

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
- 规则结案 `next_tier=false`（Tier-0 不经 LLM 即定根因）；
- 报告不含写动作建议，只有只读处置建议（提高 limit / 排查泄漏 / 改 HPA/VPA，I9）。
- M1 完整验收还要求 Agent 根因报告命中标注 + `aegis-cli replay`；LLM 通道就绪后补跑。

## 4. 清理

```bash
kubectl delete namespace aegis-eval --ignore-not-found
```

## 5. 断言（当前可机检部分）

`expected.json` 固定 `rule_ids_any=["OOM_KILLED"]`、namespace/kind 与“建议不触发写”。
M3 起由 `eval/graders` 消费；当前用 `aegis-cli scan`/`scan_cluster` 人工/脚本比对。

## 6. 实机验证（2026-10-18，kind）

- Pod `aegis-eval/oom-demo-*` 被 cgroup OOM killer 杀死，`exitCode=137`、`reason=Error`
  （kubelet 未标 OOMKilled），正好覆盖 scan 的 137 推断链。
- 经 kagent 部署的 scan MCP `scan_cluster{namespace:"aegis-eval"}` 命中
  `OOM_KILLED/Pod/aegis-eval/oom-demo-*`，置信度 Medium，证据含“容器级 limit 佐证”，
  `wrapped=true`、`partial_reads=null`，规则层结案（next_tier=false）。
- 尚未完成：Agent 根因报告命中标注（需 LLM 通道）、`aegis-cli replay`（T1.8）。
