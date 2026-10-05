# T1.2 只读工具集（8 个）

## Goal

mcp-servers/k8s-read/：pods/deployments/nodes/events/logs/metrics/describe 族；强制分页与上限（≤500 行日志/≤200 对象）。判据：每工具单测+集成测试（kind 实集群）；超限返回 429+分页引导。

## Requirements

- TBD

## Acceptance Criteria

- [ ] TBD

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
