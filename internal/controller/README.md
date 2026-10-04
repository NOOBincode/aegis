# controller — 薄 reconcile 层（CRD ↔ 应用服务）

> 所属层：控制面 reconcile 层 ｜ 里程碑：M2（T2.1 骨架，T2.11 失效语义，T2.12 冷却期） ｜ 设计文档出处：§4.2.5、§5.1 ｜ 关联不变量：I10（在途恢复/fail-closed）、I6（状态留痕）、I2（无 LLM 调用） ｜ 关联失败模式：F14（闸门崩溃在途悬挂）、F15（重试双执行）、F12（验证窗口）

## 1. 设计初衷

reconcile 层是 ChangeRequest / GuardrailPolicy / SchedulingHint 三个 CRD 与 `internal/` 各应用服务之间的唯一桥梁。它存在的理由是设计文档 §3.2 铁律 3 与落地方案 R1 红队整改项的合流：审批状态机必须在服务端 controller 落地，CLI 仅为视图——若状态推进只存在于客户端，`--yes` 类参数或自写客户端即可绕过整个闸门。本层因此只做三件事：读 CR、调应用服务、写 status；一切业务规则（分级、策略、审批条件、回滚判定）都住在对应限界上下文里，reconcile 只负责"在哪个状态该调用哪个服务、结果该迁到哪个状态"。这一薄度同时是 I10 的前提：状态机外置在 CRD status 上，进程内存不持有不可恢复状态，重启后重入即恢复。

## 2. 职责与任务清单

职责：

1. Watch ChangeRequest / GuardrailPolicy（+ M4 的 SchedulingHint，未 go 不实现）事件并触发 reconcile。
2. 按 `status.state` 分发到对应分支，调用 `internal/risk`、`internal/policy`、`internal/approval`、`internal/rollback`、`internal/breaker`、`internal/authority`、`internal/session` 的 service。
3. 写回 status（含 `executedOps`、`verification`、`rollback`、`abort`）、维护 finalizer、更新 `status.auditRef`。
4. 在途恢复与超时巡检：重启/leader 切换后重入 Executing/Verifying，核对集群实际状态后续作/回滚/中止；Executing 超时未推进即告警（F14）。
5. leader election 保证单写者（设计文档 §4.2.5-1：多副本 standby）。

任务清单（落地方案 T 编号）：T2.1（CRD + controller 骨架 + 状态机 + finalizer + leader election）、T2.11（幂等与失效语义、Executing/Verifying 超时巡检、读路径降级标注）、T2.12（冷却期与验证窗口配置校验）。完成判据（T2.1）：手写 CR yaml 能被状态机完整走完（fake 执行器）；杀进程重启后 reconcile 正确重入（I10）。

## 3. 技术选型与开源包

- controller-runtime（reconcile 框架与 manager 生命周期），版本以 `deploy/versions.md` 钉死为准。
- client-go 仅允许出现在本层与 `internal/adapters/k8s`、`cmd/`（depguard 规则：controller-runtime 只允许出现在 internal/controller 与 cmd）。
- envtest（controller-runtime 自带测试环境）用于测试；fake client 仅限单测；kind 集成测试走 `make test-integration`。
- 不引入额外状态机库：状态机就是本层的一张迁移表 + 分支函数，保持可读、可人审（R-2）。
- 不做 webhook 形式的准入拦截：闸门的强制点在 CR 状态机与工具协议层，不依赖 admission webhook 的 failurePolicy 语义（附录 C 中 admission webhook 超时留档为规模化事项）。

## 4. 具体设计（不写代码）

薄 reconcile 纪律（每一步都不得膨胀）：

1. 读：从 cache/informer 取 CR 与相关 GuardrailPolicy，不做集群直查（读集群实际状态属应用服务/适配器职责，仅在恢复分支显式调用）。
2. 调：把 spec 翻译成对应用服务的入参语义（"本 CR 需要分级/求值/审批/执行/验证"），不内联任何业务 if-else。
3. 写：按服务返回写 status；写失败按冲突/错误分类决定是否 requeue。

ChangeRequest 状态机（照录设计文档 §5.1）：

`Pending → DryRunning → AwaitingApproval → Executing → Verifying → Committed | RolledBack | Aborted | Rejected`

状态迁移表：

| 当前态 | 触发条件 | 下一态 | reconcile 分支职责 |
| --- | --- | --- | --- |
| Pending | CR 创建、idempotencyKey 去重通过 | DryRunning | 落审计引用；调 `risk` 分级 + `policy` 求值 + `rollback` 逆操作生成与验证 + `authority` 所有权核查；R3 直接 Rejected |
| DryRunning | dry-run 全部 passed | AwaitingApproval（R2）/ Executing（R0/R1） | 汇总 dryRunResult、inverseVerified、authorityCheck 进 spec 回写；级别决定分支 |
| AwaitingApproval | 审批通过（approval-svc 服务端状态机） | Executing | 超时未审批→按策略置 Aborted 或留在原地并告警（不自动通过） |
| AwaitingApproval | 审批拒绝 | Rejected | 写拒绝原因，留痕 |
| Executing | 全部操作执行成功 | Verifying | 逐个操作经放行后的窄调用（写工具只接受 CR ID）；更新 executedOps；单操作超时按 spec.operations[].timeout 处置 |
| Executing | 任一操作失败 | Verifying（走回滚验证）/ RolledBack | 触发逆操作；执行窗口超时未推进→巡检告警（F14） |
| Verifying | 验证检查全部 passed 且 healthy | Committed | verifier 观察窗口 ≥ 相关控制器稳定窗口（F12），不足判"收敛中"延长观察 |
| Verifying | 验证失败且回滚成功 | RolledBack | 写 rollback.attempted/result；超过 rollback.deadline 视为需人工介入而非自动回滚 |
| Verifying | 验证失败且回滚不可行 | Aborted | 升级人工，附漂移 diff（F6） |
| 任意态 | abort.requested 被人工/熔断器置位 | Aborted | 安全点停止；已生效部分评估回滚 |

finalizer 与在途恢复（I10-3）：

1. CR 建置时加 finalizer，保证清理不悬挂：删除请求到达时若处于在途态，先走恢复逻辑再摘 finalizer。
2. 重启后重入 Executing/Verifying：核对集群实际状态（经 adapters 读真实对象），三分支处置——已生效→补验证；未生效→按策略续作；状态不明或已漂移→回滚或中止。
3. 恢复结论写回 status 并留痕，保证"恢复"本身可审计（I6）。

Executing 超时巡检（F14）：对 Executing/Verifying 态 CR 设推进超时（结合 spec.operations[].timeout），超时未推进即告警并进入恢复分支；巡检逻辑自身不依赖进程内存，只看 status 时间戳。

幂等（F15）：CR 名由 `idempotencyKey` 哈希派生；apiserver AlreadyExists 视为重复提交，直接返回既有 CR（网络重试折叠为同一 CR）；MCP 通道信封序号乱序在协议层拒绝。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态 |
| --- | --- | --- | --- |
| 闸门进程崩溃/宕机 | CR 状态机巡检（Executing 超时未推进） | 告警；重启后 reconcile 重入核对实际状态，续作/回滚/中止 | 恢复语义自明（F14/I10） |
| leader 切换 | leader election 租约变化 | standby 接管后按上条重入，单写者保证无并发推进 | 同上 |
| 与 apiserver 分区 | client-go 调用超时/错误分类 | 写路径阻塞并告警，绝不"先放行"；requeue 等待恢复 | fail-closed（I10） |
| 状态写冲突（resourceVersion 过期） | 写 status 返回冲突 | 不重试蛮写：放弃本次 reconcile，重新入队读取最新状态 | 放弃重入（§4.2.4-2 乐观并发的 reconcile 侧镜像） |
| 应用服务返回"策略拒绝" | policy/risk/breaker 的 deny/escalate 返回 | Rejected 或升级 R2，原因写 status 与审计 | fail-closed |
| 验证窗口不足 | verifier 窗口配置校验（≥控制器稳定窗口） | 判定"收敛中"而非失败，延长观察（F12） | 延长，不误判 |
| abort 置位与执行竞争 | abort.requested 与操作执行并发 | 安全点检查：未执行操作不再下发，已生效部分评估回滚 | 中止优先 |
| 重复提交 | idempotencyKey 派生 CR 名 + AlreadyExists | 返回同一 CR，不产生第二次执行 | 折叠（F15） |

读路径降级不经过本层的状态机语义，仅作为标注信息进 status/告警（读降级为只读直连并明确标注降级态，I10）。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志字段：`cr_name`、`session_ref`、`from_state`、`to_state`、`reconcile_reason`、`requeue_after_ms`、`error_class`、`recovery_action`（resume/rollback/abort）、`leader_id`。
- 指标：`aegis_reconcile_requeue_total{reason}`、`aegis_cr_total{result}`（committed/rolled_back/aborted/rejected）、`aegis_cr_state_duration_seconds{state}`、`aegis_recovery_reentry_total{action}`、`aegis_executing_timeout_total`、`aegis_finalizer_blocked_deletes_total`。
- Trace span 命名：`controller.reconcile`、`controller.state.dryrun`、`controller.state.execute`、`controller.state.verify`、`controller.recovery.reenter`、`controller.finalizer.cleanup`。
- 审计留痕点：每次状态迁移（from/to/原因/触发者 agent|human|breaker）、finalizer 摘除、在途恢复重入与恢复分支结论、超时巡检告警、幂等折叠命中（重复提交返回同一 CR 的事件）。

## 7. 依赖方向与模块边界

谁调我：无人（本层是叶子编排层；入口是 manager 的 watch 事件与 `cmd/controller` 的装配）。

我调谁：

- `internal/<context>/service`（risk/policy/approval/rollback/breaker/authority/session/audit），只做编排调用。
- `internal/adapters/k8s` 用于恢复分支的集群实际状态核对。

禁止依赖谁：

- 禁止 import 任何上下文包的 domain 层（铁律 4：经 service 协作）；禁止把分级/策略/回滚规则内联进 reconcile（铁律 3）。
- 禁止反向依赖 `mcp-servers/*` 与 `internal/mcpserver`（方向只能是工具进程向上调上下文，见 `internal/mcpserver/README.md`）。
- 本层文件出现在 depguard 的 L2 人审清单中（aegis-go-quality），AI 产出逐行人审，PR 标注 AI 产出占比 + 人审人。

## 8. 测试策略与红队用例

- envtest 为主：状态机全路径（含 Rejected/RolledBack/Aborted 三分支）、finalizer 生命周期、恢复重入。
- 单测（fake client 仅限单测）：迁移表逐行表驱动测试；幂等去重（同 idempotencyKey 两次提交得同一 CR）；验证窗口校验失败用例。
- kind 集成（`make test-integration`）：杀进程重启后 reconcile 正确重入（T2.1 完成判据）；Executing 超时巡检告警（F14）；乐观并发冲突无蛮写重试。
- 红队用例（M2 检查点 #2 的 controller 侧部分）：
  1. 审批绕过：自写客户端直接改 CR status 伪造 AwaitingApproval→Executing，必须被服务端状态机拒绝（单测证明，T2.4 完成判据）。
  2. 重放：复制既有 CR 的提交报文重发，只返回原 CR、不产生新执行。
  3. 在途悬挂：执行中杀 controller 进程，重启后按集群实际状态正确续作/回滚/中止，且无重复执行。
  4. abort 竞争：执行中置 abort.requested，验证未下发操作被取消、已生效部分有回滚评估。
- 覆盖率：本层属 L2 安全包，≥80%，对 gosec/errcheck 不设 nolint 例外（aegis-go-quality）。
