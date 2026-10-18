---
name: aegis-ddd-layout
description: aegis 仓库轻型 DDD 架构规范（目录结构、依赖方向铁律、depguard 强制、新代码放置速查）。Use when creating a package or file, moving code, adding an MCP tool / Rego policy / CRD field / adapter / CLI subcommand, or when unsure where code belongs in the aegis monorepo.
---

# aegis 轻型 DDD 架构

事实源：设计文档 §3.2（v1.4 起）。本 skill 是执行速查 + 可机检规则。

## 目录结构

```text
aegis/
├── cmd/                     # 装配入口：仅 main + flag + 依赖注入，禁止业务逻辑
│   ├── gatekeeper/          # L2 闸门主服务
│   ├── controller/          # CRD controller
│   └── aegis-cli/           # 用户交互 CLI
├── api/v1alpha1/            # CRD Go 类型（K8s 契约层，kubebuilder 惯例）
├── internal/
│   ├── risk/                # ┐ 限界上下文：每个上下文 = domain（纯领域）
│   ├── policy/              # │              + ports（出站接口）
│   ├── approval/            # │              + service（用例编排）
│   ├── rollback/            # │
│   ├── breaker/             # │
│   ├── authority/           # │
│   ├── sanitize/            # │
│   ├── audit/               # │
│   ├── session/             # ┘
│   ├── adapters/            # 适配器：k8s(client-go) / opa / mcp / redis / langfuse
│   ├── controller/          # reconcile 层：CRD ↔ 应用服务（保持薄）
│   └── mcpserver/           # MCP server 共享框架（注册、元数据强校验、限流中间件）
├── mcp-servers/             # L1 工具层进程：k8s-read / k8s-write / prom / logs / events / gitops / runbook
├── policies/                # Rego 策略包（§5.2 opaPolicyBundle 指向此处）
├── agent/                   # L3 kagent 配置层 + prompt/上下文工程
├── eval/                    # L4 eval harness（Python，不依赖 Go 内部包）
├── deploy/                  # helm + kind 环境 + versions.md
├── docs/                    # adr/ + threat-model.md
└── crds/                    # CRD manifests
```

## 依赖方向五条铁律（CI 用 depguard 强制）

1. 单向依赖：`cmd → internal/controller、internal/mcpserver、mcp-servers/* → internal/<context>/service → domain`；`internal/adapters/` 实现各上下文 `ports.go` 声明的接口。
2. 领域纯净：上下文包禁止 import `k8s.io/*`、`sigs.k8s.io/*`、`github.com/open-policy-agent/*`、MCP SDK 与 `internal/adapters/*`。分级表、状态机、归因判定必须能纯单测。
3. reconcile 保持薄：读 CR → 调应用服务 → 写 status；业务规则不进 controller。
4. 跨上下文不直接 import 对方 domain，经 service 协作；只允许共享值对象级小工具。
5. 默认 `internal/`；`pkg/` 只放确有外部复用价值的包，当前为空即不留目录。

## depguard 参考配置（并入 .golangci.yml）

```yaml
linters-settings:
  depguard:
    rules:
      context-purity:
        files:
          - "$all && !$test"
          - "**/internal/{risk,policy,approval,rollback,breaker,authority,sanitize,audit,session}/*.go"
        deny:
          - pkg: k8s.io
            desc: "上下文层禁止依赖 k8s 客户端：经 ports 抽象，由 internal/adapters 实现"
          - pkg: sigs.k8s.io
            desc: "同上（controller-runtime 只允许出现在 internal/controller 与 cmd）"
          - pkg: github.com/open-policy-agent
            desc: "OPA 是适配器实现细节，只允许出现在 internal/adapters/opa"
          - pkg: "github.com/*/aegis/internal/adapters"
            desc: "上下文不得反向依赖适配器"
```

## 新代码放哪（速查）

| 要加的东西 | 位置 |
| --- | --- |
| 新 MCP 工具 | `mcp-servers/<族>/` 加 handler + 元数据标注；业务规则放对应 `internal/<context>` |
| 新风险分级规则 | `internal/risk` 查表（确定性代码，禁止 LLM 判断进表） |
| 新 Rego 策略 | `policies/<bundle>/`；OPA 调用仍在 `internal/adapters/opa` |
| 新 CRD / 字段 | `api/v1alpha1/` 类型 + `crds/` manifest + `internal/controller` 加 reconcile 分支 |
| 新外部系统对接 | `internal/adapters/<name>/`，实现对应上下文的 port |
| CLI 新子命令 | `cmd/aegis-cli/` 只装配；逻辑放 `internal/<context>` |
| eval 场景 | `eval/scenarios/`（Python 世界，不 import Go 包） |

## 反模式（PR 直接打回）

- 上下文包出现 client-go / OPA import；reconcile 里写业务 if-else。
- `cmd/` 出现业务逻辑；上下文之间互相 import domain。
- 为"以后可能复用"提前建 `pkg/`。
- 在 mcp-servers 里藏放行判断——放行只能在 L2 闸门（设计文档 §4.1 职责边界）。
