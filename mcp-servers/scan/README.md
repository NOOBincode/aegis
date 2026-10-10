# scan — Tier-0 确定性规则巡检 MCP server（scan_cluster）

> 所属层：L1 工具层（只读族） ｜ 里程碑：M1（落地方案 T1.12） ｜ 设计文档出处：§4.2.2 scanner、§2 I11 ｜ 关联不变量：I1/I2/I4/I11 ｜ 上游内核：`internal/scan`

## 1. 定位

把 `internal/scan` 的 Tier-0 规则巡检暴露为 MCP 只读工具 `scan_cluster`，打通 kagent Agent→规则层入口。Agent 诊断前调 `scan_cluster`，拿到**规则已结案（`NextTier=false`）**的结论与**残差（`NextTier=true`）**——结案部分不进 LLM、省 token 省审核；残差才上送 Tier-1 归因 / Tier-2 LLM。

## 2. 工具

| 工具 | 入参 | 出参 | 风险 |
| --- | --- | --- | --- |
| `scan_cluster` | `namespace?`（空=全集群）、`format?`=json\|sarif\|text、`since?`（事件时间窗秒） | `{schema, generated_at, summary{total_findings,critical,warning,info,scanned_pods,resolved_by_rules}, findings[], partial_reads[]}`（format=text 附 `report_text`） | R0 只读 |

## 3. 架构与纪律

- 进程装配入口 `main.go`（import `internal/adapters/k8s`）；handler `tools.go` 纯净，只依赖 `Scanner` 接口（*scan.Service 满足，测试注入 fake）。
- 元数据五字段强校验（RiskR0/idempotent/reversible=false/blast=0/timeout=30000），经 `internal/mcpserver` 统一中间件链（sanitize 包裹 I4、限流、超时、审计）。
- 只读 R0，不触达写；M1 无闸门只读直连（M2 收敛经闸门）。
- B 层规则来源：`AEGIS_SCAN_POLICIES` 目录（热修改），空则嵌入默认规则（`internal/policyload`）。

## 4. 运行

```sh
# 集群内（R0 只读 SA）或本地（KUBECONFIG）
go run ./mcp-servers/scan            # 监听 :8081（Streamable HTTP）
```

## 5. 测试

```sh
go test ./mcp-servers/scan/...                          # 单测+白盒+黑盒
go test -tags integration ./mcp-servers/scan/...        # 集成（需 kind 集群）+ 冒烟
```
