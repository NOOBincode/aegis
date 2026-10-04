---
name: aegis-ci
description: aegis 仓库的 CI 与协作规范（GitHub Actions、Makefile、分支/提交/PR 门禁、版本钉死）。Use when creating or modifying GitHub Actions workflows, Makefile targets, PR templates, commit/branch conventions, merge gates, version pinning (deploy/versions.md), or when preparing/merging a PR in the aegis repository.
---

# aegis CI 与协作规范

本仓库是安全敏感项目（AI 操作 K8s 的闸门），CI 是门禁而非形式。事实源：`.github/workflows/`、`Makefile`、`deploy/versions.md`、`.golangci.yml`。

## Workflow 清单与演进

| Workflow | 触发 | 内容 | 门禁起点 |
| --- | --- | --- | --- |
| `ci.yml` | push / PR | lint（golangci-lint）→ unit test（`-race` + goleak）→ build（全部 cmd 与 mcp-servers） | 始终必过 |
| `kind-smoke.yml` | PR | kind 起集群 → apply `crds/` → controller 状态机冒烟 →（M2 后）三连演示脚本 | M2 起必过 |
| `eval-regression.yml` | prompt / 模型 / 工具链变更 | `eval/` 全量场景回归，指标回退即阻断（不变量 I8） | M3 起必过 |

新增 workflow 前先回答：它守的是哪条不变量 / 失败模式（设计文档 §2/§6）？答不上来就不加。

## Workflow 编写规则

- 每个 shell 步骤显式 `shell: bash`；`runs-on` 固定 `ubuntu-latest`——CI 是唯一裁决环境，Windows / WSL2 只是开发入口。
- `permissions:` 最小化（默认 `contents: read`，按需逐项加）。
- 同一 PR 配 `concurrency` 分组 + `cancel-in-progress: true`。
- 第三方 action 钉 major tag 并在同行注释写确切版本；官方 actions（checkout / setup-go）可跟 major。
- Go 版本用 setup-go 的 `go-version-file` 从 `go.work` / `go.mod` 读；Python 及其余组件版本以 `deploy/versions.md` 为单一事实源，改版本 = 单独 PR。
- 禁止在 CI 访问任何真实集群 / 生产凭据；kind 是唯一集群。
- LLM 调用（eval 回归）必须带月度预算告警与单次会话预算上限（设计文档 §9 LLM 成本行）。

## 分支 / 提交 / PR

- `main` 受保护：禁止直推，全部走 PR + squash merge。
- 分支命名：`feat/<scope>-<topic>`、`fix/...`、`docs/...`。
- 提交信息：`<type>(<scope>): <summary>`，type ∈ feat / fix / docs / refactor / test / chore。
- PR 模板必填两部分，缺一不合并：
  1. **不变量检查单**：设计文档 §2 的 I1–I10 逐项对照，标 N/A 或说明；
  2. **AI 产出声明**：AI 生成占比 + 人审人。L2 安全包要求 100% 逐行人审（见 skill `aegis-security-review`）。

## 跨平台红线（CI 侧）

- workflow / Makefile / `scripts/*.sh` 只允许 bash；禁止 PowerShell、BAT 进入关键路径。
- kind / helm / kubectl 的安装步骤在 CI 与 WSL2 开发环境共用同一份脚本，不写第二份。
