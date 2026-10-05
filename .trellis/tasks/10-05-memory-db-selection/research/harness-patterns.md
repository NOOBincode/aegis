# Research: harness 简化——借鉴成熟项目（harness patterns）

- **Query**: "harness" = prompt 装配 + 工具循环 + 上下文压缩 + 记忆管理。kagent 是运行时底座（ADR-002）。研究成熟项目如何结构化 harness，让 aegis 不重复造轮子：kagent 自身的上下文工程内部、pi-mono 最小 harness 哲学（ADR-002/ADR-003 已引）、claude-code、Letta/MemGPT（memory blocks / 上下文分层）。给出 aegis 在 kagent 之上需要的最小 harness，以及"借哪块、自建哪块"。
- **Scope**: mixed（内部定界 + 外部项目模式；外部为训练知识，截止 2026-01，本环境无外网）
- **Date**: 2026-10-05

---

## 0. 一句话结论

**kagent/ADK 已经就是 harness 的核心（prompt 装配 + 工具循环 + 会话管理 + OTel），aegis 不重造它。** aegis 在 kagent 之上真正要自建的只有**薄薄三样**：①上下文工程注入器（带 token 上限的快照摘要），②autocompact（机制在 `internal/session`、prompt 在 `agent/`），③prompt 资产版本化 + I8 回归挂点。**复杂件全部"借模式不借框架"**：上下文分层借 Letta/MemGPT，autocompact 的"摘要+保关键决策"借 claude-code，记忆管理借 Mem0 的闭环但不引其框架，反膨胀纪律借 pi-mono。

**借 vs 建总表**：

| harness 部件 | 借 / 建 | 来源 / 落点 |
| --- | --- | --- |
| 工具循环（tool loop） | **借（复用，零代码）** | kagent / Google ADK 引擎 |
| prompt 装配骨架 | **借（复用）** | kagent Agent CRD `systemPrompt` |
| 会话管理（对话历史存取） | **借（复用）** | kagent postgres / ADK session |
| OTel trace | **借（复用）** | kagent/ADK 自带 + aegis OTel collector |
| 上下文工程注入（3 类快照摘要 + token 上限） | **自建（差异化核心）** | `agent/`；分层心智模型借 Letta |
| autocompact | **自建机制 + 借模式** | 机制在 `internal/session`，prompt 在 `agent/`；"摘要+保关键决策"借 claude-code，core/archival 分层借 Letta/MemGPT |
| prompt 版本化 + I8 回归挂点 | **自建** | `agent/`（文件名即版本 + git 历史） |
| 双通道模型抽象（公网/私域 vLLM） | **自建（薄）** | `agent/` OpenAI 兼容抽象层 |
| 记忆管理（长期记忆闭环） | **借模式 + 复用 kagent 存储** | 模式借 Mem0（抽取→存→检索→注入），存储用 kagent `memories` CRD（见 memory-db-options.md） |
| 反膨胀纪律 | **借哲学** | pi-mono 最小 harness（ADR-002/ADR-003） |

---

## 1. 先厘清：aegis 的 harness 横跨 L2/L3 两层

外部项目的 harness 是"一个运行时里的一坨"。aegis 因为**安全分层**，harness 被刻意切成两半——这是与其它项目最大的结构差异，也是为什么不能照搬任何一个项目：

| 关注点 | 归属 | 性质 |
| --- | --- | --- |
| prompt 装配、工具循环、上下文注入、autocompact 的**提示词**、模型抽象 | **L3 `agent/`**（kagent 配置层） | 质量面：影响诊断好坏，可迭代 |
| autocompact 的**触发判定与压缩产物存储**、双预算、会话生命周期、token 计量 | **L2 `internal/session`**（闸门侧） | 安全面：fail-closed，I2 预算判定是确定性逻辑 |

这条红线的原文依据：`internal/session/README.md` §4.5 "**机制在本目录，prompt 在 `agent/`**"；`agent/README.md` §7 "禁止 import `internal/` Go 包…agent 是 L3 独立世界"。**借鉴外部项目时，必须先问"这块属于 L3 质量面还是 L2 安全面"，再决定它落在哪、能不能借。**

---

## 2. 逐项目拆解：借什么、不借什么

### 2.1 kagent（底座，ADR-002）——借整个运行时核心，不碰内核

kagent 0.10.3（CNCF Sandbox，基于 Google ADK）已经提供 aegis harness 的"躯干"：

- **工具循环**：ADK 引擎的 agentic loop（think→act→observe），aegis 不写循环。
- **prompt 装配**：Agent CRD 的 `systemPrompt` 字段 + ToolServer 白名单引用。
- **会话管理**：ADK session 存到配置的 postgres（T0.4 观察到 `Database.Url`/`SessionRetentionDays`）。
- **记忆**：`memories.kagent.dev` CRD（`VectorEnabled:false` = 结构化记忆）。
- **可观测**：ADK 自带 OTel，接进 aegis collector（`deploy/observability/README.md` §4.1）。

**aegis 在 kagent 之上只加两件事**（ADR-002 二开边界原文）：配置层（Agent CR / ModelConfig / prompt 资产）+ 一个新增字段 `guardrailPolicyRef`。**不改 controller 内核、不改 ADK 引擎**。

- **借**：上述全部运行时核心。
- **不借/不碰**：kagent 内核代码；kagent 的 UI 叙事（aegis 差异化在闸门与 eval，不在交互皮，§1.4 N5）。

### 2.2 pi-mono（最小 harness 哲学）——借"反膨胀"这一件事

- **核心主张**（ADR-002 与 `agent/README.md` §3 已引）：运行时 = **prompt 装配 + 工具循环 + 上下文压缩**三件套的最小闭环，不为完整性膨胀；其余能力以最小实现补齐。
- **对 aegis 的价值**：它不是代码参照，而是**纪律参照**——当 ADR-002 触发降级（弃 kagent 自研薄运行时）时，薄运行时就按这三件套做，约 +40h（设计文档 §9）。平时它也充当"别给 harness 加戏"的拦阻器：任何超出三件套 + aegis 差异化（上下文工程/预算/审计挂点）的 harness 功能冲动，都按 Non-Goal 拦阻（纪律 R-5）。
- **借**：三件套清单 + 反膨胀纪律。
- **不借**：pi-mono 是"另一个运行时"，aegis M1–M3 的运行时是 kagent；pi 的三件套只在**降级预案**里才被真正落实为代码。**ADR-003 预留位（"pi harness 评估结论"）正是为此而留**（`docs/adr/README.md` §4 索引表）——本研究的 harness 结论应回填进 ADR-003。

### 2.3 claude-code——借 autocompact 的"摘要 + 保关键决策"范式

- **可借鉴的模式**：长会话逼近上下文上限时，把旧对话压缩成一份**结构化摘要**，显式保留"当前任务状态 / 已做决策 / 待办 / 关键约束"，原始历史归档可查但不占上下文。
- **映射到 aegis**（`agent/README.md` §4 步骤 4 + `internal/session/README.md` §4.5 已定义）：
  - autocompact 产物 = 摘要 + **关键决策列表**（已下结论、已拒绝动作、待办）。
  - aegis 比 claude-code 多一层**安全强制**：关键决策的 constraints 必须携带未决 CR 引用、熔断态、冷却期对象、taint 数据块引用——"这些是闸门正确性的输入，压缩不得丢弃"（`internal/session/README.md` §4.5 原文）。这是 aegis 自研含量所在，纯借 claude-code 拿不掉。
  - 原始历史归档进 DecisionRecord（I6），上下文只留压缩形态。
- **借**：压缩触发心智（逼近上限→压缩）、摘要即"继续工作所需的最小状态"这一设计取向。
- **不借**：claude-code 是单机 CLI，无多副本/无闸门；其压缩纯为省 token，aegis 的压缩还要过审计与安全约束，机制必须在 L2。

### 2.4 Letta / MemGPT——借 memory blocks 与上下文分层（tiered memory）

- **核心模式**：把 LLM 上下文显式分两层——**core memory**（常驻上下文的可编辑小块，相当于"永远在系统位里的关键事实"）与 **archival/recall memory**（体外大容量存储，向量/DB，按需分页调入）。配合**自编辑**：Agent 用工具主动读写自己的 memory block。
- **映射到 aegis 的上下文工程**（`agent/README.md` §4）——这是最有解释力的一层对应：
  - aegis 的三类快照摘要（拓扑 / 异常对象 / 近期事件，各设 token 上限 T1/T2/T3）**本质上就是三个 pinned 的 "core memory block"**——常驻、有硬上限、被截断即标注。
  - runbook 向量库 +（未来）语义记忆 = **archival memory**，按需检索调入。
  - autocompact = core 装不下时把状态"驱逐"到归档（DecisionRecord），上下文只留压缩形态。
- **借**：**分层心智模型**（常驻小块有预算 vs 体外大容量按需取）+ "每块有显式 token 预算"的做法。这让 aegis 的上下文工程有一个被验证过的概念框架，而不是拍脑袋。
- **不借**：Letta 是完整的 agent 服务框架（自带 server、存储、多 agent 编排），aegis 已选 kagent 作底座，不引入第二个运行时；Letta 的"Agent 自编辑 core memory"在 aegis 要**收紧**——core memory（快照摘要）由 aegis 注入器生成，不让 Agent 随意改写自己的"事实基座"，防注入自我强化（I4）。

### 2.5 Mem0 / Zep——借记忆闭环模式，不引框架

- **Mem0 模式**：从对话中**抽取**结构化记忆 → **存**向量/图后端 → 检索时**召回** → **注入**上下文。闭环清晰。
- **Zep 模式**：时间知识图谱，实体/事实带时间戳，适合"事实随时间演化"的记忆。
- **对 aegis**：MVP 的记忆需求是事实型、可按资源/症状键检索（见 memory-db-options.md §1b），结构化记忆够用。借 Mem0 的**闭环形态**（抽取→存→检索→注入）作为未来记忆子系统的骨架即可。
- **不借**：不引入 Mem0/Zep 框架本身——它们都自带向量后端 + LLM 抽取依赖 + 写入口，对个人项目过重，且记忆写入口是投毒攻击面（I4，同 runbook"知识库投毒"红线）。Zep 的时间图谱对 MVP 过度设计。
- **aegis 特有限制**：记忆写入口**不作为 MCP 工具暴露给 Agent**（同 runbook §4.4"Agent 无写入工具"），写入走评审/离线流程——这是与其它 agent 记忆系统的关键安全差异。

---

## 3. aegis 最小 harness 定义（在 kagent 之上）

把上面收拢成一份"最小 harness = kagent 给的 + aegis 必须自建的"清单。**aegis 自建的只有四薄样，全部是质量面/差异化面，安全面机制归 L2：**

| # | 自建件 | 落点 | 借的模式 | 完成判据锚点 |
| --- | --- | --- | --- | --- |
| 1 | **上下文工程注入器**：拓扑/异常/事件三类快照摘要，各带 token 上限，禁裸灌全量 YAML，截断即标注 | `agent/`（数据来自 L1 只读工具，经 sanitize 包裹） | Letta 分层 + 每块 token 预算 | T1.6"注入有单测证明不超上限" |
| 2 | **autocompact**：触发判定 + 压缩产物存储（机制 L2 `internal/session`）+ 摘要/关键决策选取的 prompt（L3 `agent/`）；constraints 强制携带未决 CR/熔断态/冷却期/taint 引用 | `internal/session` + `agent/` | claude-code 摘要范式 + Letta core→archival 驱逐 | T1.4/T1.6 + 红队"压缩后回放 DR 全量事件链完整"（I6 正交性） |
| 3 | **prompt 资产版本化 + I8 回归挂点**：文件名即版本，Agent CR 引用具体版本，变更触发回归 | `agent/` | —（git 即版本史） | R-3（M3 起 CI 强制） |
| 4 | **双通道模型抽象**：公网 OpenAI 兼容 + 私域 vLLM 同一接口 | `agent/` | — | T1.6 公网先跑通，私域留接口 |

**kagent 给的（零 aegis 代码）**：工具循环、prompt 装配骨架、会话存取、OTel trace、memories CRD。

**显式不做（Non-Goal / 反膨胀，pi-mono 纪律）**：不自建工具循环、不自建会话存储、不引第二个 agent 框架（Letta/Mem0/Zep）、不给 harness 加超出"三件套 + 差异化四样"的功能。

---

## 4. 决策落点：回填 ADR-003

- `docs/adr/README.md` §4 索引表已留 **ADR-003 = "pi harness 评估结论"，status: reserved**，等待评估输入。
- 本研究（连同 memory-db-options.md 的存储侧）即为该评估输入。建议：ADR-003 立为"**harness 形态决策 = kagent 核心复用 + aegis 四薄样自建 + 模式借自 claude-code/Letta/Mem0/pi-mono**"，五段式补齐（背景/决策/代价与接受/反方意见/复评触发条件），其中"反方意见留档"= ADR-002 的降级预案（弃 kagent 时按 pi-mono 三件套自研薄运行时）。
- 复评触发条件沿用 ADR-002（kagent 上游剧变 / fork 维护 >20h/月），M1 末首评。

---

## 5. 关键模式速查（实现期可直接对照）

| aegis 要做的事 | 直接参照 | 参照点 |
| --- | --- | --- |
| 上下文分块带 token 预算 | Letta memory blocks | 每块常驻、有上限、可截断标注 |
| 长会话压缩 | claude-code autocompact | 摘要保留"当前状态+已决+待办+约束"，原文归档 |
| 压缩时什么不能丢 | （aegis 独有，无现成参照） | constraints 强制带未决 CR/熔断态/冷却期/taint（`internal/session` §4.5） |
| 记忆子系统骨架 | Mem0 | 抽取→存→检索→注入闭环；写入口不外露 |
| 记忆/上下文分层 | Letta/MemGPT | core（常驻小块）vs archival（体外按需取） |
| harness 该多小 | pi-mono | prompt 装配 + 工具循环 + 上下文压缩三件套止 |
| 运行时核心从哪来 | kagent/ADK | 工具循环、会话、OTel 全现成，零自建 |

---

## Caveats / Not Found

1. **无外网**：kagent 0.10.3 上下文工程内部（ADK session 压缩钩子、`memories` CRD 的检索 API）无法在线核验；对 kagent 的判断基于其公开架构（Agent CRD + ADK + ToolServer）+ T0.4 部署观察。**建议实锤点**：kagent 是否暴露"上下文注入/autocompact 的可插拔钩子"，决定 aegis 的上下文工程是"配置"还是"要在 ADK session 层写胶水代码"——这直接影响 T1.6 的 30h 估时是否准确。列入 landing-plan-delta 验证项。
2. **claude-code / pi-mono 的内部实现细节**为训练知识（其确切压缩算法/三件套边界以各自仓库为准）；本研究只取其**公开设计取向**，不依赖私有实现，对结论无实质影响。
3. **Letta 的"Agent 自编辑 core memory"与 aegis I4 存在张力**（aegis 收紧为注入器生成、不让 Agent 改写事实基座）——这是有意偏离参照，实现期需在 `agent/README.md` 的上下文工程小节写清该偏离，防止后来者照 Letta 抄回自编辑。
