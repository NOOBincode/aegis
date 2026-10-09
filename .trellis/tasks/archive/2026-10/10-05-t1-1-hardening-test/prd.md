# T1.1 白盒+黑盒测试与加固

## Goal

对 mcpserver 框架 + k8s-read 做白盒（单测边界/并发/中间件顺序/错误协议）与黑盒（MCP 协议畸形输入/未知方法/未知工具/超限时）测试，发现问题即修复，再进 T1.2。

## Requirements

- TBD

## Acceptance Criteria

- [ ] TBD

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Progress (2026-10-05) — 完成

白盒 + 黑盒测试加固，覆盖率 84.3% → 91.8%，46 个用例全绿，lint 0 issues。

**白盒**（whitebox_test.go）：元数据边界（risk_hint 各非法值/timeout 边界/blast_radius 各负值）、注册校验顺序、MustRegister panic、并发注册+调用安全、中间件链顺序、sanitize 各类型包裹、错误协议分类、超时上界+handler 忽略 ctx、sprintJSON 回退、nil-result 兜底。

**黑盒**（blackbox_test.go + 集群实机）：未知工具协议层拒绝、慢工具超时、工具错误可重试标记、panic 后服务存活、tools/list schema+annotations 契约、实机 pod 的 valid/unknown-tool(-32602)/bad-method(-32601)。

**发现并处理**：
1. （真 bug，已修）mcp.NewTool 默认 InputSchema 与 RawInputSchema 冲突致 tools/list 序列化失败 —— toMCPTool 清零默认 InputSchema。
2. （记录）未知工具被 mcp-go 协议层拦截（-32602），不经过本框架审计埋点 —— 无害拒绝，M2 若要审计探测需在传输层补留痕。
3. （记录）超时中间件 goroutine 随 handler 返回结束；handler 若不 respect ctx 会迟完成（Go 不可强杀 goroutine 的固有限制），写工具的"超时但已应用"由闸门状态机核对（M2）。
