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

- [x] 三个 server 各至少一个工具被 kagent `tools/list` 发现；元数据强校验通过（prom 2 / logs 1 / events 1；security-smoke 验证）。
- [x] Prometheus query/range 对 kind 内 Prometheus 实调返回结构化样本；query 27 series、range 33 series/6 points；上限/参数错误显式。
- [x] logs/events 聚合在 kind 内可用：2 Pod 日志归并 8 行；aegis-t13 事件时间线 11 条；空集合返回空数组。
- [x] 单测覆盖参数边界、上游错误、结果上限；`make lint` 逐 module 0 issues；`make test -race` 全绿。

## Evidence

- `internal/adapters/prom`：只读 `/api/v1/query`、`/api/v1/query_range`；httptest 覆盖 vector/matrix/上游错误/参数校验。
- `internal/adapters/k8s/observability.go`：ListPods/PodLogs/ListEvents；日志行首时间戳解析、字节上限、事件对齐格式。
- `mcp-servers/prom`（prom_query/prom_range）、`mcp-servers/logs`（aggregate_logs）、`mcp-servers/events`（get_event_timeline）：共享 mcpserver 中间件（I4/sanitize/审计/预算/超时）。
- 部署：Dockerfile + `deploy/mcp-servers/{prom,logs,events}-mcpserver.yaml` + RBAC + NetworkPolicy + `AEGIS_*_IMAGE`；`mcp-up.sh` 全量 apply；`security-smoke.sh` 校验 5 个 server 工具面。
- Agent 白名单扩到 13 个（scan + 8 k8s-read + prom 2 + logs + events）；systemMessage 同步。
- 2026-10-19 实机：Prometheus `up` instant 27 series；range 33 series/6 points；logs 2 Pod/8 行；events 11 条；5 个 MCPServer 全部 Ready。
- 残余：Agent 当前无 key（`ai-down` 后 CreateContainerConfigError），需用户用新 key `ai-up --with-agent --restart` 才能跑 LLM 端到端。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
