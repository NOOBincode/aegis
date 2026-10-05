# Research: 落地方案增量（landing-plan delta）——记忆/存储与 harness 选型落地

- **Query**: 把"记忆存储与数据库选型 + harness 借鉴"两份研究落成可并入《落地方案 v1.x》的具体增量：新增/修改的任务条目、验收判据、里程碑固定动作。要求可直接 merge。
- **Scope**: internal（依据落地方案 v1.0 体例：任务表 = ID | 任务 | 交付物 | 估时 | 完成判据；里程碑末固定动作；§12 变更纪律）
- **Date**: 2026-10-05

> 上游研究：`research/memory-db-options.md`（存储选型 + kagent postgres 复用裁决）、`research/harness-patterns.md`（harness 借/建 + ADR-003 回填）。
> 体例对齐：本 delta 不改设计文档已定的形态级决策（审计→etcd、会话→Redis、向量→LanceDB 已是 §3.3 与各 README 定案），只做"确认 + 补漏 + 立 ADR"。凡与既有任务重叠处以修改行（标 `改`）呈现，新增以 `新` 呈现。

---

## 1. 变更总览（merge 清单）

| # | 动作 | 落到 | 触发纪律 |
| --- | --- | --- | --- |
| 1 | **新增任务 T0.7**：存储与 harness 选型定盘（立 ADR-003 + 新 ADR-004） | Phase 0 §3 | 落地方案 §12-2（新增 ADR） |
| 2 | **修改任务 T1.4**：补"gatekeeper 专用 Valkey 实例"交付与判据 | M1 §4 | 改任务需同步周排期 W7 |
| 3 | **修改任务 T1.7**：前置 LanceDB-Go CGO 可行性闸 | M1 §4 | — |
| 4 | **修改任务 T1.6**：补"kagent 上下文注入钩子实锤"前置验证 | M1 §4 | — |
| 5 | **M1 末尾固定动作 +1**：存储隔离（quarantine）验证 | M1 §4 末尾 | 并入红队检查点 #1 同周 |
| 6 | **资源预算表 +2 行**：gatekeeper-valkey + 双 postgres 实测占用 | §9 / `deploy/versions.md` | R29 整改项的延伸 |

---

## 2. 新增任务（并入 Phase 0 §3 任务表）

### 新 T0.7 — 存储与 harness 选型定盘（立 ADR-003 / ADR-004）

| 字段 | 内容 |
| --- | --- |
| **任务** | 把本研究两份结论拍板为 ADR：ADR-003 填"pi harness 评估结论"预留位（harness 形态）；新增 ADR-004（记忆/存储选型）。同时把 kagent 上下文注入钩子、LanceDB-Go CGO 两个技术未决点实锤。 |
| **交付物** | ① `docs/adr/ADR-003-*.md`（五段式齐全，status 转 accepted）；② `docs/adr/ADR-004-memory-storage-selection.md`（五段式）；③ `deploy/versions.md` 资源预算表新增 gatekeeper-valkey 行 + 双 postgres 实测占用；④ 两个技术实锤结论写入 ADR-004 附录 |
| **估时** | 6h（ADR 写作 4h + 两个实锤验证 2h） |
| **完成判据** | 见下 |

**T0.7 完成判据（逐条可核验）**：

1. ADR-003 五段齐全并转 accepted：背景（harness 借/建分歧）、决策（kagent 核心复用 + aegis 四薄样自建 + 模式借 claude-code/Letta/Mem0/pi-mono）、代价与接受、反方意见留档（= ADR-002 降级预案，弃 kagent 则按 pi-mono 三件套自研薄运行时）、复评触发条件（沿用 ADR-002，M1 末首评）。
2. ADR-004 五段齐全并转 accepted，决策正文中嵌入下方 §3 的**存储分工总表**与 §4 的**三条复用红线**。
3. `docs/adr/README.md` §4 索引表：ADR-003 状态由 reserved 改 accepted；新增 ADR-004 行。
4. **实锤 a（kagent 上下文钩子）**：用 `kubectl explain memories.kagent.dev` + 读 kagent 0.10.3 controller/backend 源码或文档，确认"快照摘要注入 / autocompact"在 kagent 里是**纯配置可达**还是**需在 ADK session 层写胶水代码**；结论写进 ADR-004 附录。若需胶水代码，回头校准 T1.6 的 30h 估时。
5. **实锤 b（LanceDB-Go CGO）**：确认 `mcp-servers/runbook`（Go 进程）引入 LanceDB 是否触发落地方案附录 B-2 的"禁 CGO"。三选一并写入 ADR-004 附录：① LanceDB-Go 纯 Go 可用→维持 Go；② 需 CGO→runbook 改 Python MCP 进程（eval/ 已是 Python，不破依赖图）或经 sidecar 调用；③ 破例允许单进程 CGO 并在版本表留痕。**此实锤是 T1.7 的放行闸**（见 §4 改 T1.7）。

---

## 3. 待嵌入 ADR-004 的"存储分工总表"（决策正文素材）

| 数据类 | 存储 | 平面 | 可变性 | 出处 |
| --- | --- | --- | --- | --- |
| DecisionRecord 审计链 | etcd / CRD | 审计（immutable） | 写即封存（RBAC 无 update + sealed + WORM，§5.3 R19） | `internal/audit/README.md` §3 |
| ChangeRequest 状态机 / GuardrailPolicy | etcd / CRD | 审计 + 控制 | 状态机迁移留痕 | §5.1/§5.2 |
| 会话预算 / 冷却期 / 暂停态 / 快照本体 | **Valkey（gatekeeper 专用实例）** | 会话（ephemeral，安全面） | 可丢可重建 | `internal/session`、`internal/adapters/redis` |
| L3 对话历史 / Agent 长期记忆 | **kagent postgres（圈禁 L3）** | 记忆（mutable，质量面） | 可改可删，TTL | kagent ADK session + `memories` CRD |
| runbook 向量 /（未来）语义记忆 | **LanceDB 嵌入式** | 向量（embedded，质量面） | 可从 YAML 重建 | §3.3、`mcp-servers/runbook/README.md` |
| eval 场景 + 结果 | git + 文件系统 + CRD 引用 | — | run_id 归档 | `eval/README.md` §4 |
| 工具元数据 | kagent ToolServer CRD | 契约 | 声明即契约 | T1.1 |
| LLM trace 回放 | Langfuse（自带 pg/clickhouse/valkey/minio） | 观测 | 弱引用，单向 | `deploy/observability/README.md` §4.2 |

## 4. 待嵌入 ADR-004 的"三条复用红线"（决策正文素材）

1. kagent postgres 只装 kagent 自己的东西（Agent/ModelConfig、ADK session、`memories` CRD）；任何 `internal/*` 上下文禁止读写它。
2. 闸门会话用**独立 Valkey 实例**，不共用 Langfuse 的 valkey、不碰 kagent 的任何存储。
3. Agent 记忆是可变质量面，永不作放行/审计依据（I1）；写入口不作 MCP 工具暴露（防投毒，I4，同 runbook §4.4）。

---

## 5. 修改既有任务（改行，体例同落地方案 §4）

### 改 T1.4（读路径双预算 / 会话存储）

- **交付物**追加：`deploy/` 增加 gatekeeper 专用 **Valkey** 实例（docker-compose 从简，与 Langfuse 的 valkey 物理隔离；版本钉入 `deploy/versions.md`）。
- **完成判据**追加一条：`make up` 后 gatekeeper-valkey 就绪；杀 gatekeeper 进程重启后会话预算计数可从 Valkey 恢复（变更事实仍以 CRD 为准，I10）；**且该实例与 Langfuse valkey、kagent postgres 互不共用**（网络/凭证层隔离，可演示）。

### 改 T1.7（runbook 检索工具 / LanceDB）

- **前置闸**：T0.7 实锤 b（CGO 裁决）通过后才开工。
- **完成判据**追加一条：runbook 的进程形态（Go 内嵌 / Python 进程 / sidecar）与 ADR-004 附录的 CGO 裁决一致，且未违反落地方案附录 B-2（或已按裁决留痕破例）。

### 改 T1.6（L3 运行时接入 / 上下文工程）

- **前置验证**：T0.7 实锤 a（kagent 上下文钩子）结论落地后，上下文工程按"配置"或"ADK 胶水"对应实现；若需胶水代码，先回报估时偏差（>20% 触发落地方案 §12-1 重排）。
- **完成判据**保持原文（注入单测不超上限），追加：autocompact 的 constraints 强制携带未决 CR / 熔断态 / 冷却期 / taint 引用（`internal/session/README.md` §4.5），用 M1 OOMKilled 会话做黄金样例证明压缩后关键决策不丢。

---

## 6. M1 末尾固定动作追加（并入 §4 末尾，与红队检查点 #1 同周）

> **存储隔离（quarantine）验证**——防"便利的复用变成承重墙"：

1. 静态核查：grep `internal/`、`mcp-servers/` 全仓，确认无任何代码连接 kagent 的 postgres（连接串/host 只可能出现在 kagent 自己的 chart 值里）。
2. 降级演练（随 ADR-002 复评一并做）：假设弃用 kagent，列出 aegis 会失去什么——**正确答案是只失去 L3 对话连续性与 Agent 记忆，预算/审计/冷却期全部存活**（因为它们在 Valkey + etcd）。若列出任何安全/审计态随之丢失，即 quarantine 被突破，冻结相关开发先补档（对齐 ADR 失效语义）。
3. 实测记录：双 postgres（kagent + Langfuse）+ etcd + 两个 valkey + LanceDB 的常驻内存占用，回填 `deploy/versions.md` 资源预算表（R29 延伸），确认 16GB 底线仍成立。

---

## 7. 与 §12 变更纪律的对齐

- 本 delta 触发 §12-2（新增 ADR-003 填实 + ADR-004 新立）→ 落地方案随之修订，修订记录追加文末。
- 不触发 §12-3（无 Non-Goal 突破：未新增任何外部重型存储，净增基础设施 = 一个 Valkey 容器）。
- 估时影响：新增 T0.7 = 6h，落在 Phase 0 缓冲内；T1.4/T1.6/T1.7 为判据追加/前置闸，不改原估时量级（除非实锤 a 显示需 ADK 胶水代码，届时按 §12-1 重排）。

---

## Caveats / Not Found

1. 本 delta 是**提案**，未并入正式《落地方案》文件（研究任务不改动 research/ 之外的文件）；merge 动作应由主 agent 在评审后执行。
2. T0.7 的两个实锤（kagent 钩子、LanceDB CGO）是本 delta 里唯一带"可能回头改估时/改形态"的不确定项；其余均为对已定格式的确认与落地。
3. ADR 编号：本研究假设 ADR-004 未被占用（当前索引表仅到 ADR-003 reserved）。merge 前需 `ls docs/adr/` 复核编号不撞。
