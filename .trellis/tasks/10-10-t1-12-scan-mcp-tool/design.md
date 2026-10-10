# T1.12 scan_cluster MCP 工具 — 技术设计

## 1. 架构与模块

```
kagent Agent ──MCP(tools/call scan_cluster)──> mcp-servers/scan（独立进程）
                                                  │
        main.go（装配入口）                        │  internal/mcpserver 框架
          newScanService()                        │  （注册/元数据强校验/中间件链：
          ├─ clientset（in-cluster|KUBECONFIG）    │   sanitize 包裹/限流/超时/审计）
          ├─ adapters/k8s.NewScanSnapshotProvider  ▼
          ├─ policyload.FilePolicySource      tools.go：scan_cluster Tool
          └─ scan.NewService                     handler(ctx,args)→Scanner.Scan→Result
                                                  │
                                          依赖 Scanner 接口（解耦，可注入 fake）
```

**关键解耦**：handler 只依赖 `Scanner` 接口（`Scan(ctx, scan.CollectOptions) (scan.Result, error)`，`*scan.Service` 天然满足），不碰 client-go/adapter——测试注入 fake，生产由 main.go 装配注入真实 service。工具 handler 无业务判断（§4.1 职责边界），判定全在 internal/scan。

**装配边界**：`main.go` 作为进程装配入口 import `internal/adapters/k8s`（M1 过渡，无闸门只读直连；M2 收敛经闸门）。`tools.go`（handler/schema）保持纯净。

## 2. 支撑重构：internal/policyload

`FilePolicySource` 现困于 `cmd/aegis-cli`（package main 不可复用）。提取到 `internal/policyload`：
- `FilePolicySource{Dir}`：目录热加载 + fail-fast 解码（sigs.k8s.io/yaml 在装配边缘，非 domain）。
- `DefaultPolicySource()`：解码 `scan.DefaultReliabilityYAML`（embed 默认，气隙开箱可用）。
- 实现 `scan.PolicySource` 接口。
- depguard：`internal/policyload` 不在 ctx-no-infra-deps 清单（允许 sigs yaml）；方向 policyload→scan（合法）。

aegis-cli 改用 `policyload.FilePolicySource`，删除 `cmd/aegis-cli/policysource.go`，其测试迁至 `internal/policyload`。

## 3. scan_cluster 工具契约

- **Name**: `scan_cluster`
- **InputSchema**: `{namespace?: string, format?: enum[json,sarif,text]=json, since?: int 秒}`（无必填）
- **Metadata**: `risk_hint=R0, idempotent=true, reversible=false, est_blast_radius=0, timeout_ms=30000`（采集+判定，给足上界）
- **Handler**: 解析 args → `Scanner.Scan(ctx, {Namespace, SinceSeconds})` → 渲染/结构化 → `Result{Data: {findings, summary, partial_reads, report?}}`
- **错误协议**：Scanner 返回错误 → `NewToolError(ErrClassDownstream, retryable=true)`；参数 format 非法 → `ErrClassSchema` 协议错误。

## 4. 四层测试矩阵

| 层 | 文件 | 用例 |
| --- | --- | --- |
| 单测 | `tools_test.go` | 参数解析（ns/format/since 边界、非法 format）、结果结构字段、Scanner 错误→工具错误 |
| 白盒 | `tools_test.go` | 注册元数据五字段、InputSchema 契约、scanner nil 防御 |
| 黑盒 | `blackbox_test.go` | tools/list 含 scan_cluster（schema/annotations）、tools/call 正常+未知工具拒绝+超时+panic 隔离（复用 mcpserver HTTPHandler+httptest+StreamableHttpClient 模式） |
| 集成 | `integration_test.go`（build tag `integration`） | kind 实集群注入 OOMKilled → scan_cluster 命中 OOM_KILLED |
| 冒烟 | `integration_test.go` | server 启动 + tools/list 发现 scan_cluster |

集成测试用 build tag `//go:build integration` 隔离（需真实集群），默认 `go test` 不跑；`make test-integration` 触发。

## 5. 失效与边界

- 只读 R0，不触达写。
- Scanner 错误向上抛（不误报空结果）；超时由框架中间件兜底（timeout_ms=30000）。
- 采集盲点经 partial_reads 透出（不静默当空）。
