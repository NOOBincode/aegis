# T1.1 MCP 工具框架与注册机制

## Goal

mcp-servers/ 骨架 + ToolServer 注册流程 + 工具元数据标注 schema 强制校验（risk_hint/idempotent/reversible/est_blast_radius/timeout_ms 缺一注册失败）。判据：空壳 server 注册成功且被 kagent 发现；缺元数据被拒绝。

## Requirements

- TBD

## Acceptance Criteria

- [ ] TBD

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
