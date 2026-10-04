# T0.1 monorepo 脚手架

## Goal

按设计文档 §3.2（v1.4 轻型 DDD 分层）落地 aegis 工程基座，作为一切后续开发的底座。

## Requirements

- 目录骨架：`cmd/ api/ internal/ mcp-servers/ policies/ agent/ eval/ deploy/ docs/ crds/ scripts/`
- `go.work` 聚合：库代码（api/internal/policies）属根 module；每个 `cmd/*` 与 `mcp-servers/*` 独立 module
- 根 `Makefile`（薄封装，逻辑在 scripts/）
- `.golangci.yml`：含 depguard 架构依赖方向机检（五铁律）
- `.gitattributes`：钉 LF 行尾
- PR 模板：不变量检查单（I1–I10）+ AI 产出声明（R-2/R-4）
- `.agents/skills/` 项目规范四件

## Acceptance Criteria

- [x] `make lint && make build` 在空架子上通过
- [x] depguard 拦下 domain 文件 import client-go 的违例（规则已配，拦载口径见 .golangci.yml）
- [x] PR 模板落盘（I1–I10 检查单 + AI 产出声明）

## Status / Evidence

**已完成（2026-10-04 前 + 本会话）。**

- 骨架代码：16 个 `internal/**/doc.go` + `api/v1alpha1/doc.go` + 3 个 `cmd/*/main.go` + 7 个 `mcp-servers/*/main.go`
- `make lint` = 0 issues；`make build` = 全 module 通过
- 本会话补齐最后一块：`.github/pull_request_template.md`
