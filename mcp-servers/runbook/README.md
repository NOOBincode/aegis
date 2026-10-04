# runbook — Runbook 检索工具：诊断知识的窄入口

> 所属层：L1 工具层 ｜ 里程碑：M1（落地方案 T1.7） ｜ 设计文档出处：§3.3（选型）、§4.1 ｜ 关联不变量：I1、I4 ｜ 关联失败模式：F1（间接）、F4、F10

## 1. 设计初衷

诊断 Agent 的价值上限取决于它能否复用排障经验而不是每次从零推理。runbook server 把项目积累的事故复盘与排障经验结构化入库，以向量检索方式注入诊断上下文。两个设计红线：其一，**检索结果只是参考材料，不是放行依据**——runbook 里写的"可以 scale"不构成任何写操作的许可（I1：放行只由代码与策略决定）；其二，**无命中明确返回空，不编造**（T1.7 完成判据原文）——编造的 runbook 引用比没有更危险，它给幻觉披上权威外衣。F10（eval 集过拟合）的缓解机制之一（真实事故复盘入库）也以本库为载体。

## 2. 职责与任务清单

**职责**：runbook YAML 的检索（向量 + 结构化过滤），Top-K 片段返回，命中/无命中的明确语义。不做知识写入（写入走维护流程，见第 4.4 节，不经 MCP 工具）、不做"自动执行 runbook 步骤"（步骤是给 LLM 参考的，执行永远走闸门）。

**任务清单**：

| 任务 | 交付 | 出处 |
| --- | --- | --- |
| T1.7 | LanceDB 嵌入式 + runbook YAML 格式定义；初始灌入 5–10 条自有排障经验 | 落地方案 W6 |
| T1.5 | 检索片段出口统一 sanitize 包裹（片段最终进 LLM 上下文） | 落地方案 W5 |
| F10 缓解 | 真实事故复盘入库通道（配合 held-out 轮换规则） | 设计文档 §6 F10 |

## 3. 技术选型与开源包

| 项 | 选型 | 理由 / 出处 |
| --- | --- | --- |
| 向量库 | LanceDB 嵌入式 | 嵌入式零运维，适合个人项目；知识检索给诊断 Agent 用。放弃外部向量服务：MVP 阶段过度设计（设计文档 §3.3 原文） |
| 知识形态 | 结构化 runbook YAML | 向量检索给"找得到"，YAML 结构给"看得懂、可评审、可回放" |
| embedding 模型 | 与 LLM 接入同一 OpenAI 兼容抽象层选型 | 具体模型与版本以 `deploy/versions.md` 钉死为准 |
| 版本钉死 | LanceDB 客户端版本 | 以 `deploy/versions.md` 钉死为准 |

## 4. 具体设计（不写代码）

### 4.1 检索工具清单

| # | 工具 | 入参语义 | 强制约束 | 返回结构 |
| --- | --- | --- | --- | --- |
| 1 | search_runbooks | query（自然语言检索串）、top_k（缺省 5）、filter（结构化过滤：symptom 域、severity、last_reviewed 之后） | top_k ≤10；query 长度上限；embedding 服务超时按 E-UPSTREAM 处理 | `hits[]`：runbook_id、title、score、片段（相关小节原文）、last_reviewed；**无命中或分数低于阈值时 `hits` 为空数组 + `matched=false`**，绝不填充似是而非的结果 |

### 4.2 runbook YAML 结构定义（入库契约）

```yaml
id: rb-oom-restart-storm            # 唯一编号，git 内可追溯
title: 重启风暴初判（OOM 型）
symptoms:                           # 检索锚点：症状关键词/同义词
  - OOMKilled
  - restart loop
  - memory limit
root_cause_candidates:              # 候选根因列表（诊断 Agent 的对照清单）
  - id: rc-memory-limit
    evidence: [container OOMKilled 事件, 内存曲线贴顶]
diagnosis_steps:                    # 建议排查序列（参考性，非指令）
  - 查 OOMKilled 事件时间线
  - 对照内存 working set 与 limits
remediation:                        # 处置建议与级别标注（供分级对照，非放行）
  - action: scale_deployment
    risk_level: R1                  # 期望处置级别，对照 risk-classifier 终判
  - action: open_change_pr          # 涉及 GitOps 管资源时
    risk_level: R2
references:                         # 来源：事故复盘/官方文档/个人经验
  - postmortem-2026-03-xxx
last_reviewed: "2026-09-20"         # 评审时间戳（filter 与维护流程用）
reviewed_by: maintainer             # 评审人
```

字段纪律：`remediation.risk_level` 是**期望处置级别标注**，供 risk-classifier 与 eval 场景标注对照；实际级别以闸门终判为准（I1）。

### 4.3 检索语义

1. query 向量化 → LanceDB Top-K 召回（k 取 top_k 的 2 倍做重排余量）；
2. score 低于命中阈值 → 直接返回空（`matched=false`），阈值随压测校准登记；
3. 命中片段按 YAML 小节原文截取（不二次改写——改写=编造）；
4. 出口统一 sanitize 包裹：runbook 虽属内部数据，但片段同样进 LLM 上下文，且部分内容来自外部事故复盘转录，按统一出口纪律包裹留痕。

### 4.4 知识库维护流程（谁写入、如何评审）

1. **谁写入**：仅项目维护者经 git 评审入库。初始 5–10 条自有排障经验（T1.7）；后续三个来源——真实事故复盘转录（F10 缓解的官方通道）、eval 场景复盘沉淀、公开 postmortem 材料（W20 前收集清单）。**Agent 无写入工具**：运行时不存在"学习入库"的 MCP 入口，防止被注入"把恶意文本写进知识库"（知识库投毒是向量检索的经典攻击面）。
2. **如何评审**：入库走 PR 评审（同代码纪律）；每条带 `last_reviewed`/`reviewed_by`；定期复核触发条件——上游 K8s 行为变更、eval 发现该 runbook 误导诊断、引用的事故复盘被推翻。M3 起 held-out 场景轮换（F10）时同步审计知识库与场景的重合度，防过拟合互哺。
3. **更新纪律**：YAML 改动即 git 历史，向量索引由 CI/本地脚本从 YAML 重建（重建流程版本钉死），不手工改索引文件。

### 4.5 元数据五字段

| 工具 | risk_hint | idempotent | reversible | est_blast_radius | timeout_ms（建议初值） |
| --- | --- | --- | --- | --- | --- |
| search_runbooks | R0 | true | true | 0 | 8000 |

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级 | 对应 |
| --- | --- | --- | --- | --- |
| 参数非法（E-SCHEMA） | query 空/超限、top_k 超限 | 拒绝 + 合法值引导 | 直接失败 | I3 |
| embedding 服务超时/不可达（E-UPSTREAM） | 超时器 | 返回空结果 + `degraded=true` 标注（检索降级≠诊断停止） | **降级**：显式标注的降级优于编造 | I10 精神 |
| 向量索引损坏/缺失 | 打开校验失败 | 返回空 + 告警（索引从 YAML 重建的指引落运维文档） | **降级为空，不编造** | F1 间接防线 |
| 无命中/低分 | score 阈值判定 | `matched=false` + 空 hits + 建议补充查询词 | 正常路径（宁缺毋滥） | T1.7 |
| 片段出口包裹失败（E-SANITIZE） | 包裹器异常 | 整次调用失败 | fail-closed | I4 |

**失效姿态**：本工具的失效默认值是"空"——检索能力挂了，Agent 失去经验加持但不会被假经验误导。空与编造之间永远选空。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- **日志字段**：L1 统一字段集 + `query_digest`（检索串哈希，不落原文）、`top_k`、`matched`、`hits_count`、`lowest_score`、`degraded`、`index_version`。
- **指标**：`aegis_tool_calls_total{server="runbook", tool, result}`；`aegis_runbook_search_total{result}`（result ∈ hit / empty / degraded / error）；`aegis_runbook_entries`（gauge：库内条目数，观测知识库生长）；`aegis_runbook_hit_score`（histogram：命中分水位，校准阈值用）。
- **Trace span**：`l1.runbook.search_runbooks`，子 span `.embed` / `.retrieve` / `.sanitize`。
- **审计留痕点**：检索 query_digest 与命中 runbook_id 列表落 DecisionRecord `spec.toolCalls`——诊断结论引用了哪条经验必须可回放（I6），这也是 F10 审计"eval 指标是否被库内知识污染"的数据源。

## 7. 依赖方向与模块边界

- **谁调我**：诊断 Agent（M1 直连 / M2 起经 MCP 代理）。归因流水线不消费本工具（attributor 走确定性事件与所有权图谱，I9 明确"零 LLM 参与"，也不依赖经验库）。
- **我调谁**：`internal/mcpserver` 框架、`internal/budget`、`internal/sanitize`（M1）、embedding 服务端口（OpenAI 兼容抽象层，设计文档 §4.3 双通道）。
- **禁止依赖谁**：禁止反向依赖 L2 任何组件（检索结果不带任何闸门语义）；**无写入工具**——Agent 侧协议面只读，知识库写路径不经 MCP；embedding 通道只发检索串（query_digest 落日志不落原文，防敏感串入库）。

## 8. 测试策略与红队用例

- **单测**：空结果路径（`matched=false` 语义稳定）；低分截断；top_k 边界；索引损坏降级。
- **集成测试**：初始 5–10 条经验灌入后，用 M1 的 OOMKilled 场景提问验证 Top-K 命中相关 runbook（T1.7 完成判据）；索引重建幂等。
- **红队用例**：
  1. 幻觉压力（F1 间接）：构造库中不存在的故障提问，验证返回空而非编造（T1.7 判据的红队版）；
  2. 知识库投毒推演：构造"经工具写入恶意 runbook"的注入尝试，验证写入入口在协议面不存在；
  3. 检索串注入：query 夹带指令文本，验证按检索语义处理（去向量库而非 LLM 系统位）、出口包裹留痕（I4）；
  4. 阈值漂移：边界分数样本，验证阈值两侧命中/空的一致性；
  5. embedding 不可用：验证 `degraded=true` 空结果路径，诊断链路不中断。
