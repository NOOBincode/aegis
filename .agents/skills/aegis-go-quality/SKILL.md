---
name: aegis-go-quality
description: aegis 仓库 Go 代码质量管理规范（lint、测试、错误处理、依赖纪律、L2 安全包人审、跨平台 Go 规则）。Use when writing, reviewing, or refactoring Go code in the aegis repo, adding dependencies, writing tests, or touching .golangci.yml.
---

# aegis Go 代码质量

## Lint

- `.golangci.yml` 是唯一事实源；本地 `make lint` 与 CI 跑同一配置。
- 启用基线：govet、staticcheck、errcheck、gosec、revive、gocritic、goimports、misspell、unparam、depguard（架构依赖规则见 skill `aegis-ddd-layout`）。
- `nolint` 必须同行注释理由；L2 安全包（下方清单）对 gosec、errcheck 不设 nolint 例外。

## L2 安全包纪律（落地方案 R-2）

以下目录中的 AI 生成代码必须**逐行人审**，PR 描述标注"AI 产出占比 + 人审人"：

`internal/risk` `internal/policy` `internal/approval` `internal/rollback` `internal/breaker` `internal/authority` `internal/sanitize` `internal/audit` `internal/session` `internal/controller`

## 测试

- 表驱动为默认形态；领域层（各上下文 domain 文件）纯单测，不起任何集群、不依赖 adapter。
- controller 用 envtest；fake client 仅限单测；集成测试打 kind（`make test-integration`）。
- 并发代码配 `go.uber.org/goleak`；`go test -race` 默认开启。
- 覆盖率务实线：L2 安全包 ≥80%，其余 ≥60%；不达标在 PR 里说明原因，不刷数字。

## 错误处理与并发

- `%w` 包装，判定用 `errors.Is/As`；禁止吞错（`_ =`、空分支）；错误信息小写、不带结尾标点。
- `context.Context` 作第一个参数贯穿全链路。
- gatekeeper 对 apiserver / OPA 的每次调用必须有超时，且**超时即拒绝写操作**（fail-closed，I10）——超时降级为放行是一票否决的 bug。

## 依赖纪律

- 新依赖：PR 描述写理由 + license（仅 MIT / Apache-2.0 / BSD）；能用标准库解决不加依赖。
- 控制面（`cmd/` `internal/` `mcp-servers/`）禁止引入 Go 以外语言的运行时；`eval/` 是唯一的 Python 世界。
- client-go / controller-runtime / OPA 等核心依赖版本与 `deploy/versions.md` 对账一致。

## 跨平台 Go 规则

- 路径一律 `path/filepath`；禁止硬编码 `/` 或 `\` 分隔符；文件内容写行尾不假设 LF/CRLF。
- 禁止 CGO；禁止平台特定 syscall——确有需要的场合先开 ADR 再写。
