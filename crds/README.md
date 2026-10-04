# crds — CRD manifests（K8s 契约层）

> 所属层：控制面契约层（`api/v1alpha1` 的部署产物）｜ 里程碑：M1（DecisionRecord，T1.8）、M2（ChangeRequest/GuardrailPolicy，T2.1）、M4（SchedulingHint 仅契约位）｜ 设计文档出处：§5 ｜ 关联不变量：I5、I6、I10 ｜ 关联失败模式：F14、F15

## 1. 设计初衷

CRD 是闸门语义的 K8s 载体：ChangeRequest 状态机落盘使 gatekeeper 进程无状态（I10，设计文档 §4.2.5），DecisionRecord 落盘使决策可回放（I6）。本目录的初衷是把"契约"从代码中显式化：人类评审 YAML 契约，机器消费契约，契约永远先于消费者。第一版即全量字段——abort/timeout/rollback.deadline/idempotencyKey 不留"以后再加"（§5 首行，红队记录 R6 整改）。

## 2. 职责与任务清单

| # | 职责 | 交付物 | 对应任务 |
| --- | --- | --- | --- |
| 1 | 四个 CRD manifest 维护 | 本目录生成物 | T1.8、T2.1 |
| 2 | 生成链治理 | api/v1alpha1 → controller-gen → crds/ 的纪律 | T2.1 |
| 3 | 安装/升级顺序编排 | §4.3 | T2.1 |
| 4 | conversion webhook 预留 | §4.5 | 接缝 |
| 5 | CI 一致性校验 | generate 无 diff 门禁 | T0.1（CI 骨架） |

## 3. 技术选型与开源包

- kubebuilder 惯例（设计文档 §3.2：`api/v1alpha1` 为 K8s 契约层）；controller-gen 作为生成器（controller-gen 版本随 controller-runtime 对账，见 `deploy/versions.md` 纪律 5）。
- 安装消费工具：kubectl/kustomize，版本以 `deploy/versions.md` 钉死为准。

## 4. 具体设计（不写代码）

### 4.1 四个 CRD 定位

| CRD | 一句话定位 | 设计文档出处 | 引入里程碑 |
| --- | --- | --- | --- |
| ChangeRequest | 闸门核心对象：一次写操作的全生命周期载体，幂等键派生 CR 名 | §5.1 | M2（T2.1） |
| GuardrailPolicy | 可评审的护栏策略 YAML：硬拒清单/爆炸半径配额/限流/会话预算/熔断参数 | §5.2 | M2（T2.1） |
| DecisionRecord | 审计对象：输入快照/工具序列/消毒事件/结果，落盘不可变，回放载体 | §5.3 | M1（T1.8） |
| SchedulingHint | 调度建议契约位：hint 是建议而非指令，调度器可拒绝 | §5.4 | M4（仅契约位，决策门控制） |

### 4.2 类型与 manifest 的同步纪律

- `api/v1alpha1` 的 Go 类型是唯一事实源；本目录 manifest 由 controller-gen 生成。
- 禁止手改 manifest：任何契约变更先改类型与 kubebuilder 标注，再重新生成。
- CI 校验生成物无 diff（一致性门禁，随 kind-smoke 同源执行）；字段语义对照设计文档 §5.1/§5.2 逐字段核对（T2.1 的 W10 碎片任务）。

### 4.3 安装/升级顺序

安装（编号步骤）：

1. apply `crds/`（契约先行）。
2. controller（RBAC + reconcile 层）。
3. gatekeeper。
4. GuardrailPolicy default 实例 + Rego bundle（`policies/`）。
5. kagent/agent 层（`guardrailPolicyRef` 绑定，§4.3）。

升级：先应用新版 CRD schema（字段只增不改语义），再升级 controller；不支持删除字段的降级，破坏性变更走 conversion（§4.5）。理由：契约先于消费者；controller 先于其管理的 CR 实例承载写流量。

### 4.4 ChangeRequest 状态机（状态迁移表）

| 当前态 | 事件 | 下一态 | 说明 |
| --- | --- | --- | --- |
| Pending | dry-run 通过 | DryRunning |  |
| Pending | dry-run 失败或被策略拒 | Rejected | 留痕（F1） |
| DryRunning | 需要审批 | AwaitingApproval | R2 强制人审 |
| DryRunning | 无需审批 | Executing | R0/R1 |
| AwaitingApproval | 批准 | Executing | 审批状态机在服务端 controller（红队记录 R1 整改） |
| AwaitingApproval | 拒绝 | Rejected | 留痕 |
| Executing | 执行完成 | Verifying |  |
| Executing/Verifying | abort 置位或熔断触发 | Aborted | 中止语义：随时可人工/熔断器置位（§5.1 status.abort） |
| Verifying | 验证健康 | Committed |  |
| Verifying | 验证失败且逆操作复验通过 | RolledBack | 回滚前漂移复验（F6） |

终态四态：Committed/RolledBack/Aborted/Rejected。在途态（Executing/Verifying）超时未推进由 controller 巡检告警（F14）；重启后 reconcile 重入核对集群实际状态，已生效补验证、未生效按策略续作/回滚/中止（I10，§4.2.5-3）。finalizer 保证清理不悬挂。

### 4.5 conversion webhook 预留

本期单版本（v1alpha1）不启用 conversion；目录结构与 Makefile 目标预留。触发条件：出现破坏性字段语义变更需引入 v1beta1 时启用。可用性约束：conversion webhook 故障 = apiserver 无法服务该 CR 的读写，属 fail-closed 面（与 I10 同源）；启用时必须多副本部署，webhook 超时与失败策略在评审中明示。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| manifest 与类型漂移 | CI generate diff 校验 | 打回 PR，要求改类型后重新生成 | fail-closed：契约漂移 = 双事实源 |
| schema 校验拒绝（缺必填如 idempotencyKey/inverseOperation） | apiserver 准入 | 拒绝创建，修正后重提 | fail-closed（I10）：字段必填是 I5/F15 的契约底座 |
| finalizer 悬挂（F14） | deletionTimestamp 非空且 finalizers 非空超时 | controller 重入核对集群实际状态后续作/回滚/中止；人工强删仅经 CLI 显式操作并留痕 | 恢复优先；人工介入必须留痕（F7 同口径） |
| 进程重启在途 CR | reconcile 重入（I10） | 核对实际状态：已生效补验证，未生效按策略续作/回滚/中止 | 在途恢复是机制而非告警 |
| 重复提交同一意图（F15） | idempotencyKey 派生 CR 名 | 重复提交返回同一 CR，不产生第二次执行 | 折叠去重，调用方重试安全 |
| 大对象撞 etcd 上限 | 对象大小巡检 | 本期 N3 不处理；DecisionRecord 全量留痕的规模化拆分按设计文档附录 C 留档口径 | 记录为接缝，不静默膨胀 |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- **结构化日志字段**：ts、level、controller、cr_name、crd_kind、from_state、to_state、reason、attempt、duration_ms。
- **指标**：`aegis_cr_total{risk_level, result}`（状态机终态计数，CR 通过率来源）、`aegis_reconcile_stuck_total`（F14 在途巡检命中）、`aegis_audit_record_total`（DecisionRecord 落盘计数）。
- **Trace span**：controller.reconcile（属性含 cr_name/from_state/to_state）。
- **审计留痕点**：DecisionRecord 不可变存储 + `status.auditRef` 互引（I6）；状态迁移全量写入 status；审批决定（approver/decidedAt/channel）写回 §5.1 approval 字段；人工介入（强删/复位）经 CLI 并留痕。

## 7. 依赖方向与模块边界

- **谁调我**：kubectl/kustomize（make up 与 CI kind-smoke 的 apply 步骤）；apiserver（运行时消费契约）。
- **我调谁**：无——本目录是静态清单；生成链上游是 `api/v1alpha1` 类型。
- **禁止依赖谁**：manifest 不内嵌业务代码、不引用 `internal/`；禁止手改生成物；`internal/controller` 经 scheme 消费类型，但本目录不反向 import 任何 Go 包（生成链单向：api → crds）；apimachinery/metav1 版本与版本表的 K8s 版本对账。

## 8. 测试策略与红队用例

- **一致性**：CI 校验 generate 无 diff；§5.1/§5.2 逐字段核对（T2.1 碎片任务）。
- **冒烟**：kind-smoke.yml 的 apply crds/ + controller 状态机冒烟（aegis-ci）；reconcile 层用 envtest（aegis-go-quality）。
- **在途恢复测试**：杀进程重启后 reconcile 正确重入（T2.1 完成判据，F14/I10）。
- **红队用例**：自写客户端伪造 status 绕过审批（T2.4 单测，红队检查点 #2 项）；幂等重放双执行（F15，T2.11）；R3 操作变体越权（演示③底层，T2.3）。CRD 层不直接裁决攻击，但契约字段（authorityCheck/dryRunResult/inverseVerified）是闸门裁决的证据底座。
