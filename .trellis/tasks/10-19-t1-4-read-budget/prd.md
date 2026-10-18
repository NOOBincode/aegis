# T1.4 读路径双预算

## Goal

internal/session 落地读路径双预算（工具调用数/token 数 + apiserver QPS），mcpserver 中间件接入；耗尽返回 rate_limited 并保留当前结论。验收：超预算会话被暂停；并发调用按会话聚合计数不可绕过；单测覆盖。

## Requirements

1. `internal/session` domain：会话预算（工具调用数/token 数上限）+ `internal/breaker` QPS 窗口；纯单测。
2. `internal/mcpserver` 中间件：每次调用按 session 聚合计数，超限返回 `ErrClassRateLimited` 协议错误；并发调用不可绕过。
3. `internal/session` 与 mcpserver 的接缝用接口注入，不在框架内硬编码 Redis。
4. 预算配置来源（GuardrailPolicy / env）只做最小闭环，M2 再收敛进 gatekeeper。

## Acceptance Criteria

- [ ] 单测：工具调用数/token/QPS 三类超限均返回 rate_limited；并发聚合计数不绕过。
- [ ] 集成：构造超预算会话，调用被暂停且返回当前预算口径。
- [ ] `make lint` 逐 module 0 issues；根 module 测试通过。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
