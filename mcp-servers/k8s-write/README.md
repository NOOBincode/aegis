# k8s-write — 写工具集：仅接受 ChangeRequest ID 的三个窄接口

> 所属层：L1 工具层 ｜ 里程碑：M2（落地方案 T2.8） ｜ 设计文档出处：§4.1、§4.2.4、§5.1 ｜ 关联不变量：I1、I3、I5、I7、I10 ｜ 关联失败模式：F6、F11、F14、F15

## 1. 设计初衷

k8s-write 是整个 L1 中安全责任最重的一个 server，它的设计目标可以用一句话概括：**让 Agent 直接触达写能力在协议层成为不可能**（设计文档 §4.1）。三个写工具不接受任何自由参数，只接受 ChangeRequest ID——参数在 CR 建立时已经过 risk-classifier 分级、opa-eval 策略校验、（按级别）approval-svc 审批、rollbacker 逆操作生成与 dry-run 验证。工具层拿到的不是"意图"，而是"已被闸门放行、处于 Executing 态的执行凭证"。

这个设计同时满足三条不变量：I1（放行由代码与策略决定，LLM 输出不经手工具层参数）、I3（写权限不体现在任何 Agent 可见的宽接口上）、I10（写路径 fail-closed，任何校验失败宁可不执行）。

## 2. 职责与任务清单

**职责**：按 ChangeRequest 中已固化的操作描述执行三个确定性写动作（scale/restart/cordon），执行前复核前置条件，执行时携带乐观并发保护，执行后把结构化结果交回闸门状态机。不做分级、不做审批、不做逆操作生成（全在 L2）。

**任务清单**：

| 任务 | 交付 | 出处 |
| --- | --- | --- |
| T2.8 | 3 个写工具，仅接受 ChangeRequest ID；乐观并发（resourceVersion/SSA 冲突检测，检出即放弃重新归因） | 落地方案 W15 |
| T2.1 | CR 状态机与 finalizer（本工具的调用前提） | 落地方案 W10–W11 |
| T2.5 | 逆操作生成与验证（本工具执行结果的回滚消费方） | 落地方案 W13 |
| T2.12 | 目标级冷却期、验证窗口校验的执行配合 | 落地方案 W15 |

## 3. 技术选型与开源包

- 出站写操作经 `internal/adapters/k8s` 端口（client-go 封装，携带 resourceVersion），版本以 `deploy/versions.md` 钉死为准；
- 共享框架 `internal/mcpserver`；本 server 不注册进 Agent 可见的工具白名单，仅挂到 gatekeeper 内部注册表；
- 执行结果回写 CR status 由 controller reconcile 完成（读 CR → 调应用服务 → 写 status，DDD 铁律 3），本工具只负责返回回写所需的结构化字段。

## 4. 具体设计（不写代码）

### 4.1 工具清单与元数据

| # | 工具 | 职责 | risk_hint | idempotent | reversible | est_blast_radius | timeout_ms（建议初值） |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | scale_deployment | 调整 Deployment replicas 至 CR 固化值 | R1（仅当步长 ≤±1；更大步长由 risk-classifier 判 R2） | true（经 idempotencyKey 折叠，F15） | true（逆操作=原 replicas） | `{pods: 步长值, nodes: 0, namespaces: 1}` | 30000 |
| 2 | restart_pod | 删除单个 Pod（由控制器重建） | R1 初判，reversible=false 触发 I5 自动升 R2（见 4.4） | true | false（"效果不可回滚"，I5 → 自动升 R2） | `{pods: 1, nodes: 0, namespaces: 1}` | 30000 |
| 3 | cordon_node | 封锁节点（禁止新调度） | R1（逆操作=uncordon，天然可逆） | true | true（逆操作=uncordon） | `{pods: 0, nodes: 1, namespaces: 0}` | 30000 |

### 4.2 入参协议（协议层强制，唯一入参）

- 入参：仅 `cr_id`（字符串，ChangeRequest 名，如 §5.1 `metadata.name: cr-20260917-0001`）。
- 协议层拒绝规则：schema 中除 `cr_id` 外不允许任何字段；携带自由参数（namespace/name/replicas 等）一律解析失败，注册 schema 本身就不给自由参数留位置——这不是运行时检查，是协议形状上的不可能（I3）。
- 附加信封：调用方须提供会话签名信封与序号（MCP 通道 mTLS + 签名信封 + 会话内序号，§4.2.5），序号乱序拒绝（F15）。

### 4.3 执行前置校验（编号步骤，任一失败即拒绝执行，fail-closed）

1. 读取 CR：`cr_id` 存在且 `spec.operations[].tool` 与本工具名精确一致；
2. 状态复核：`status.state` 必须处于 `Executing` 态——`Committed` 路径中的执行态。其他任何状态（Pending/DryRunning/AwaitingApproval/Verifying/终态）一律拒绝；
3. 中止位复核：`status.abort.requested=true` 时拒绝执行并返回中止语义（§5.1 abort 字段）；
4. 参数复用：执行参数（namespace/name/replicas）只取自 CR `spec.operations[].args`，工具内部无任何参数改写逻辑；
5. 爆炸半径复核：执行前按 I7 再核一次目标对象当前规模与 `estBlastRadius` 一致性，漂移超限拒绝（GuardrailPolicy `blastRadiusQuota` 执行点）；
6. 冷却期复核：目标对象在冷却期内拒绝（§4.2.4-4，T2.12）。

### 4.4 关键语义说明

- **restart_pod 的升级路径**：§4.2.1 将"单 Pod restart"列入 R1 定义，但 rollbacker 规则（§4.2.2）明确 restart 的逆操作=无、标记"效果不可回滚"→ 按 I5 自动升 R2（人审）。因此本工具的常态执行路径是"CR 已经过人审"，`reversible=false` 的元数据正是触发该升级的开关。
- **CR 状态机**（§5.1，工具只参与其中 Executing 一段）：

| 当前状态 | 触发事件 | 下一状态 | 本工具角色 |
| --- | --- | --- | --- |
| Pending | 提交入库 | DryRunning | 不参与 |
| DryRunning | dry-run 通过 | AwaitingApproval（R2）/ Executing（R0-R1） | 不参与 |
| AwaitingApproval | 审批通过 | Executing | 不参与 |
| Executing | gatekeeper 携带 cr_id 调用 | 执行中 | 被调用，前置校验全过才动手 |
| Executing | 执行成功返回 | Verifying | 返回结构化执行结果 |
| Executing | 前置校验失败/执行失败 | Aborted（或按策略回滚） | 返回失败语义，不自行重试 |
| Verifying | 验证通过/失败 | Committed / RolledBack | 不参与 |
| 任意 | abort 置位 / 终态进入 | Aborted / Rejected | 拒绝执行 |

- **乐观并发**：写请求携带工具读到的 `resourceVersion`；apiserver 返回冲突（或 SSA field-manager 冲突）时**检出即放弃**，返回 conflict 语义给 gatekeeper，由其重新归因（§4.2.4-2），工具层绝不蛮写重试。
- **执行结果回写**：工具返回结构含 `op_index`、`executed_at`、`observed_resource_version`、`observed_state`（执行后对象状态摘要）、`result`（success/conflict/precondition_failed/timeout）、`error_class`；controller 据此写回 `status.executedOps` 与 `status.verification` 的输入侧。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级 | 对应 |
| --- | --- | --- | --- | --- |
| 自由参数注入（E-SCHEMA） | 协议 schema 形状 + 解析层 | 解析失败，调用不成立 | fail-closed | I3 |
| CR 不存在 / 工具名不匹配 | 步骤 1 | 拒绝执行，返回 not_found / mismatch | fail-closed | F15（防张冠李戴执行） |
| CR 状态非法（E-CRSTATE） | 步骤 2 状态复核 | 拒绝执行，返回当前状态语义 | fail-closed（状态机外的执行=越权） | I1、F14 |
| 中止位已置位 | 步骤 3 | 拒绝执行 | fail-closed | §5.1 abort |
| 爆炸半径漂移（E-QUOTA） | 步骤 5 复核对 | 拒绝执行，返回漂移说明 | fail-closed | I7 |
| resourceVersion / SSA 冲突（E-CONFLICT） | apiserver 409 与 field-manager 冲突 | 放弃执行，返回 conflict + 观察到的 rv，交 gatekeeper 重新归因 | fail-closed，不重试 | §4.2.4-2、F11 |
| apiserver 超时/分区（E-UPSTREAM） | 超时器 | 返回 timeout，**不自行重试**；CR 保持 Executing 由 F14 超时巡检发现后按策略续作/回滚/中止 | fail-closed（超时降级放行是一票否决 bug） | I10、F14 |
| 执行后验证输入缺失 | 返回结构缺字段 | controller 拒绝推进状态机 | fail-closed | I6 |

**失效姿态**：本层写路径无任何降级概念——闸门不可用、校验不过、上游超时，结局只有一个：不执行。进程无状态，重启后靠 CR 状态机 + finalizer 恢复在途变更（I10）。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- **日志字段**：L1 统一字段集 + `cr_name`、`idempotency_key`、`op_index`、`tool`、`cr_state_before`、`resource_version_expected`、`resource_version_observed`、`conflict_type`、`result`。不落执行参数明文以外的敏感信息。
- **指标**：`aegis_write_exec_total{tool, result}`（result ∈ success / conflict / cr_state_invalid / precondition_failed / quota_drift / timeout）；`aegis_write_conflict_total{tool}`；`aegis_write_duration_seconds{tool}`。熔断器的输入信号（R1 执行失败率）取自 `aegis_write_exec_total` 的失败桶（circuit-breaker 在 L2，本层供给数据）。
- **Trace span**：`l1.k8swrite.<tool>`，子 span `.precheck`（前置校验链）/ `.upstream`（带 rv 的写调用）/ `.conflict`（冲突放弃路径单独留 span，归因流水线可检索）。
- **审计留痕点**：执行事件进 DecisionRecord `spec.toolCalls` 与关联 ChangeRequest 的 `status` 全量字段（§5.1），`auditRef` 回链。每一次前置校验拒绝都是独立审计事件（拒绝也是留痕）。

## 7. 依赖方向与模块边界

- **谁调我**：仅 L2 gatekeeper 在 CR `Executing` 阶段调用。Agent 运行时的工具白名单中不存在本 server（MCP 代理模式下运行时无法绕过，设计文档 §3.1）。
- **我调谁**：`internal/mcpserver` 框架、`internal/adapters/k8s` 出站端口；执行结果交回给调用方 gatekeeper（由 controller reconcile 写 status）。
- **禁止依赖谁**：禁止 import L2 判定组件做二次放行（前置校验只做"执行可行性"核对，不做"是否放行"判断——放行的唯一证据是 CR 状态本身）；禁止直连 `internal/authority`（所有权判断已在 CR `authorityCheck` 固化）；与 k8s-read 零互调。

## 8. 测试策略与红队用例

- **单测**：前置校验链逐项负例（每步造一个失败输入）；状态机迁移表全路径。
- **集成测试**：kind 实集群走通 三工具成功路径；`aegis-go-quality` 要求 L2 安全包 ≥80% 覆盖——本层虽非 L2 包，写路径用例密度按同级对待。
- **红队用例**（对应红队检查点 #2 与写工具准入）：
  1. 参数注入：构造携带 namespace/replicas 自由参数的调用，验证协议层拒绝（I3）；
  2. 越权目标：篡改 CR `operations[].tool` 与工具名不匹配，验证拒绝；伪造 `status.state=Executing`（非闸门写入），验证工具复核不认（与 T2.4 服务端状态机配合）；
  3. 幂等重放：复制签名信封重发、序号乱序、同一 idempotencyKey 双提交，验证折叠为同一 CR / 拒绝（F15）；
  4. 并发冲突：执行前第三方修改目标对象，验证 409 放弃 + 无重试 + conflict span 落归因（F11、§4.2.4-2）；
  5. 状态悬挂：杀 gatekeeper 进程制造 Executing 悬挂，验证 F14 巡检告警与恢复语义（配合 T2.11）。
