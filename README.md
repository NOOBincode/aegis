# aegis：AI 原生 K8s 安全操作与调度平台

一个架在 Kubernetes 之上的 **AI 操作安全层**：让 LLM Agent 能诊断、能修复、能参与调度决策，但每一次写操作都必须经过确定性的风险分级、策略校验、审批与可回滚封装——AI 的变更和人类变更过同一套门禁，且比人类变更多一层熔断。

> 当前状态：**骨架期**。仓库内只有设计文档、落地方案、目录设计说明（各目录 `README.md`）与项目级 skill，尚无实现代码。实现排期见落地方案。

## 文档地图

| 文档 | 作用 |
| --- | --- |
| [技术方案设计文档](AI原生K8s安全操作平台-技术方案设计文档.md)（v1.4） | 决策层事实源：定位、不变量 I1–I10、架构、数据契约、失败模式 F1–F15、ADR |
| [落地方案](AI原生K8s安全操作平台-落地方案-v1.0.md)（v1.1） | 执行层：任务拆分 T 编号、周级排期、里程碑验收口径 |
| `.agents/skills/` | 项目级规范 skill：`aegis-ci` / `aegis-go-quality` / `aegis-ddd-layout` / `aegis-security-review` |
| 各目录 `README.md` | 该目录的设计初衷、任务清单、开源包选型、具体设计、错误处理与可观测性 |

## 目录结构（轻型 DDD，设计文档 §3.2）

```text
aegis/
├── cmd/                     # 装配入口（gatekeeper / controller / aegis-cli），禁止业务逻辑
├── api/v1alpha1/            # CRD Go 类型（K8s 契约层）
├── internal/                # 限界上下文（domain + ports + service）+ adapters + controller + mcpserver
├── mcp-servers/             # L1 工具层进程（k8s-read / k8s-write / prom / logs / events / gitops / runbook）
├── policies/                # Rego 策略包
├── agent/                   # L3 Agent 运行时（kagent 配置层 + prompt/上下文工程）
├── eval/                    # L4 eval harness（Python）
├── deploy/                  # kind 环境 + 观测基座 + versions.md 版本钉死表
├── docs/                    # ADR + 威胁模型
├── crds/                    # CRD manifests
└── scripts/                 # 自动化脚本（bash only）
```

依赖方向五铁律与 depguard 机检规则见 `.agents/skills/aegis-ddd-layout/SKILL.md`。

## 工程纪律速览

- 合并门禁：PR 模板双必填（I1–I10 检查单 + AI 产出人审声明），详见 `.github/workflows/README.md` 与 skill `aegis-ci`。
- L2 安全包 AI 产出逐行人审（落地方案 R-2）。
- 跨平台：自动化一律 bash；主开发环境 WSL2 Ubuntu；CI（ubuntu-latest）为唯一裁决环境（落地方案附录 B）。

## 快速开始

待 Phase 0（落地方案 W1–W2）完成后提供 `make up` 一键环境。第一周行动清单见落地方案 §11。
