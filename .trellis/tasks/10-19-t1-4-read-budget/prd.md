# T1.4 读路径双预算

## Goal

internal/session 落地读路径双预算（工具调用数/token 数 + apiserver QPS），mcpserver 中间件接入；耗尽返回 rate_limited 并保留当前结论。验收：超预算会话被暂停；并发调用按会话聚合计数不可绕过；单测覆盖。

## Requirements

1. `internal/session` domain：会话预算（工具调用数/token 数上限）+ `internal/breaker` QPS 窗口；纯单测。
2. `internal/mcpserver` 中间件：每次调用按 session 聚合计数，超限返回 `ErrClassRateLimited` 协议错误；并发调用不可绕过。
3. `internal/session` 与 mcpserver 的接缝用接口注入，不在框架内硬编码 Redis。
4. 预算配置来源（GuardrailPolicy / env）只做最小闭环，M2 再收敛进 gatekeeper。

## Acceptance Criteria

- [x] 单测：工具调用数/token/QPS 三类超限均返回 rate_limited；并发聚合计数不绕过。
- [x] 集成：`TestBlackBoxSessionBudget` 经真实 MCP HTTP 会话，第二次调用返回 rate_limited；token 超限保留当前结果并在下一次 Before 拦截。
- [x] `make lint` 逐 module 0 issues；根 module 测试通过（-race 全绿）。

## Evidence

- `internal/session`：`Manager` 并发安全聚合 tool calls/tokens；`LimitsFromEnv` 默认 50 calls/200k tokens/10 QPS，`AEGIS_SESSION_BUDGET_DISABLED=1` 可关。
- `internal/breaker.QPSWindow`：滑动窗口纯函数（时间注入，无 sleep）；会话 `Before` 调用 QPS。
- `internal/mcpserver`：`BudgetLimiter` 端口 + `rateLimitMiddleware`；超限协议错误 `rate_limited`；token 超限保留当前结果、`Meta.RemainingBudget` 置零、下一次调用拦截；审计日志含 `session_id`。
- `mcp-servers/scan` / `k8s-read` 启动期装配 `session.Manager + breaker.QPSWindow`。
- 2026-10-19：`TestBlackBoxSessionBudget` 通过；`make test -race` 全绿；lint 0 issues。
- 残余：QPS/预算是**进程内**的，多副本共享预算需 M2 Redis 端口；GuardrailPolicy 下发 M2 接。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
