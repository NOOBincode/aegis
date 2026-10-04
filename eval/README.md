# eval — L4 eval harness 总述（Python）

> 所属层：L4 评估与观测层 ｜ 里程碑：M3（落地方案 T3.1；场景格式先行于 M1 T1.10/R-3）｜ 设计文档出处：§4.6 ｜ 关联不变量：I8 ｜ 关联失败模式：F10、F11

## 1. 设计初衷

- **eval 是护城河**：差异化钉死在 L2 闸门与 L4 eval 层（§1.5 冷静剂 2），"无 eval 的 Agent 迭代=裸奔"（I8 原文）。
- **G1 的验收口径建立在场景库上**：自建故障场景库 ≥20 场景、根因诊断准确率 ≥80%、报告可回放（§1.2 G1）。
- **对抗自我欺骗**：F10（自己出题自己答，指标虚高）用 held-out 场景集 + 真实事故复盘入库对冲；F11（控制器正常行为被误判为故障）用归因正确率与误修复率两个一等指标对冲（§4.6）。
- 与 inferchaos 的关系：以库/流水线方式复用其故障注入能力，**不合仓**（§4.6）——两个资产互相成就，各自叙事独立。

## 2. 职责与任务清单

1. runner：场景加载 → kind 环境准备 → 注入 → Agent 运行 → 评分 → 报告 六阶段流水线（§4）。
2. 场景库治理入口（`eval/scenarios/README.md`）：格式 lint、20 场景补齐（T3.2）、held-out 轮换（T3.7/F10）。
3. 评分委托（`eval/graders/README.md`）：四大一等指标——诊断准确率/归因正确率/误修复率/MTTR，口径文档化（T3.3）。
4. 结果存储与历史对比：run 归档、基线管理、任意两 run 的 per-scenario diff（§4）。
5. 回归门禁对接（`eval/regression/README.md`，T3.4）与 inferchaos 复用对接（T3.6，≥5 场景由其生成）。
6. 成本治理：回归用中小模型 + 会话预算上限（设计文档 §9 LLM 成本行）。

## 3. 技术选型与开源包

- **语言**：Python（设计文档 §3.2：eval/ 独立模块，不依赖 Go 内部包；落地方案附录 A 同文）。Python 版本以 `deploy/versions.md` 钉死为准。
- **故障注入**：Chaos Mesh（场景 manifest 载体，随 `deploy/kind/` 环境 T0.2 部署）。自有项目 inferchaos 以库/流水线方式复用，不合仓；其接口稳定版本在 T3.6 前确认（落地方案 §9 外部资产清单）。
- **集群环境**：kind 是唯一集群（aegis-ci 红线：CI 禁访问真实集群）。演示负载沿用设计文档 §5.1 示例命名（shop/payments）。
- **模型通道**：OpenAI 兼容抽象，回归默认中小模型（§9），具体模型以 `deploy/versions.md` 钉死为准。
- **放弃项**：自建故障注入框架（重复 Chaos Mesh/inferchaos 现成能力）；eval 并入 Go 主仓依赖图（破坏落地方案附录 A 的边界约定）。

## 4. 具体设计（不写代码）

**runner 六阶段**（每阶段的输入/输出语义）：

1. **场景加载**：读场景目录，格式校验（Chaos Mesh manifest 存在、期望根因标注、期望处置级别齐全）；格式错误在加载期拒绝，CI lint 同款校验。
2. **kind 环境准备**：执行 `deploy/kind/` 共用脚本（kind + Chaos Mesh + 演示负载，与 CI/WSL2 同一份脚本）；环境探针不绿则当次 run 记 infrastructure_failure。
3. **注入**：apply 场景 manifest；等待故障生效断言（探针/指标条件）通过；记录 t0（注入完成时刻，MTTR 测量起点，口径见 `eval/graders/README.md` §4）。
4. **Agent 运行**：以固定会话参数（prompt 版本、模型通道、预算）驱动 L3 Agent（`agent/README.md`）；参数写入 run 元数据；全程采集 DecisionRecord 与 OTel trace。
5. **评分**：调用 graders 产出四指标；期望处置核对（应 R1/R2/拒绝）比对 Agent 实际动作与 ChangeRequest 结局（§5.1 状态机终态）。
6. **报告**：机器可读 JSON（schema 见 `eval/graders/README.md` §4）+ 人读 markdown；与基线对比产出 delta，供 `eval/regression/README.md` 阻断规则消费。

**结果存储与历史对比**：run 按 run_id 归档——config（prompt/模型/场景集版本哈希）、result.json（四指标 + per-scenario 明细）、report.md、DecisionRecord 集合引用。基线 = 指定 run 的升格（仅全指标不降的 run 可升格，规则见 `eval/regression/README.md` §4）；历史对比支持任意两 run 的 per-scenario diff，指标回退定位到场景。

**与 inferchaos 的复用方式**：库引用，不合仓（§4.6）。eval harness 把故障注入原语作为依赖调用；场景 manifest 标注生成来源，inferchaos 生成 ≥5 个场景是 T3.6 完成判据。inferchaos 保持独立产品定位与发布节奏，双向不阻塞。

**格式先行纪律**：M1 的 T1.10 即按 M3 沿用格式产出 1 号场景（OOMKilled）；R-3 要求 M1/M2 期间先把场景格式定死，避免 M3 返工。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| 环境准备失败（kind/Chaos Mesh/负载不绿） | 阶段探针 | 重试一次；仍失败记 infrastructure_failure，不计入指标分母；连续失败阻断当次 run | 与 Agent 失败严格区分：环境故障不算 Agent 得分，也不给免死——单独标记人工介入 |
| 注入失败/故障未生效 | 故障生效断言 | 场景记 error，不计入分母 | 注入没成 = 场景没跑；若误当"Agent 零动作"统计，误修复率与归因正确率双双失真（F11 口径污染） |
| Agent 会话超时/中断 | 会话超时配置 + trace 完整性检查 | 场景按"未达期望处置"判 fail；证据缺失标 missing_evidence | 对指标诚实：超时不是中性事件 |
| LLM 预算耗尽（§9） | 预算计数（会话级 + 月度级） | 当次 run 中止；已跑场景结果保留但不发报告 | fail-closed 于发布：半份报告不得进基线（F10 防线之一） |
| 评分器异常 | graders 错误面 | 当次 run 失败，不产报告、不触基线更新 | 防脏数据进基线 |
| 门禁对接异常（CI 侧） | 见 `eval/regression/README.md` §5 | 同该文档 | 与该文档保持一致的 fail-closed 口径 |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

**结构化日志字段**：`eval.run_id`、`eval.scenario_id`、`eval.phase`（load/setup/inject/run/grade/report）、`eval.phase_duration_ms`、`eval.infra_error_class`、`eval.budget_consumed_tokens`、`eval.t0_epoch`、`eval.t1_epoch`（MTTR 端点，Committed 时刻）。

**指标**（run 内自采集；自托管环境可接 Prometheus，CI 内以日志/artifact 承载）：`eval_run_total{status="pass|fail|infra_failure"}`；`eval_scenario_duration_seconds{scenario_id=,category=}`；`eval_metric_value{metric="diag_accuracy|attribution|misrepair|mttr"}`。门禁判定的机器可读载体是 result.json 而非 metrics——CI 无长期 Prometheus（aegis-ci 规范口径）。

**Trace**：span `eval.run`（run 根）→ `eval.scenario.<id>` 子 span 链，inject/run/grade 分段埋点；与 Agent 侧 `agent.session.run`、闸门侧 span 以 trace context 串联——一次 eval 场景可从报告一跳进入完整决策链回放（G4 可回放的验收形态）。

**审计留痕点**：每 run 的 config 哈希、场景集版本（含 held-out 标记）、原始 Agent 输出与 DecisionRecord 一并归档，人工可逐场景复核（grader 判定可重算）；对外宣称指标必须标注集来源（F10 原文：定期轮换；对外宣称时标注集来源）。

## 7. 依赖方向与模块边界

- **谁调我**：CI 回归门禁（`eval/regression/README.md`）；本地 Makefile 目标（aegis-ci 约定）；开发者手动触发（调参、M3 验收①演示）。
- **我调谁**：L3 Agent 会话接口（`agent/README.md`，经 OpenAI 兼容通道配置）；`deploy/kind/` 环境脚本（与 CI/WSL2 共用同一份，aegis-ci 跨平台规则 4）；Chaos Mesh API；inferchaos 库（T3.6 起）；graders（本仓子目录）。
- **禁止依赖谁**：禁止 import 任何 Go 内部包（落地方案附录 A 明文红线）；禁止访问 kind 以外集群（aegis-ci 红线，本地同口径）；禁止修改被测对象之外的集群状态（每场景独立命名空间，run 结束销毁重建）；grader 引入 LLM 评判类依赖前须评审（成本+可重复性+注入面，见 `eval/graders/README.md` §3）。
- **与五铁律的关系**：eval/ 是 Go 依赖图外的 Python 世界；"单向依赖"体现为证据单向流动——eval 消费 DecisionRecord 与报告，Go 侧永不 import eval（落地方案附录 A）。

## 8. 测试策略与红队用例

- **runner 自测**：黄金场景（1 号 OOMKilled，T1.10）端到端稳定通过；mock graders 验证六阶段编排与断点续跑语义；故障演练（杀 kind、断 LLM 端点）验证 §5 错误分类落位。
- **场景格式 lint 进 CI**：加载期校验前置，脏场景在 PR 阶段拦截。
- **红队用例**：
  1. **F10 防线**：held-out 轮换演练（M3 检查点 #3）——用训练集同模板变体检训练集指标的虚高成分。
  2. **F11 防线**：干扰类场景零误修复专项（M3 验收④：HPA 正常伸缩与故障并发时 Agent 零误修复）。
  3. **指标口径攻防**：构造"讨好 grader 关键词但根因错误"的 Agent 输出，验证 graders 语义层兜底（与 `eval/graders/README.md` §8 联动）。
  4. **半份报告演练**：模拟 run 中途预算耗尽，验证结果不进基线、不产生报告（§5）。
