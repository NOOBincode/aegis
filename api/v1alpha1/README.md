# v1alpha1 — 四个 CRD 的 Go 类型设计说明

> 所属层：控制面契约层（api/ 子层） ｜ 里程碑：M1（DecisionRecord）→ M2（ChangeRequest/GuardrailPolicy）→ M4 占位（SchedulingHint） ｜ 设计文档出处：§5.1–§5.4 ｜ 关联不变量：I5（回滚字段）、I6（审计字段）、I7（配额字段）、I9（authorityCheck）、I10（abort/恢复/幂等字段） ｜ 关联失败模式：F4（sessionBudget）、F6（rollback 漂移）、F12（验证）、F14（在途恢复）、F15（idempotencyKey）

## 1. 设计初衷

`api/v1alpha1` 是 aegis.dev 组的第一个 API 版本，承载四个 CRD 的 Go 类型。设计基调照设计文档 §5 开篇："第一版即包含中止/恢复/超时/回滚字段——与 I5/I6/I7 保持一致，不留'以后再加'"（R6 整改的直接产物）。四个对象分工：ChangeRequest 是闸门核心对象（一次变更的全生命周期）；GuardrailPolicy 是可评审的护栏 YAML（集群级安全参数）；DecisionRecord 是审计对象（落盘不可变存储）；SchedulingHint 是 M4 可选模块的契约占位（hint 是建议而非指令，调度器可拒绝）。

## 2. 职责与任务清单

职责：为四个 CRD 提供类型定义、默认值、kubebuilder validation 标记、deepcopy 生成方法与 printer columns 规划；本层禁止业务逻辑（负面清单见 `api/README.md`）。

任务清单：T1.8（DecisionRecord，§5.3 字段全量）、T2.1（ChangeRequest/GuardrailPolicy，§5.1/§5.2 字段第一版全量，含 abort/timeout/rollback.deadline/idempotencyKey）、W10 碎片任务（CRD 字段对照 §5.1/§5.2 逐字段核对）、M4 决策门 go 后补 SchedulingHint 完整类型（本期仅留契约位）。

## 3. 技术选型与开源包

- kubebuilder/controller-gen 标记惯例（validation、default、printcolumn、subresource），生成 deepcopy 与 `crds/` manifests。
- 依赖仅 `k8s.io/apimachinery` 元类型（metav1 等），版本以 `deploy/versions.md` 钉死为准。
- 每个 CRD 一个 types 文件，manifest 由生成产出、不手改。

## 4. 具体设计（不写代码）

### 4.1 ChangeRequest（§5.1，闸门核心对象）

spec 字段逐组说明：

| 字段 | 语义 | 校验要点 |
| --- | --- | --- |
| `spec.sessionRef` | 关联决策会话，审计回溯锚点 | 必填 |
| `spec.clusterRef` | 目标集群；本期恒为默认集群，多集群接缝预留（N6，避免升格时重写契约） | 必填，带默认 |
| `spec.idempotencyKey` | I10/F15 幂等键，CR 名由其哈希派生，重试折叠去重 | 必填 + 格式约束；缺失则写意图连 CR 都建不出来 |
| `spec.intent` | 自然语言意图描述（自由文本，只存不传，进 LLM 上下文前必经 sanitize） | 长度上限 |
| `spec.source` | agent / human——AI 变更与人类变更过同一套门禁的标识 | 枚举 |
| `spec.operations[]` | 操作列表，每组含：`tool`（工具名）、`args`（自由参数，只存不传）、`riskLevel`（risk-classifier 终判，非 LLM 自评）、`authorityCheck`（I9：`fieldOwner` 如 hpa/payments，非空则必须走 owner API 或拒绝；`decision`=clear/via-owner-api/rejected）、`estBlastRadius`（pods/nodes/namespaces 估算，I7 依据）、`dryRunResult`（passed/failed/skipped）、`inverseOperation`（I5 必填，生成失败自动升级）、`inverseVerified`（逆操作 dry-run 验证结果）、`timeout`（单操作执行超时） | riskLevel 枚举 R0–R3；inverseOperation 必填 |
| `spec.approval` | 审批槽：`required`（R2 强制 true）、`approver`、`decidedAt`、`channel`（cli/webhook） | required 与 riskLevel 一致性由服务端状态机保证，不在类型层表达 |
| `spec.guardrailPolicyRef` | 绑定的 GuardrailPolicy（如 default） | 必填 |

status 字段逐组说明：

| 字段 | 语义 |
| --- | --- |
| `status.state` | 状态机：Pending→DryRunning→AwaitingApproval→Executing→Verifying→Committed \| RolledBack \| Aborted \| Rejected（逐字照录 §5.1） |
| `status.abort` | 中止语义：可随时由人工/熔断器置位——`requested`、`reason` |
| `status.executedOps` | 已执行操作计数，在途恢复的核对锚 |
| `status.verification` | 执行后验证（verifier）：`checks[]`（name/passed）、`healthy`；观察窗口 ≥ 相关控制器稳定窗口（F12） |
| `status.rollback` | I5 回滚语义：`deadline`（超过视为需人工介入而非自动回滚）、`attempted`、`result` |
| `status.auditRef` | DecisionRecord 引用，I6 留痕回指 |

### 4.2 GuardrailPolicy（§5.2，可评审的护栏 YAML）

| 字段 | 语义 | 关联 |
| --- | --- | --- |
| `spec.clusterSelector` | 策略适用集群范围；本期为空=默认集群，接缝预留 | N6 |
| `spec.hardDeny[]` | R3 硬禁止清单（I1 代码级强制）：verbs×resources 组合（如 delete namespaces）、secrets 明文读取、kube-system/gatekeeper-system 命名空间、clusterroles/clusterrolebindings 全域 | 类型层只管结构，强制在 `internal/policy` |
| `spec.blastRadiusQuota` | I7 爆炸半径硬上限：maxPodsPerChange / maxNodesPerChange / maxNamespacesPerChange | 数值下限 1 |
| `spec.rateLimit` | maxChangesPerHour / maxReadQPS（F5 的 apiserver 保护） | 正值 |
| `spec.sessionBudget` | R4 整改（F4）：maxToolCalls / maxLLMTokens，执行点在 `internal/session` | 正值 |
| `spec.circuitBreaker` | 熔断：failureRateThreshold（默认 0.3）/ window（30m）/ action（downgrade_to_readonly） | action 枚举 |
| `spec.opaPolicyBundle` | Rego 策略包路径（指向 `policies/`） | 必填 |

### 4.3 DecisionRecord（§5.3，审计对象）

| 字段 | 语义 |
| --- | --- |
| `spec.sessionRef` | 关联会话 |
| `spec.clusterSnapshotRef` | 输入状态快照（对象清单哈希），回放输入 |
| `spec.llmTraceRef` | Langfuse 调用链引用 |
| `spec.toolCalls[]` | 全量工具调用序列（I6） |
| `spec.sanitizationEvents[]` | 注入命中/消毒记录（I4/F2 的落点） |
| `spec.changeRequests[]` | 关联 CR 列表 |
| `spec.outcome` | 结果（如 committed） |
| `spec.retentionDays` | 审计保留期（365） |

设计约束：不可变——消费方（`internal/audit` 与回放）按"创建后不改"使用；更新策略（Finalizer/保留期清理）由 audit 上下文实现，类型层只留字段。

### 4.4 SchedulingHint（§5.4，M4 占位）

`spec.targetRef` + `spec.hints[]`（affinity/priority/profile 建议）+ `spec.evidence`（诊断结论引用）+ `status.adopted`（controller 采纳与否）。本期仅留契约位：hint 是建议而非指令、调度器可拒绝（I2/N2 的契约表达）；M4 决策门 no-go 则本类型不进入实现排期。

### 4.5 状态子资源与 finalizer 设计

- 全部 CRD 启用 status 子资源（spec/status 分离，/status 独立鉴权面）。
- finalizer 仅 ChangeRequest 需要：在途态（Executing/Verifying）拦截删除，走恢复逻辑后摘 finalizer，保证清理不悬挂（I10-3，分支逻辑见 `internal/controller/README.md`）；其余 CRD 用标准级联删除。
- `status.auditRef` 与 DecisionRecord 的互相引用构成审计回环（I6）。

### 4.6 printer columns 规划（kubectl get 可读性）

| CRD | 规划列 |
| --- | --- |
| ChangeRequest | NAME / STATE / RISK-LEVEL（取首个 operation 的 riskLevel）/ SOURCE / APPROVAL / EXECUTED-OPS / AGE |
| GuardrailPolicy | NAME / MAX-PODS / MAX-CHANGES-HOUR / BREAKER-ACTION / AGE |
| DecisionRecord | NAME / SESSION / OUTCOME / CHANGE-REQUESTS（数）/ AGE |
| SchedulingHint | NAME / TARGET / ADOPTED / AGE（M4 才生效） |

## 5. 错误处理与失效语义

本层无运行时进程，失效语义 = apiserver 第一道的拒绝语义：

| 错误类别 | 检测手段 | 处置策略 | 姿态 |
| --- | --- | --- | --- |
| 幂等键缺失/非法（F15 前置） | `idempotencyKey` 必填 + 格式 validation | apiserver 拒绝创建 | fail-closed |
| 非法 state/枚举值 | state 与全部枚举字段的 validation 标记 | apiserver 拒绝写入；绕过 apiserver 的未知状态由 controller 白名单迁移表拒识并告警 | fail-closed 双保险 |
| 配额/预算数值越界 | blastRadiusQuota、rateLimit、sessionBudget 数值下限标记 | apiserver 拒绝 | fail-closed |
| status 被无鉴权方改写 | status 子资源分离 + RBAC；审批字段（approval）的推进只认服务端状态机写入（R1 整改） | 直写 status 伪造审批在鉴权与状态机两层都无效 | fail-closed |
| 契约漂移 | CI 校验生成物新鲜度 + 枚举全集对照设计文档 §5 | 漂移 PR 打回 | 机检 |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 本层不产生运行时遥测；它是全部审计与观测数据的 schema 事实源。
- 审计留痕的字段口径：`toolCalls`、`sanitizationEvents`、`changeRequests`、`clusterSnapshotRef`、`llmTraceRef` 的填充责任在 `internal/audit`；`status` 各子结构的推进责任在 `internal/controller`。
- kubectl 可读性本身是运维可观测面：printer columns 让在途 CR（Executing/Verifying 悬挂）肉眼可查，F14 的人工巡检入口。
- 契约变更审计走 Git 历史 + 独立 PR 纪律（`api/README.md` 第 4 节）。

## 7. 依赖方向与模块边界

谁调我：`cmd/*`、`internal/controller`、`internal/adapters/k8s`、gatekeeper 与 CLI 等全部消费者。

我调谁：仅 `k8s.io/apimachinery` 元类型。

禁止依赖谁：`internal/` 任何包、OPA、MCP SDK（契约层为最底端，详见 `api/README.md` 第 7 节）；本目录内禁止出现任何行为方法（含"自动计算级别/自动判定审批"之类），那是 `internal/` 对应上下文的财产。

## 8. 测试策略与红队用例

- 契约成对用例：每字段"合法值通过 / 非法值被拒"（envtest 或 apiserver dry-run 承载）；枚举全集与设计文档 §5 逐字对照（W10 碎片任务）。
- 幂等：同 idempotencyKey 两次创建，第二次被 AlreadyExists 折叠——此语义的消费测试在 controller 侧（F15），类型层保证键的必填约束成立。
- kind 冒烟：kind-smoke.yml 的 apply `crds/` 步骤保证 manifest 可被服务端接受。
- 红队用例：
  1. 枚举绕过：向 state/riskLevel/source 注入枚举外取值，apiserver 拒绝。
  2. 审批伪造：直接 patch `spec.approval`/`status.state` 伪造已批准，被状态机拒识（controller 侧断言）。
  3. 自由文本注入：`intent`/`args` 灌入指令型文本，验证其消费路径（LLM 上下文注入前）必经 `<untrusted_cluster_data>` 包裹（I4，消费方测试）。
  4. 超大对象：`toolCalls`/`sanitizationEvents` 无限追加逼近 etcd 对象上限（附录 C 留档项的契约侧防护——列表元素设合理上限，超限拒绝）。
