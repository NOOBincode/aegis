# T1.11 确定性规则层（scanner / Tier-0）— 减少 AI 审核压力

> 编号说明：落地方案 §4 M1 清单中 `T1.1`=工具框架、`T1.4`=读路径双预算（均已占用），规则层（设计文档 §4.2.2 `scanner`，I11 Tier-0）在落地方案 M1 中尚无对应项，属**缺口**。本任务顺延编号为 **T1.11**，并需在落地方案补记该缺口；任务 slug `t1-4` 仅为目录名，不影响语义编号。

## Goal

为 M1 补齐**确定性规则巡检内核**（设计文档 §4.2.2 `scanner`，I11 Tier-0）：对集群状态用确定性规则做高频故障模式的根因判定，离线、零 LLM、零 token；能规则结案的就不惊动 LLM/人审，作为 AI 与人审的**前置过滤器**——这是"减少 AI 审核压力"的落点（对应 I11：规则先行、LLM 兜底；每前置一层，token 成本与隐私暴露面同步下降）。

## Requirements

**范围（本任务做什么）**
- 新建 `internal/scan` 独立包（根 module `github.com/aegis-dev/aegis`，与 `risk`/`policy`/`authority`/`sanitize` 等闸门域包并列）。
- 规则层为**确定性、离线、零 LLM、零 token、零 API key**（I11/I2）：断网/气隙环境照常产出价值。
- 引擎与取数解耦：`internal/scan` 为纯领域逻辑，**不直接依赖 client-go**；经注入的"快照提供者"接口获取集群状态，规则判定为纯函数（输入快照 → 输出 findings），可表驱动单测。
- 规则库覆盖高频故障模式：OOMKilled / CrashLoopBackOff / Pending 不可调度 / 探针失败 / 调度约束失配 / 镜像拉取失败 等（kubeagent 覆盖集对齐）。
- 输出**结构化 findings**（finding id、severity、受影响对象、根因、证据、置信度），并生成**脱敏报告**（HTML/JSON/SARIF，默认不含集群身份与敏感字段，对齐 §4.2.2 / kubeagent 隐私姿态）。
- 消费形态（M1）：供 `aegis-cli` 与/或只读 MCP 工具调用，作为 Agent 诊断的 Tier-0 前置。

**复用路线（kubeagent 优先，自研降级 — 设计文档 §4.1/§4.2.2/R17；已定案为纯自研，见 `research/reuse-decision.md` 与 `design.md §4`）**
- 调研结论：kubeagent 代码全在 `internal/` 下，**import 硬阻塞**；其最大可复用资产是已被验证的架构与规则形态，仅作设计蓝本。
- **纯自研 `internal/scan`**（决策者裁决 2026-10-08）：不 import kubeagent 代码，也**不依赖其 mcp 件**（含交叉验证基线一并砍掉）——其 internal 封闭设计预示维护风险，且气隙/私域要求零外部运行时依赖。
- **规则库双层**：A 层硬编码 Go Detector（运行时根因判定）+ B 层数据驱动 **YAML 规则 DSL**（结构 lint，**支持热修改**，抗 R17 主载体）。
- **正确性由 golden 场景库 + fixtures + eval 回归门禁保证**（I8），不依赖外部件对照。

**约束 / Non-Goals（本任务不做什么）**
- 不做 Tier-1 归因（authority/attributor，I9）与 Tier-2 LLM 诊断——本任务仅 Tier-0。
- 不做写路径/审批——规则层只读，永不触达写能力。
- 不引入 kubeagent 的 `--fix` 修复、SSAR 写前预检（写路径机制归 M2）。
- 遵守产品不变量：I1（放行只在 L2）、I2（不进热路径）、I4（输出过 sanitizer，隔离不可信数据）、I11（规则先行）。
- 结果若进入 LLM 上下文，须经 `internal/sanitize` 包裹（M1 形态 T1.5）。

**规则腐烂防线（R17）**
- 规则库作为一等公民维护；M3 eval 引入"规则层解决率"指标作腐烂信号（本任务仅需在 findings/报告中暴露足以统计该指标的结构）。

## Acceptance Criteria

- [ ] 新增 `internal/scan` 包（根 module），`make build && make lint` 通过；depguard 不违规（引擎不依赖 client-go）。
- [ ] 断网/无 LLM key 环境（kind 集群）可对 OOMKilled 场景产出**命中标注的根因 finding**（确定性，零 LLM）。
- [ ] 纯自研、**零外部依赖**：不 import kubeagent 代码、不依赖其 mcp 件；正确性由 golden 场景库 + fixtures 保证。
- [ ] 规则库为**双层**：A 层 Detector（OOM/CrashLoop/Pending/探针失败/镜像拉取/重启环/启动错误/配置错误）+ B 层 YAML 结构 lint DSL（**改 YAML 热生效、无需重编译/重启**；坏规则 fail-safe 回退已知良好集并告警）；每规则有命中/不命中两类用例。
- [ ] findings 结构含 finding id/severity/对象/根因/证据/置信度；脱敏报告（≥JSON+SARIF）不含集群身份与敏感字段；采集盲点记入 PartialReads 不静默当空。
- [ ] 引擎纯逻辑表驱动单测覆盖关键判定路径；新增 A 层模式=一文件+注册表一行、新增 B 层规则=一 YAML 条目，均不改引擎。
- [ ] 至少一种消费形态可跑通（`aegis-cli scan` 子命令或 MCP 只读工具），结果按 I4 过 sanitize。
- [ ] 落地方案 §4 补记 T1.11 缺口与编号冲突说明。

## Notes

- 复杂任务：`design.md` + `implement.md` 在 `task.py start` 前补齐。
- kubeagent 复用性调研结论回填 `research/`（trellis-research 进行中），结论将决定"薄封装 vs 自研"，并写入 `design.md`。
- 相关不变量：I1 / I2 / I4 / I11；失败模式：R17（规则腐烂）；ADR 关联：ADR-001（仅读路径，无涉）、ADR-002（无关）。
