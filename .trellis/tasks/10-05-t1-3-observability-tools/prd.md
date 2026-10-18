# T1.3 观测数据工具

## Goal

mcp-servers/prom/（query/range 窄接口）+ logs/events 聚合视图。判据：指标查询结构化返回，带 token 截断。

## Requirements

1. `mcp-servers/prom`：`query`（instant）与 `query_range` 两个窄接口，经 Prometheus HTTP API；超时、结果行数/样本数上限、时间范围上限。
2. `mcp-servers/logs`：日志聚合视图（按 ns/label 聚合，不新增大范围全量日志能力）；输出上限与截断标记。
3. `mcp-servers/events`：事件时间线聚合（按对象/时间排序）；只读。
4. 三进程都走 `internal/mcpserver` 注册与中间件（I4 包裹、超时、审计）；元数据五字段齐全；不得出现放行/写判断。
5. 结果结构化返回并带 token 截断标记；错误分类沿用 mcpserver 错误协议。

## Acceptance Criteria

- [ ] 三个 server 各至少一个工具被 kagent `tools/list` 发现；元数据强校验通过。
- [ ] Prometheus query/range 对 kind 内 Prometheus 实调返回结构化样本；超上限返回 schema/too_large 错误而非截断静默。
- [ ] logs/events 聚合在 kind 内可用；无数据时明确返回空，不编造。
- [ ] 单测覆盖参数边界、上游错误、结果上限；`make lint` 逐 module 0 issues。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
