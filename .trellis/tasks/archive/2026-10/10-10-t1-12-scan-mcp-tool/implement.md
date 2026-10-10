# T1.12 scan_cluster MCP 工具 — 执行计划

## 步骤（有序）

- [ ] 1. 提取 `internal/policyload`：`FilePolicySource` + `DefaultPolicySource` + `decodePolicyFile`（自 cmd/aegis-cli/policysource.go），实现 `scan.PolicySource`。
  - 验证：`go build ./internal/policyload/...`
- [ ] 2. aegis-cli 改用 policyload：scan.go 换 import、删 policysource.go、测试迁至 internal/policyload。
  - 验证：`go test ./cmd/aegis-cli/... ./internal/policyload/...`
- [ ] 3. 建 `mcp-servers/scan`：`go.mod`、`tools.go`（Scanner 接口 + scanCluster Tool + handler）、`main.go`（装配 + serve）、`README.md`；go.work 注册。
  - 验证：`go build ./mcp-servers/scan/...`
- [ ] 4. 单测+白盒（`tools_test.go`）：参数解析、结果结构、错误协议、元数据契约、fake Scanner 注入。
  - 验证：`go test ./mcp-servers/scan/...`
- [ ] 5. 黑盒（`blackbox_test.go`）：MCP 协议级 tools/list + tools/call + 未知工具 + 超时 + panic 隔离。
  - 验证：`go test ./mcp-servers/scan/... -run BlackBox`
- [ ] 6. 集成+冒烟（`integration_test.go`，build tag integration）：kind 注入 OOMKilled → scan_cluster 命中。
  - 验证：`go test -tags integration ./mcp-servers/scan/...`（需 kind）
- [ ] 7. 全量检查：`make build && make lint && go test ./...` 全绿。
- [ ] 8. trellis-check 独立检查 → commit。

## 回滚点

- policyload 提取：aegis-cli 改回内联（git 恢复）。
- scan server：删 `mcp-servers/scan` + go.work 移除该行。

## 执行进度（2026-10-10）

**已完成**：步骤 1–7。
- 1 policyload 提取（含 DefaultRules 缓存、路径穿越防御 G304）；2 aegis-cli 改用 policyload（删旧 policysource）；3 mcp-servers/scan（tools.go Scanner 解耦 + main.go 装配 + go.work 注册）；4 单测+白盒（参数解析/结果结构/错误协议/元数据/schema）；5 黑盒（list 契约/正常调用/未知工具/非法 format/panic 隔离）；6 集成（kind 注入 OOMKilled → scan_cluster 经 MCP 命中 OOM_KILLED）+ 冒烟（启动+发现）；7 全量检查（build/lint 0 issues、测试全绿）。
- 覆盖率：scan server 52.3%（main 装配路径由集成覆盖）、policyload 86.5%。
- 中间件链实测生效：集成测试日志可见 `mcp.tool.invoke risk_hint=R0` 埋点。

**待**：步骤 8 trellis-check 独立检查 → commit。
