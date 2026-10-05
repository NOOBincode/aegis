# Research: 记忆存储与数据库选型（memory & database selection）

- **Query**: aegis 该用什么存储栈来承载 会话/记忆/审计/向量/eval/工具元数据 六类数据；kagent 0.10.3 已自带 postgres + `memories.kagent.dev` CRD + session 存储——是复用它，还是另建？要求私域/离线可部署、kind 单节点可跑、个人项目不过度设计。
- **Scope**: mixed（内部为主 + 外部项目知识，外部事实来源为训练知识，截止 2026-01；本环境无外网，已实测 connection reset，无法在线核验）
- **Date**: 2026-10-05

---

## 0. 一句话结论

**设计文档其实已经把六类数据的归属定了大半**，本研究的作用是（a）把分散在 §3.3 / `internal/session` / `internal/audit` / `crds` 的决策收拢成一张"存储分工总表"，（b）回答唯一真正的新问题——**kagent 自带的 postgres 该不该被 aegis 的业务状态复用**。

结论：**复用，但只复用到 L3 运行时自己的边界内（kagent 的会话连续性 + `memories` CRD 承载的 Agent 长期记忆）；aegis 的安全态/审计态一律不进 kagent 的 postgres**。审计（不可变）= etcd/CRD，会话（ ephemeral + 预算）= Redis/Valkey，记忆（可变 + 质量面）= kagent postgres（圈禁在 L3），向量 = LanceDB 嵌入式。**新增基础设施净额 ≈ 一个 gatekeeper 专用 Valkey 容器**（env-up.sh 当前缺它）。

**三平面分离**（任务要求的清晰划分）：

| 平面 | 数据 | 存储 | 可变性 | 丢掉它的后果 |
| --- | --- | --- | --- | --- |
| **审计（immutable）** | DecisionRecord、ChangeRequest 状态机、GuardrailPolicy | **etcd / K8s CRD** | 写入即封存（RBAC 无 update + finalizer sealed + WORM 导出，§5.3 R19） | 不可接受 → 所以放控制面 etcd，fail-closed |
| **记忆（mutable，质量面）** | Agent 长期记忆、L3 会话连续性 | **kagent 自带 postgres（圈禁 L3）** | 可改可删，TTL 管理 | 可接受 → Agent 变笨但闸门不受影响；随 ADR-002 降级可弃 |
| **会话（ephemeral，安全面）** | 预算计数、冷却期、暂停/恢复态、快照本体 | **Redis / Valkey**（gatekeeper 专用实例） | 可丢可重建 | 可接受 → 重建即可；变更事实永远在 CRD（I10） |
| **向量（embedded，质量面）** | runbook 知识、（未来）Agent 语义记忆 | **LanceDB 嵌入式** | 可从 YAML 重建 | 可接受 → 降级为空返回，不编造（runbook/README §5） |

---

## 1. 六类数据的归属判定（逐条给答案）

### (a) 会话状态 + 对话上下文（含 autocompact）→ 拆两半

这是最容易混淆的一类，必须拆成**两个层级的"会话"**：

| 子项 | 归属层 | 存储 | 依据 |
| --- | --- | --- | --- |
| L3 对话历史（LLM 实际看到的 message list）、kagent 会话生命周期 | L3 运行时 | **kagent postgres（复用，不自建）** | kagent/ADK 原生职责；T0.4 已观察到 controller config `Database.Url=postgres://...`、`SessionRetentionDays`。自建=重复造轮子 |
| L2 安全会话（sessionRef 生命周期、双预算计数、冷却期、暂停/恢复） | L2 闸门 | **Redis/Valkey（自建，已在设计内）** | `internal/session/README.md` §2-3、`internal/adapters/redis/README.md` §4.1 key 设计；这是 F4/F5 的执行点，是**安全组件**，必须与运行时解耦 |
| autocompact 压缩产物（摘要 + 关键决策留存） | L2 机制 / L3 prompt | **Redis**（机制层，`internal/session` §4.5）；原始历史归档进 **DecisionRecord**（etcd，I6） | `internal/session/README.md` §4.5、`agent/README.md` §4 步骤 4 |

**为什么不能把 L2 预算/会话放进 kagent postgres**：预算判定是 fail-closed 的安全逻辑（`internal/session/README.md` §4.4：Redis 断连即视为预算耗尽、写路径阻塞）。ADR-002 预留了"弃用 kagent、自研薄运行时"的降级路径——若预算计数寄生在 kagent postgres 里，降级瞬间预算机制随之消失，F4 失控面洞开。**安全态必须寄生在 aegis 自己的存储上**。

### (b) Agent 长期记忆 → kagent `memories` CRD（M1 先用，结构化够用）

- **复用 kagent `memories.kagent.dev` CRD + 其 postgres**。T0.4 观察到 `VectorEnabled:false`，即默认是**结构化/精确检索记忆**，无语义向量召回。
- 对 aegis MVP 的记忆需求（"payments 服务上月出过同类 OOM"、"某对象的 owner 是 HPA"这类**事实型、可按资源/症状键检索**的记忆），结构化记忆**够用**。零新增基础设施。
- **语义/向量记忆推迟到 M1 之后**：若确需"相似故障召回"，**扩展现有 LanceDB**（runbook 已在用）而不是给 kagent postgres 开 pgvector、更不是引入独立向量库。理由见 §3。
- **红线**：记忆是**可变质量面**，绝不承载任何安全/审计语义；记忆写入口要防投毒（同 runbook 的"知识库投毒"攻击面，I4/`mcp-servers/runbook/README.md` §4.4——Agent 无写入 MCP 工具，写入走评审）。

### (c) DecisionRecord 审计链（immutable, I6）→ etcd/CRD，**绝不进 postgres**

- 已是定案：`internal/audit/README.md` §3 "DecisionRecord 是 CRD，存储即 K8s API（etcd）…不引入外部不可变存储系统"；不可变靠 RBAC 无 update + finalizer sealed + WORM 导出三件套（§5.3 R19）。
- **kagent postgres 类别性不适合审计**：它可变、归 kagent 所有、随 ADR-002 可弃。审计叙事（I6"你怎么证明 Agent 没越权"）一旦寄生在可被一次性丢弃的运行时存储里即崩塌。
- 快照本体体积大，存 Redis，DR 只记 `clusterSnapshotRef` + 哈希（`internal/audit/README.md` §3）。

### (d) runbook / 知识向量 → LanceDB 嵌入式（已定，T1.7）

- 已定案：设计文档 §3.3、`mcp-servers/runbook/README.md` §3 "LanceDB 嵌入式，零运维…放弃外部向量服务：MVP 过度设计"。
- **维护纪律**：向量索引由 CI/脚本从 runbook YAML 重建，不手工改索引文件（`mcp-servers/runbook/README.md` §4.4-3）。

### (e) eval 场景 + 结果 → Git + 文件系统 + DecisionRecord 引用，**无需数据库**

- 场景 = Chaos Mesh manifest + 期望根因标注，存 git（`eval/scenarios/`）。
- 结果 = 按 `run_id` 归档的文件（`config` / `result.json` / `report.md` + DecisionRecord 集合引用），基线 = 指定 run 的升格（`eval/README.md` §4）。
- LLM 调用链回放在 Langfuse（`llmTraceRef` 弱引用，单向，§5.3/`deploy/observability/README.md` §4.2）。
- 结论：**eval 不需要数据库**，文件 + git + CRD 引用足够；这与"个人项目不过度设计"一致。

### (f) 工具元数据 → kagent ToolServer CRD + internal/mcpserver 注册 schema，**无需数据库**

- 元数据五字段（`risk_hint/idempotent/reversible/est_blast_radius/timeout_ms`）注册进 ToolServer CRD，由 `internal/mcpserver` 框架强校验（缺一注册失败，落地方案 T1.1）。
- 归属 = K8s CRD（声明即契约），不需要独立存储。

---

## 2. 候选存储对比矩阵

维度：**离线/私域可部署**（E3 硬约束，`deploy/README.md` §4.1）、**kind 单节点内存占用**（16GB 底线，R29）、**个人项目运维成本**、**适配的 aegis 数据类**、**结论**。

| 存储 | 离线可部署 | kind 内存 | 运维成本 | 适配数据类 | 结论 |
| --- | --- | --- | --- | --- | --- |
| **etcd / K8s CRD** | ✅（kind 自带） | 0（已在跑） | 零（控制面已有） | (c) 审计、(f) 工具元数据、变更状态机 | **采用 = 主存储（审计+控制态）** |
| **Redis / Valkey** | ✅ 完全离线 | 极低（~10–30MB RSS） | 极低（单容器，docker-compose 从简） | (a) 安全会话/预算/冷却期/快照本体 | **采用 = ephemeral 会话平面**。选 **Valkey**（Linux 基金会 OSS fork，与 Redis 兼容、私域叙事更干净，观测栈已用 `valkey:8.0-alpine`） |
| **kagent 自带 postgres** | ✅（随 kagent chart 离线装） | 已在跑（T0.4） | 零（kagent 自管） | (a) L3 对话历史、(b) Agent 长期记忆 | **复用但圈禁 L3**，不承载 aegis 安全/审计态 |
| **LanceDB 嵌入式** | ✅（纯本地文件，无外网） | 嵌入式，无独立进程 | 零（库形态） | (d) runbook 向量、（未来）(b) 语义记忆 | **采用 = 唯一向量层** |
| **pgvector**（加到现有 postgres） | ✅（`pgvector/pgvector:pg16` 镜像可离线） | 复用现有 pg | 低 | (b)/(d) 向量 | **不采用**：aegis 已有 LanceDB 作向量层，引入 pgvector = 第二个向量栈，违背最小化；且 kagent 的 pg 归 kagent 管，不应被 aegis 业务依赖 |
| **SQLite** | ✅ | 嵌入式 | 零 | 可作 (a)/(e) 的替代 | **不采用为会话存储**：高频 TTL 缓存 + 双预算原子计数是 Redis 的主场，SQLite 不擅长高频过期间写；eval 结果用文件即可，也不需它。设计文档 §3.3 已定 Redis |
| **Qdrant / Milvus / Weaviate** | ✅ 但重 | 高（独立服务，数百 MB–GB） | 高（独立部署/备份/升级） | (b)/(d) 向量 | **不采用**：个人项目 + 16GB kind 上过度设计（设计文档 §3.3 已明确放弃外部向量服务） |
| **Mem0 / Zep（记忆框架）** | ✅ OSS 可自托管，但都需自带向量后端 + LLM 抽取 | 中–高 | 中–高 | (b) Agent 记忆 | **不整体采用，只借模式**（见 harness-patterns.md）。Mem0 的"抽取→存→检索→注入"闭环、Zep 的时间知识图谱对 MVP 过重；且引入 LLM 抽取依赖与写入口，放大记忆投毒面（I4） |

---

## 3. 推荐栈（ONE 主存储 + 可选向量层）

> 任务要求"ONE primary store + optional vector layer"。落到 aegis 语境：

- **主存储（store of record）= etcd / K8s CRD**：一切安全态与审计态（DecisionRecord、ChangeRequest、GuardrailPolicy、工具元数据）。这是 I1/I5/I6/I10 的物理底座，天然随 kind 就位、零新增运维。
- **可选向量层 = LanceDB 嵌入式**：runbook（现在）+ Agent 语义记忆（如未来需要）。纯本地文件，离线零运维。
- **ephemeral 缓存 = Valkey**：会话/预算/冷却期/快照本体。可丢，崩溃恢复永远以 CRD 为准（I10）。
- **复用而非自建 = kagent postgres**：仅限 L3 对话连续性 + Agent 记忆（`memories` CRD），圈禁在运行时边界内。
- **观测自闭环 = Langfuse 的 postgres/clickhouse/valkey/minio**：只服务 LLM trace 回放，DR→Langfuse 单向弱引用（`deploy/observability/README.md` §7：观测不成为审计依赖）。**不要为省一个实例去复用 Langfuse 的 postgres 存业务数据**——那会把观测层变成审计依赖，违反 I10。

**净新增基础设施**：仅 **一个 gatekeeper 专用 Valkey**（当前 `scripts/env-up.sh` 的 `DEFAULT_COMPONENTS=(chaos-mesh prometheus argocd kagent)` 与 `deploy/observability/docker-compose.yml` 里都没有供闸门会话用的 Redis/Valkey；观测栈的 valkey 是 Langfuse 的，不可共用——见 §4 红线 2）。

---

## 4. 复用 vs 自建：kagent postgres 的裁决（任务核心张力）

**裁决：复用 kagent postgres 做"它本来就在做的事"（L3 会话连续性 + `memories` CRD 的 Agent 记忆），不为 aegis 业务状态复用它。**

四条理由（按权重）：

1. **ADR-002 降级安全**：降级预案是"弃 kagent、自研薄运行时"。凡寄生在 kagent postgres 的 aegis 状态，降级时随之丢失。会话预算/冷却期是 F4 防线、审计是 I6 底线——两者都不允许随运行时一起被丢弃。=> 安全态与审计态必须在 aegis 自有的 Redis + etcd。
2. **不可变性错配**：postgres 可变、归 kagent 管；DecisionRecord 要求 RBAC 无 update + sealed + WORM（§5.3 R19）。把审计放 postgres 等于自拆 I6。
3. **失效姿态自明（I10）**：`internal/session` 的预算判定要求"存储断连=视为预算耗尽=写路径 fail-closed"。这个语义只有在 aegis 可控的适配器（`internal/adapters/redis`）里才能精确实现；寄生于 kagent 的内部 schema 无法保证。
4. **最小化**：为 L3 对话/记忆另建一套存储是重复造轮子——kagent 已经做了，且 ADK 的 session 管理是现成件。复用它（在 L3 边界内）正是"复用不重写"原则（`agent/README.md` §1）的体现。

**三条红线（防"便利的复用变成承重墙"）**：

1. **kagent postgres 里只允许有 kagent 自己的东西**：Agent/ModelConfig 配置、ADK 会话、`memories` CRD 数据。任何 `internal/*` 上下文（risk/audit/session/…）禁止读写它。
2. **闸门会话用独立 Valkey 实例**，不共用 Langfuse 的 valkey、不共用 kagent 的任何东西。观测/运行时的存储都不是闸门的安全依赖。
3. **Agent 记忆（memories CRD）永远是可变质量面**：不作为任何放行/审计依据（I1：放行只由代码与策略决定）；写入口防投毒（无 MCP 写工具，走评审，同 runbook 红线）。

---

## 5. 与现有文档的对账（本研究"确认"了什么 / "新增"了什么）

| 项 | 现状 | 本研究 |
| --- | --- | --- |
| 审计→etcd/CRD | 已定（`internal/audit` §3，§5.3） | ✅ 确认，排除 postgres |
| 会话/预算→Redis/Valkey | 已定（`internal/session` §2-3，`internal/adapters/redis`） | ✅ 确认；**新增**：落地专用 Valkey 实例（env-up 当前缺） |
| runbook 向量→LanceDB | 已定（§3.3，runbook README §3） | ✅ 确认；**扩展**：未来 Agent 语义记忆也走它 |
| Agent 长期记忆 | **未明**（设计文档只字未提"长期记忆"的存储） | 🆕 **本研究新增裁决**：复用 kagent `memories` CRD，圈禁 L3，语义记忆推迟 |
| eval 结果 | 已定（eval README §4，文件+git+CRD 引用） | ✅ 确认，无需数据库 |
| 工具元数据 | 已定（ToolServer CRD + mcpserver 强校验） | ✅ 确认，无需数据库 |
| 双 postgres（kagent + Langfuse） | 已各自存在 | ⚠️ 明确**不互用、不被业务复用**，各自圈禁 |

---

## Caveats / Not Found

1. **无外网**：本环境实测 connection reset，无法在线核验 kagent 0.10.3 的 `memories` CRD 精确 schema 与 `VectorEnabled` 的确切语义（是否即 pgvector 语义记忆开关）。kagent 侧事实依据 = 任务简报 + T0.4 prd 记录（`Database.Url=postgres://...`、`SessionRetentionDays`、`VectorEnabled:false`、`memories` CRD 存在）。**建议 T0.4 后续用 `kubectl explain memories.kagent.dev` 与读 controller config 实锤一次**（成本 5 分钟，落在 landing-plan-delta 的验证判据里）。
2. **LanceDB 的 Go 绑定疑似依赖 CGO**：`mcp-servers/runbook` 是 Go 进程（有 `main.go`），而落地方案附录 B-2 明确"禁 CGO"。LanceDB 官方 Go 客户端（lancedb-go）历史上是经 CGO 封 Rust 核心。**这是一个未解冲突**，可能影响 runbook（T1.7）与未来语义记忆的实现形态（Go 进程内嵌 vs Python sidecar vs 纯 Go 向量替代）。**必须在 T1.7 前裁决**，已列入 landing-plan-delta。
3. **16GB 内存预算（R29）**：当前 kind(3 节点) + Chaos Mesh + Prometheus + ArgoCD + kagent(含其 pg) + Langfuse 四件套已经吃紧。新增 gatekeeper Valkey 虽极轻，但建议在 W2 出口自检的资源预算表里补一行实测量（R29 整改项本就要求 `deploy/versions.md` 增资源预算表）。
4. **训练知识截止 2026-01**：Mem0/Zep/Letta 的最新版本能力可能已有变化；本研究只用它们的**架构模式**（不依赖具体版本 API），故对结论无实质影响。
