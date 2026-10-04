# cmd/controller — CRD controller 进程

> 所属层：控制面 reconcile 层 ｜ 里程碑：M1（DecisionRecord 契约落盘）/ M2（ChangeRequest、GuardrailPolicy 全量状态机）/ M4 可选（SchedulingHint，决策门 go 之后才注册 reconcile 分支）｜ 设计文档出处：§3.2、§4.2.5、§5 ｜ 关联不变量：I6、I9、I10 ｜ 关联失败模式：F11、F12、F14

## 1. 设计初衷

controller 是 aegis 全部 CR 状态机的唯一推进者。红队 R1 整改项（设计文档 §10）把审批状态机从"CLI 客户端实现"移到服务端，本进程就是该整改的落地位置：状态迁移只能由 reconcile 写回 status，任何客户端（包括 aegis-cli、自写脚本、Agent 自身）直接改 status 都不能伪造审批。第二个初衷是 I10 的在途恢复：闸门与 controller 都可能死（F14），状态全落 CRD status 后，本进程重启 reconcile 重入 `Executing`/`Verifying` 态的 ChangeRequest，核对集群实际状态后续作/回滚/中止，finalizer 保证清理不悬挂。第三个初衷是**零私有 RPC**：gatekeeper 与 controller 之间不建任何进程级通信，全部经 apiserver 的 CR 契约（spec/status）交互（§3.1 数据流只含 CR 与 MCP 两条线）。

## 2. 职责与任务清单

**reconcile 的 CR 清单**：

| CR | reconcile 职责 | 主要任务来源 |
| --- | --- | --- |
| ChangeRequest | 核心状态机推进（§5.1 status.state 七态）；finalizer 管理；在途恢复（§4.2.5-3）；Executing/Verifying 超时巡检告警（F14 检测手段）；驱动 dry-run、执行后验证（verifier）、回滚（I5） | T2.1、T2.5、T2.6、T2.12 |
| GuardrailPolicy | 护栏策略对象的合法性校验（rateLimit、circuitBreaker 数值域、blastRadiusQuota 正整数、opaPolicyBundle 路径可解析），校验结论写 status；非法策略不静默生效——以 status 标注拒绝原因并视为未生效（fail-closed 的配置面） | T2.3 配套 |
| DecisionRecord | 不可变校验：spec 一经创建拒绝更新与删除（审计对象，§5.3"落盘不可变存储"）；`retentionDays` 到期清理（保留期执行点）；finalizer 保证清理动作完成 | T1.8 配套 |
| SchedulingHint | **本期仅契约位**（§5.4）；reconcile 分支在 M4 决策门 go 后注册：hint 翻译为 Volcano 优先级/亲和性 patch（§4.5）。hint 是建议而非指令，调度器可拒绝 | M4（设计文档 §7） |

**进程职责清单**：① watch 上述 CR 并驱动 reconcile（保持薄：读 CR → 调应用服务 → 写回 status，§3.2 分层规则 3）；② leader election 单写者（I10）；③ 在途恢复与 finalizer；④ 超时巡检与告警；⑤ 探针与优雅退出（骨架见 `cmd/README.md` §4）。

**任务对照**：T2.1（CRD + controller 骨架 + 状态机 + finalizer + leader election）、T2.4（审批状态机服务端化）、T2.5（rollbacker 的执行/回滚驱动）、T2.6（breaker 状态执行与配额结果落 CR）、T2.11（幂等 + 失效语义）、T2.12（冷却期与验证窗口校验）。

## 3. 技术选型与开源包

| 项 | 选型 | 理由 | 放弃项及原因 |
| --- | --- | --- | --- |
| 控制器框架 | **controller-runtime**（版本以 `deploy/versions.md` 钉死为准） | T2.1 明确"controller-runtime 工程"；watch/cache/重试/leader election 全现成 | 自研 reconcile 循环：重复造轮子且无生态 |
| leader election | controller-runtime 自带（K8s Lease 对象） | §4.2.5-1"leader election 保证单写者"的标准实现 | 自建选举：正确性风险无收益 |
| 测试 | envtest（controller 测试标准件）+ fake client（单测）+ kind（集成） | aegis-go-quality 测试节原文约定 | 全部打真集群做单元测试：慢且不可复现 |
| K8s 客户端 | client-go（经 controller-runtime；写 status 走 status subresource） | 与 gatekeeper 同一版本线，对账 `deploy/versions.md` | — |
| 日志/OTel/flag/配置 | 统一骨架选型见 `cmd/README.md` §3 | 三进程一致 | viper：禁 |

选型纪律：reconcile 路径**零 LLM 调用**（I2）——本进程不 import 任何模型 SDK，依赖评审时 grep 可证。

## 4. 具体设计（不写代码）

**ChangeRequest 状态机**（§5.1 status.state；迁移的唯一写者是本进程的 reconcile）：

| 当前态 | 触发事件 | 下一态 | reconcile 动作 |
| --- | --- | --- | --- |
| （无） | gatekeeper 创建 CR | Pending | 校验 spec 完整性（operations 必填、idempotencyKey 存在、timeout 合法） |
| Pending | dry-run 通过 | DryRunning | 驱动 dry-run，结果写 `operations[].dryRunResult` |
| Pending | dry-run 失败 | Rejected | 留痕拒绝原因 |
| DryRunning | 级别 R1 且无需审批 | Executing | 生成执行计划（幂等键去重检查） |
| DryRunning | 级别 R2 | AwaitingApproval | 审批请求附诊断链 + 影响面报告已随 CR（§4.2.1） |
| AwaitingApproval | spec.approval 写入且校验通过 | Executing | 校验：级别确需审批、处于 AwaitingApproval、未超 `rollback.deadline`、非冷却期、非熔断态 |
| AwaitingApproval | 人工拒绝 | Rejected | 留痕 approver/decidedAt |
| 任意非终态 | `status.abort.requested` 置位 | Aborted | 按当前进度决定清理动作；终态之一 |
| Executing | 执行成功 | Verifying | `executedOps` 递增；执行结果留痕 |
| Executing | 执行失败 | Verifying（回滚路径）或 Aborted | 按策略触发 rollbacker：执行 inverseOperation 后进入 Verifying 验证回滚效果 |
| Verifying | 验证通过（`verification.healthy=true`） | Committed | 终态；写 auditRef 关联 |
| Verifying | 验证失败且未超 `rollback.deadline` | RolledBack | 回滚留痕（`rollback.attempted/result`） |
| Verifying | 验证失败且超 deadline | （停留 + 告警） | 视为需人工介入而非自动回滚（§5.1 rollback.deadline 语义，F6） |

非法迁移（如外部直写 status 跳态）由 reconcile 检测后以 controller 持有的状态为准纠正，纠正动作本身留痕——这是红队 R1 整改的服务端机制：aegis-cli 只被授权写 spec（含 spec.approval），status 经 K8s status subresource 权限隔离由 controller 独占。

**在途恢复**（I10/§4.2.5-3，F14 处置；编号步骤）：

1. 进程重启后 watch 重建，重入所有处于 `Executing`/`Verifying` 的 ChangeRequest。
2. 核对集群实际状态：经 `internal/authority`（attributor 区分"自己执行的生效"与"第三方控制器改的"，I9）与对象读回比对。
3. 已生效 → 补验证（走 Verifying 正常出口）。
4. 未生效 → 按策略续作（幂等键保证重试折叠，F15）、回滚或转 Aborted。
5. finalizer 保证终态清理动作完成后才允许 CR 删除，清理不悬挂。

**超时巡检（F14 检测手段）**：周期比对 `Executing`/`Verifying` 态 CR 的停留时长与 `operations[].timeout`、验证窗口配置，超时未推进即告警（指标见 §6）；处置不自动跳成功态，只触发人工/熔断通道。

**leader election 单写者（I10）**：多副本 standby，lease 持有者才执行写 status 的 reconcile 路径；失 lease 即停写（readyz 跟随，见 `cmd/README.md` §4 探针语义表）。

**I2/I9 内建约束**：reconcile 与 verifier 代码路径禁止 LLM 调用；任何"目标对象状态变化"先经 attributor 归因，能归因到健康控制器正常行为的变化 = 非事件，只留痕不诊断、不进写路径（I9/F11）；verifier 观察窗口不得短于相关控制器稳定窗口（F12，T2.12 配置校验强制），否则把正常收敛误判为失败。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| reconcile 返回错误（apiserver 冲突、依赖暂不可用） | controller-runtime 标准错误回传 | 指数退避重入（框架语义） | 安全重试；冲突视为可恢复 |
| 乐观并发冲突（resourceVersion / SSA field-manager，§4.2.4-2） | 写回冲突错误 | **检出即放弃本次推进，重新归因后下一轮再判** | 不蛮写重试（I9 原文）；重试折叠由幂等键兜底 |
| 状态停留超时（Executing/Verifying） | 巡检比对 timeout 字段 | 告警 + 人工通道；不自动判成功 | F14 检测；假成功比悬挂更危险 |
| 回滚前漂移复验失败（目标被第三方修改，F6） | rollbacker 复验 diff | 拒绝自动回滚，升级人工，附漂移 diff | I5 的反假安全感条款 |
| 验证窗口 < 控制器稳定窗口（F12） | CR 配置校验（T2.12） | 校验失败，不进入 Verifying | 把"收敛中"与"失败"区分开 |
| 冷却期内重复动作（§4.2.4-4） | 目标对象冷却期记录 | 拒绝并留痕 | Agent 自身是控制回路一员，防自我激励振荡（R13 整改） |
| finalizer 悬挂（清理依赖不可达） | finalizer 停留时长 | 重试 + 告警；人工摘除留痕 | 不静默丢 finalizer |
| 非法迁移 / 伪造审批（直改 status） | 迁移合法性校验 | 以 controller 状态为准纠正并留痕 + 告警 | 红队 R1 的服务端闭环（T2.4 单测证明） |
| reconcile 路径出现 LLM 调用（I2） | 依赖评审 grep + depguard | 一票否决打回 | LLM 不进热路径 |
| 进程崩溃 | K8s 重启新副本 | 无状态重入：§4 在途恢复序列 | I10：状态全在 CRD status，死亡不丢事实 |

总姿态：状态机侧失败 = 停留原态 + 告警，绝不自动跳"成功态"；一切不确定都宁可慢、宁可找人。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

**结构化日志字段**：`cr_name`、`cr_kind`、`generation`、`resource_version`、`from_state`、`to_state`、`trigger`（event 来源：watch 重入 / 人工 spec 变更 / 超时巡检）、`reconcile_duration_ms`、`requeue_after`、`finalizer_state`、`recovery_mode`（在途恢复路径标记）、`verify_window_seconds`、`verify_stable_window_seconds`。

**指标**：

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `aegis_reconcile_total{controller=,result=}` | counter | 各 CR reconcile 结果（success/error/requeue） |
| `aegis_reconcile_duration_seconds{controller=}` | histogram | reconcile 耗时 |
| `aegis_cr_state_transition_total{from_state=,to_state=}` | counter | 状态迁移计数（审计对账） |
| `aegis_cr_stuck_total{state=}` | gauge | 停留超时未推进的 CR 数（F14） |
| `aegis_finalizer_blocked_total{kind=}` | counter | finalizer 悬挂次数 |
| `aegis_cr_recovery_total{result=}` | counter | 在途恢复结果分布（续作/回滚/中止） |

**Trace span 命名**：`controller.reconcile.changerequest`、`controller.reconcile.guardrailpolicy`、`controller.reconcile.decisionrecord`、`controller.cr.recover`（在途恢复）、`controller.cr.verify`（验证窗口段）；M4 go 后增 `controller.reconcile.schedulinghint`。span 属性携带 `cr_name`、`from_state`、`to_state`。

**审计留痕点**：① 每次状态迁移写回 status（append-only 语义，同症状不同动作可解释，I6）；② 迁移事件与 DecisionRecord 经 `auditRef` 关联（§5.1）；③ 回滚尝试、回滚结果、`rollback.deadline` 越过事件全量留痕；④ 伪造迁移纠正动作留痕 + 告警；⑤ 审批相关迁移记录 approver / decidedAt / channel（§5.1 approval 字段）。

## 7. 依赖方向与模块边界

- **谁调我**：无人以进程方式调用。事件来源全是 apiserver watch：gatekeeper 创建 CR（间接）、aegis-cli 写 spec.approval（间接）、外部系统改 spec（间接）。
- **我调谁**：`internal/controller`（reconcile 薄层）→ 各上下文 `service`（approval / rollback / breaker / authority / audit / session）→ `internal/adapters/k8s` 写 status 与读集群实际状态；adapters/opa（dry-run 验证期的策略复核按设计需要经 service 编排）。
- **禁止依赖谁**：禁止 import gatekeeper 进程的任何包（无私有 RPC，交互仅经 apiserver CR 契约）；跨上下文禁止直接 import 对方 domain（铁律 4，经 service 协作）；reconcile 禁止内联业务规则（铁律 3，业务在 `internal/`）；禁止 import LLM SDK（I2）。
- **被依赖关系**：`internal/controller` 包不得反向 import cmd（装配方向单向，铁律 1）。

## 8. 测试策略与红队用例

1. **envtest 状态机全路径（T2.1 完成判据）**：用 fake 执行器手写 CR yaml 走完整状态机（Pending→…→Committed/RolledBack/Aborted/Rejected）；终态四分支全覆盖，表驱动。
2. **在途恢复（I10）**：进程杀掉重启后 reconcile 正确重入 Executing/Verifying——已生效补验证、未生效续作/回滚/中止；finalizer 不悬挂。
3. **审批伪造（红队 R1 闭环，T2.4 完成判据）**：构造"绕过 CLI 直改 CR status 的审批"用例，状态机拒绝并纠正；aegis-cli 仅 spec 写权限的 RBAC 用例在 kind 验证。
4. **F12/F6/T2.12**：验证窗口短于稳定窗口的 CR 校验失败；回滚前漂移场景拒绝自动回滚并附 diff。
5. **kind 冒烟与三连演示（T2.13）**：`kind-smoke.yml` 自 M2 起为必过门禁（见 `.agents/skills/aegis-ci/SKILL.md`）。
6. **红队检查点 #2 映射**：审批绕过用例（用例 3）是 M2 准入门槛项；I1–I10 检查单中 I2（reconcile 无 LLM 调用）、I9（归因先行）、I10（fail-closed + 重入）的验收方法见 `.agents/skills/aegis-security-review/SKILL.md`。
7. **人审纪律（R-2）**：`internal/controller` 属 L2 安全包清单，AI 产出逐行人审，PR 标注占比与人审人；本目录装配代码随 PR 走常规评审。
