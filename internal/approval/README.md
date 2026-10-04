# approval — 服务端审批流状态机（approval-svc）

> 所属层：L2 安全闸门（控制面） ｜ 里程碑：M2（落地方案 T2.4） ｜ 设计文档出处：§4.2.1（R2 处置流）、§5.1（approval 字段与状态机）、红队记录 R1 ｜ 关联不变量：I6、I10 ｜ 关联失败模式：F8

## 1. 设计初衷

红队记录 R1（P0）指出初版设计的致命伤：审批流若只在 CLI 客户端实现，可被 `--yes` 类参数或自写客户端绕过。整改结论（已回写 §5.1）：审批状态机移到服务端 controller，CLI 仅为视图。本上下文就是这个"服务端"的语义内核：AwaitingApproval 是 ChangeRequest 状态机的法定一态（§5.1），任何审批结论必须由服务端校验身份与状态后写入 CR status，Agent 与 CLI 都无法伪造。

第二个初衷是审批质量：R2 人审不是点个头，而是基于"诊断链 + 影响面报告"的知情决策（§4.2.1）。第三个初衷是审批自身的度量：F8 审批疲劳（通过率/耗时监控）要求审批流从第一天就把决策数据结构化留痕，反哺 risk 分级调优（`internal/risk/README.md`），并经 eval 验证 R1 扩边界的安全性（M3）。

## 2. 职责与任务清单

1. 实现服务端审批子状态机：Requested → Approved / Rejected / Expired / Cancelled（终态封闭），嵌在 CR 状态机的 AwaitingApproval 态内。
2. 校验审批人身份：只有通过 Kubernetes 认证与授权校验的审批人提交的结论才被接受；CR status（含 `approval.approver`、`approval.decidedAt`、状态字段）只能由 controller 写。
3. 组装审批包：诊断链（DecisionRecord 引用）+ 影响面报告（estBlastRadius、dry-run 结果、逆操作与验证结果、OPA 结论）。
4. 提供审批动作入口：`aegis-cli approve/deny`（视图）与 webhook 推送通道（可后置降级项，落地方案 §10）。
5. 处置 AwaitingApproval 超时：过期即视为未获批，进入中止链路。
6. 产出审批疲劳指标（F8）：审批量、通过率、耗时、驳回分布。
7. 支撑红队 R1 验收：自写客户端直接改 CR status 无法伪造审批（T2.4 完成判据）。

## 3. 技术选型与开源包

- 状态机内核在 domain：纯 Go、零 k8s 依赖，状态迁移可单测穷举。
- 状态持久化：审批结论写 CR（§5.1 `spec.approval` 与 `status.state`），无进程内存态（I10）；gatekeeper 多副本 standby + leader election 单写者（§4.2.5）。
- 身份与权限：复用 Kubernetes 认证与 RBAC——controller 以其 ServiceAccount 写 status，普通用户与 Agent SA 无 status 写权限；审批人身份取自调用方凭证。
- webhook 通道：实现细节后置（落地方案 §10 允许降级为仅 CLI）；本上下文只定义"通知通道"端口，投递失败不影响状态机。
- 领域层零外部依赖；涉及的组件版本以 `deploy/versions.md` 钉死为准。

## 4. 具体设计（不写代码）

### 4.1 审批子状态机（服务端）

| 当前状态 | 触发事件 | 守卫条件 | 下一状态 | 副作用 |
| --- | --- | --- | --- | --- |
| Requested | approve | 审批人身份合法且 CR 仍处 AwaitingApproval | Approved | 写 `approval.approver`/`decidedAt`；CR 推进 Executing；留痕 |
| Requested | deny | 审批人身份合法且 CR 仍处 AwaitingApproval（与 approve 行同守卫） | Rejected | CR 终态 Rejected；留痕 |
| Requested | 超时 | 超过审批时限 | Expired | 绝不自动通过；按策略 CR 转 Aborted（reason=approval_timeout）或留在原地并告警；通知申请人；留痕 |
| Requested | abort 置位 | `status.abort.requested=true`（人工/熔断器，§5.1） | Cancelled | CR 转 Aborted；留痕 |
| Approved / Rejected / Expired / Cancelled | 任意 | — | 原状态不变 | 非法迁移尝试记审计事件并告警 |

- 终态封闭：四个终态均不可再迁移；对已离开 AwaitingApproval 的 CR 送达的审批一律拒绝并提示状态已变——防"审批了不该审批的东西"。
- 超时语义：超时未审批绝不自动通过；过期后按策略转 Aborted 或留在原地并告警（与 `internal/controller/README.md` 的编排口径一致）。进入 Aborted 时不动集群；同一 `idempotencyKey` 重投返回同一 CR（F15），是否以新意图新建由人决定。

### 4.2 审批人身份来源与不可伪造性

1. 身份来源：审批动作经 API 提交时携带调用方 Kubernetes 凭证，controller 完成认证与审批授权校验后才执行迁移。
2. 写权限独占：`status`（含 `approval.*`、`state`）只允许 controller 的 ServiceAccount 写；任何用户或 Agent 直接改 status 的请求被 RBAC 拒绝——这是"自写客户端无法伪造审批"的机制基础（T2.4 单测证明）。
3. 不可伪造性三支柱：RBAC 写权限独占 + 状态机守卫（只在 Requested 态接受审批）+ 每次审批动作全量留痕（含非法尝试，I6）。
4. CLI 定位：`aegis-cli approve/deny` 只是上述 API 的视图与交互壳，不持有任何审批逻辑（红队 R1 整改落点）。

### 4.3 审批包组装（知情决策）

审批推送（CLI 视图或 webhook 通知）必须附带：

1. 诊断链：DecisionRecord 引用（`sessionRef`、`llmTraceRef`、工具调用序列，§5.3），审批人可回放"AI 为什么提议这次变更"。
2. 影响面报告：estBlastRadius（pods/nodes/namespaces）、`dryRunResult`、逆操作方案与 `inverseVerified`、OPA 求值结论与违例清单。
3. 元信息：`intent`（§5.1）、来源（`source: agent|human`）、审批时限。

### 4.4 通道与降级

- CLI 通道为本期主通道；webhook 推送是可后置降级项（落地方案 §10：T2.10 webhook 审批通道可降级为仅 CLI，三连演示与红队检查点不因此削减）。
- webhook 只影响"通知送达"，不影响状态机：投递失败 → 记降级事件，审批人仍可通过 CLI 拉取待审批列表完成决策。
- 通知层经端口抽象，状态机内核不感知通道实现。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| 身份校验失败（无凭证/无效/无审批授权） | controller 认证与 RBAC | 拒绝迁移，记 `invalid_attempt`（含 attempted_approver），告警 | fail-closed：身份不明 = 未获批（红队 R1 防线） |
| 非法状态迁移（终态后再审批、非 Requested 态收审批） | 状态机守卫 | 拒绝 + 审计事件 + 告警 | fail-closed：状态机封闭性即审批完整性 |
| 审批送达时状态已变（stale：并发 abort/超时先到） | 守卫比对 CR 当前状态 | 拒绝并提示最新状态；竞争只可能有一个生效 | fail-closed：先到的合法事件赢，后到的绝不补票 |
| AwaitingApproval 超时 | 时限巡检 | Expired → 按策略转 Aborted 或留在原地并告警，通知申请人，留痕；绝不自动通过 | 默认不批准：超时即未获批，宁可不做不可错做 |
| webhook 投递失败 | 通知端口错误返回 | 降级仅 CLI，记 degraded 事件 | 降级不影响安全语义：通知是便利，审批是状态机 |
| 进程故障/leader 切换 | F14 巡检、reconcile 重入 | 状态全在 CRD，恢复后仍处 AwaitingApproval，审批可继续 | 无内存不可恢复态（I10）；不丢审批上下文 |

整体原则：审批语义的失效姿态是"默认不批准"。身份不明、状态不明、通道故障的任何一种，结论都是不进入 Executing——与 I10 写路径 fail-closed 一致。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志（事件 `approval_event`）：`cr_name`、`action`（approve/deny/expire/cancel）、`approver`、`decided_at`、`prev_state`、`validation_result`、`stale_reject`、`channel`（cli/webhook）。
- 指标：
  - `aegis_approval_total{result}`：审批结果计数；
  - `aegis_approval_pending_duration_seconds`（histogram）：AwaitingApproval 驻留时长——F8"耗时"口径；
  - `aegis_approval_expired_total`、`aegis_approval_reject_total`：过期/驳回计数——F8"通过率"口径；
  - `aegis_approval_invalid_attempt_total`：身份或状态非法尝试计数——红队监测哨兵，突增即告警。
- Trace：span `gatekeeper.approval.decide`；通知通道子 span `approval.notify`（webhook 路径）。
- 审计留痕点：审批结论写 CR `spec.approval`（approver/decidedAt/channel）与 `status.state`（§5.1）；非法尝试与降级事件全部入 DecisionRecord 链路（I6）；F8 汇总指标定期反哺 risk 分级规则评审（`internal/risk/README.md`）。

## 7. 依赖方向与模块边界

- 谁调我：`internal/controller` 的 reconcile（状态机执行者与唯一 status 写者）与 `cmd/gatekeeper` 编排；`cmd/aegis-cli` 仅作为视图经 API 触发审批动作。
- 我调谁：不直接调其他上下文 domain。审批包所需的 DecisionRecord 引用与影响面字段由编排层以值对象传入；审批结论的审计落盘由 audit 上下文经 service 协作完成（铁律 4：跨上下文经 service，只共享值对象级数据）。
- 禁止依赖谁：`k8s.io/*`、`sigs.k8s.io/*`、webhook/MCP 实现包、`internal/adapters/*`、其他上下文 domain——depguard 强制（见 `.agents/skills/aegis-ddd-layout/SKILL.md`）。
- 边界红线：CLI 不得内嵌审批逻辑或状态副本（红队 R1）；状态机不得为通道便利开旁路（webhook 直写状态之类一律禁止）。

## 8. 测试策略与红队用例

- 状态机穷举单测：五状态迁移矩阵全覆盖，含全部非法迁移的拒绝断言；守卫条件（身份/状态）逐项验证。
- 不可伪造单测：模拟自写客户端直接改 CR status → 被 RBAC/守卫拒绝，审批状态不变（T2.4 完成判据）。
- 竞态单测：并发 approve + abort、approve + 超时到达，断言恰好一个生效、另一个记 stale 拒绝。
- 超时测试：模拟时限推进 → Expired → Aborted，申请人收到通知事件。
- 红队用例（M2 检查点，项目准入门槛）：审批绕过专项——`--yes` 类参数、自写客户端、重放旧审批、跨 CR 复用审批结论，全部必须失败且留痕；审批包完整性（审批人可见诊断链与影响面报告，I6）。
- 覆盖率 ≥80%（L2 安全包，落地方案 R-2 全人审范围）。
