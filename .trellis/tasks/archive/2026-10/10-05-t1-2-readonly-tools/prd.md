# T1.2 只读工具集（8 个）

## Goal

mcp-servers/k8s-read/：pods/deployments/nodes/events/logs/metrics/describe 族；强制分页与上限（≤500 行日志/≤200 对象）。判据：每工具单测+集成测试（kind 实集群）；超限返回 429+分页引导。

## Requirements

- 8 个只读工具：get_pods / get_deployments / get_nodes / get_events / get_pod_logs / get_resource_metrics / describe_pod / describe_deployment。
- 强制分页与上限（≤200 对象 / ≤500 行日志），超限返回错误（上层转 429）+ next_cursor 分页引导。
- 结果经框架中间件 I4 包裹（`<untrusted_cluster_data>`）。
- 元数据五字段强校验（RiskR0）。

## Acceptance Criteria

- [x] 8 工具全部注册成功（元数据强校验通过）。
- [x] 单测：happy path（I4 包裹 + 结构化）、必填参数缺失 schema 错误、limit>200 超限、tail_lines>500 上限。
- [x] kind 集成测试 + 冒烟（tools/list 发现 8 工具；对实集群调 get_nodes/get_pods/get_deployments/get_events 返回结构化数据）。
- [x] `make lint` 0 issues。

## 修复记录（2026-10-11 收尾）

接手在制品时发现并修复 5 个 bug：
1. `requireStr` 返回 `*mcpserver.Error` 与 reader 的 `error` 在 `:=` 复用时类型冲突（build blocker）→ 改名 `perr`。
2. `fmt` unused import。
3. `argInt64`/`argLimit` 不处理 `int`（tail_lines=9999 被静默吞成 0，上限失效，真缺陷）→ 兼容 int/int64。
4. `describe_deployment` 对无 Selector/Replicas 的 Deployment nil pointer panic → nil-safe。
5. fake metrics client 测试数据：metrics API 的 REST resource 是 `pods`/`nodes`（非 `podmetricses`），且 tracker List 需 PrependReactor 供数。

## Notes

- 见 implement.jsonl / check.jsonl。
