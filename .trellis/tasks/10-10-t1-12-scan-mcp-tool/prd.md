# T1.12 scan_cluster MCP 工具（Agent→Tier-0 入口）+ 四层测试

## Goal

把 `internal/scan` 的 Tier-0 规则巡检暴露为 MCP 只读工具 `scan_cluster`（`mcp-servers/scan` 独立进程，经 `internal/mcpserver` 框架注册），打通 kagent Agent→规则层入口——Agent 诊断前可调 `scan_cluster` 拿到 Tier-0 已结案/残差结论，减少 AI 审核压力（I11）。配套**四层测试**（单测/白盒/黑盒/集成）+ 冒烟。

## Requirements

**范围**
- 新建 `mcp-servers/scan` 独立 MCP server 进程（与 k8s-read 等并列），go.work 注册。
- `scan_cluster` 工具：入参 `namespace`（可选，空=全集群）、`format`（json|sarif|text，默认 json）、`since`（事件时间窗秒，可选）；出参结构化 findings + summary + partial_reads。
- 元数据五字段强校验（RiskR0/idempotent/reversible/est_blast_radius/timeout_ms），经框架统一中间件链（sanitize 包裹 I4、限流、超时、审计埋点）。
- 装配与解耦：handler 依赖 `Scanner` 小接口（`Scan(ctx, CollectOptions) (Result, error)`），main.go 装配入口注入真实 service；测试注入 fake。
- **支撑重构**：`FilePolicySource` 从 `cmd/aegis-cli`（package main，不可复用）提取到共享包 `internal/policyload`，供 aegis-cli 与 scan server 共用（DRY）。

**约束 / Non-Goals**
- 只读：scan_cluster 是 R0，不触达写。
- M1 形态：无 L2 闸门，采集经 `internal/adapters/k8s` 只读直连（同 k8s-read M1 过渡）；M2 收敛经闸门。
- 不做 Tier-1 归因 / Tier-2 LLM；不做写工具。

**四层测试**
- 单测：handler 参数解析（namespace/format/since）、结果结构、Scanner 错误→工具错误。
- 白盒：注册元数据五字段、InputSchema 契约、错误协议分类。
- 黑盒：MCP 协议级（tools/list 含 scan_cluster 且 schema/annotations 正确、tools/call 正常+未知工具拒绝+畸形参数、超时、panic 隔离）。
- 集成：kind 实集群，注入 OOMKilled，scan_cluster 返回命中 finding。
- 冒烟：server 启动 + tools/list 发现 scan_cluster。

## Acceptance Criteria

- [ ] `mcp-servers/scan` 可 build；go.work 含该 module；`make lint` 0 issues。
- [ ] `scan_cluster` 注册成功，元数据五字段齐（RiskR0），tools/list 透出 schema + readOnlyHint。
- [ ] 单测/白盒/黑盒全绿（复用 mcpserver 黑盒基建模式）。
- [ ] kind 集成：注入 OOMKilled，scan_cluster 经 MCP 调用返回 OOM finding（断言 rule_id=OOM_KILLED）。
- [ ] `internal/policyload` 提取后 aegis-cli 与 scan server 共用，各自测试全绿。
- [ ] handler 依赖 Scanner 接口，测试注入 fake 无需集群。

## Notes

- 复杂任务：design.md + implement.md 齐备后 start。
- 复用基建：`internal/mcpserver`（注册/中间件/HTTPHandler 黑盒）、`internal/scan`（引擎）、`internal/adapters/k8s`（采集端口）。
- 关联：T1.11 规则层（已归档）、设计文档 §4.1/§4.2.2/I11。
