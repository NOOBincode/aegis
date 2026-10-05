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

## Progress (2026-10-05) — 完成

框架核心 + 端到端打通，三判据全过：
- 缺元数据拒绝注册：单测 TestToolMetadataValidate/TestRegistryRegister（每字段缺失/非法用例）。
- 空壳 server 注册成功且被 kagent 发现：k8s-read 容器化（FROM scratch 静态二进制）→ kind load → MCPServer CR（transportType=http + deployment.port=8080 + httpTransport /mcp）→ kagent API 确认列出 get_nodes。
- 中间件链（鉴权/限流/包裹/审计/超时）+ 错误协议 + 结构化返回；集成测试（mcp-go client + StreamableHTTP）证明 tools/list 发现 + tools/call I4 包裹。
- 修复真实 bug：mcp.NewTool 默认 InputSchema 与 RawInputSchema 冲突致 tools/list 序列化失败。
- 登记清单落盘 deploy/mcp-servers/k8s-read-mcpserver.yaml。
