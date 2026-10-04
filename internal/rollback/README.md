# rollback — 逆操作生成与验证（rollbacker）

> 所属层：L2 安全闸门（控制面） ｜ 里程碑：M2（落地方案 T2.5） ｜ 设计文档出处：§4.2.2（rollbacker 契约）、§5.1（inverseOperation/rollback 字段）、§6 F6 ｜ 关联不变量：I5、I10 ｜ 关联失败模式：F6、F12、F14

## 1. 设计初衷

I5 是平台对 AI 写操作的终极承诺：一切写操作可回滚，每个写操作强制生成逆操作并 dry-run 验证；无法生成逆操作的自动升级 R2（人审）。红队记录 R2（P0）补上另一半教训：逆操作生成成功 ≠ 可执行——目标状态漂移会使回滚失败，造成假安全感。因此 rollbacker 的核心是"验证优先"：逆操作先过 dry-run、回滚前再过漂移复验，任何一步不过就升级人工，绝不让"回滚"二字本身成为安慰剂。

本上下文还承接两条失效语义：`rollback.deadline` 超时转人工（§5.1：超过此时间视为需人工介入而非自动回滚），以及与 verify 窗口的协同（F12：验证窗口不得短于控制器稳定窗口，回滚触发判定不得把"收敛中"误判为失败）。

## 2. 职责与任务清单

1. 逆操作生成：对 scale/restart/cordon 三族操作按规则表生成逆操作；无逆操作者标记"效果不可回滚"并触发升 R2。
2. 逆操作强制 dry-run 验证：通过才允许 `inverseVerified=true`（§5.1）；失败则升级级别（§4.2.2）。
3. 回滚前漂移复验（F6）：比对目标对象当前状态与执行后快照，漂移则拒绝自动回滚、升级人工并产出漂移 diff。
4. 回滚执行的 dry-run 复验与正式执行：乐观并发，冲突即放弃并重新归因（§4.2.4-2）。
5. `rollback.deadline` 语义执行：超时不再自动回滚，转人工并留痕。
6. 与 verify 窗口协同（F12）：只消费 verifier 的判定，不自行缩短观察窗口；"收敛中"不触发回滚。
7. 状态衔接：回滚结果驱动 CR 状态机到 RolledBack；finalizer 保证清理不悬挂（§4.2.5）。

## 3. 技术选型与开源包

- 逆操作规则表为纯代码（表即代码、PR 可评审），与 risk 判定表同一纪律；规则随写工具集扩张同步增补，每加一个写工具过一遍准入清单（`aegis-security-review` 新写工具准入）。
- 集群读写（取当前状态、dry-run、执行逆操作）全部经本上下文 `ports` 声明的出站接口，由 `internal/adapters/k8s` 实现；domain 零 `k8s.io/*` 依赖。
- dry-run 复用 K8s 原生 dry-run 语义（§5.1 `dryRunResult` 字段的底层），不自研第二套模拟器。
- 领域层零外部依赖；涉及版本以 `deploy/versions.md` 钉死为准。

## 4. 具体设计（不写代码）

### 4.1 三族逆操作规则表

| 操作族 | 逆操作 | 生成依据 | 效果不可回滚标记 |
| --- | --- | --- | --- |
| scale_deployment（目标 replicas=N） | scale_deployment（replicas=执行前原值） | 执行前对象快照中的原 replicas | 否 |
| restart_pod | 无 | 进程已重启，历史进程状态不可恢复（§4.2.2 明示） | 是 → 自动升 R2 |
| cordon_node | uncordon（解除节点封锁） | cordon 天然可逆 | 否 |

- 表外操作一律按"无逆操作"处理：标记不可回滚并升 R2，直到规则表补入该族——与 risk 表未命中同级的 fail-closed 纪律。
- 逆操作参数必须取自执行前快照（原 replicas），不允许从集群现值反推——现值可能已漂移，反推即制造假逆操作。

### 4.2 逆操作生成与验证流程

1. 操作获批后、执行前：读取目标对象执行前快照（原 replicas 等）。
2. 查 §4.1 规则表生成逆操作；无逆操作 → 标记不可回滚 + 升 R2，阻断自动执行路径。
3. 逆操作提交 dry-run 验证（经端口）：通过 → `inverseVerified=true` 写入 CR；失败 → 升 R2 并记录失败原因（§4.2.2：逆操作生成后必须 dry-run 验证通过，失败则升级级别）。
4. 逆操作与快照随 CR 保留至 `rollback.deadline`（§5.1 `rollback` 字段）。

### 4.3 回滚流程

1. 触发：执行后验证失败 / 人工触发 / `status.abort` 置位。
2. 截止检查：当前时间超过 `rollback.deadline` → 不自动回滚，转人工并留痕（§5.1 语义）。
3. 漂移复验（F6）：取目标对象当前状态与执行后快照比对——一致则继续；漂移（第三方已修改目标）则拒绝自动回滚，升级人工，产出漂移 diff（字段级：哪些字段偏离快照、现值为何）。
4. 回滚前 dry-run 复验（F6 的检测手段即"回滚前 dry-run 复验"）：通过 → 执行；失败 → 升级人工。
5. 正式执行：携带 resourceVersion 乐观并发；冲突检出 → 放弃本次回滚并重新归因（§4.2.4-2），不蛮写重试。
6. 结果写 CR：`rollback.attempted=true`、`result`；状态机 → RolledBack（或按策略续作/中止）；finalizer 清理。

### 4.4 与 verify 窗口的协同（F12）

- 回滚触发判定只消费 verifier 的结论；verifier 观察窗口受"≥ 相关控制器稳定窗口"约束（如 HPA `stabilizationWindowSeconds`，§4.2.4-3），该约束在 CR 校验层强制（落地方案 T2.12）。
- 窗口内控制器尚未收敛 → verifier 判"收敛中"而非失败（F12 处置）→ 本上下文不触发回滚，等待延长观察；rollbacker 永不自行缩短或无视该窗口。
- 目标对象冷却期（§4.2.4-4）：冷却期内的重复动作/重复回滚请求直接拒绝，防 Agent 自我激励回路放大振荡。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| 无逆操作（restart 族/表外操作） | 规则表查询 | 标记效果不可回滚 + 自动升 R2 | fail-closed 于自动路径：不可回滚的写操作不允许自主执行（I5） |
| 逆操作 dry-run 验证失败 | dry-run 结果 | 升 R2 + 记录原因，不留假安全感 | 红队 R2：生成成功 ≠ 可执行 |
| 回滚前检出漂移（F6） | 快照比对 | 拒绝自动回滚 + 升级人工 + 附漂移 diff | 自动路径 fail-closed；人工通道保留——人基于 diff 决断，而非机器蛮干 |
| 回滚 dry-run/执行失败 | dry-run 与执行返回 | 告警 + 转人工，记 `result` | 自动回滚不无限重试；失败本身即升级信号 |
| `rollback.deadline` 超时 | 截止检查 | 转人工（§5.1），留痕 | 超时后漂移积累已不可控，自动回滚风险大于收益 |
| 验证窗口内"收敛中" | verifier 判定 | 不触发回滚，延长观察（F12） | 把正常收敛误判为失败会触发多余动作、加剧振荡 |
| 乐观并发冲突 | resourceVersion 检出 | 放弃并重归因（§4.2.4-2） | 不蛮写重试；与控制器打架是 I9 明令避免的行为 |
| 进程故障/分区（F14） | 状态机巡检 | 在途回滚由 reconcile 重入：核对集群实际状态，已生效补验证、未生效按策略续作/回滚/中止；finalizer 保证清理 | 一切状态落 CRD，无内存不可恢复态（I10） |

整体原则：自动回滚全路径 fail-closed（宁可不动，不可错动），人工通道始终开放且带完整上下文（diff、快照、超时原因）。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志（事件 `rollback_event`）：`cr_name`、`trigger`（verify_failed/manual/abort）、`deadline_exceeded`、`drift_detected`、`drift_fields`、`diff_ref`、`dryrun_result`、`concurrency_conflict`、`escalated_to_human`、`result`、`duration_ms`。
- 指标：
  - `aegis_rollback_total{result, trigger}`：回滚执行计数；
  - `aegis_rollback_drift_detected_total`：漂移检出计数（F6 频率观察）；
  - `aegis_rollback_dryrun_failed_total`：回滚 dry-run 失败计数；
  - `aegis_rollback_escalated_total{reason}`：转人工计数（无逆操作/漂移/超时/执行失败分列）；
  - `aegis_rollback_deadline_exceeded_total`：deadline 超时计数；
  - `aegis_inverse_verify_failed_total`：逆操作验证失败计数（升 R2 触发器）。
- Trace：span `gatekeeper.rollback.execute`；子 span `rollback.drift_check`、`rollback.dryrun`、`rollback.inverse_apply`。
- 审计留痕点：`inverseOperation`/`inverseVerified`/`rollback.attempted`/`rollback.result` 全落 CR（§5.1）；漂移 diff 引用随 DecisionRecord 保存（I6）；每次升 R2、转人工、deadline 超时均记审计事件，纳入 F6 度量与 `docs/threat-model.md` 统计。

## 7. 依赖方向与模块边界

- 谁调我：`internal/controller` 的 reconcile（回滚编排执行者）与 `cmd/gatekeeper`；verifier 的判定经编排层以值对象传入（本上下文不 import 对方 domain）。
- 我调谁：仅本上下文 `ports` 声明的出站接口——"读取对象当前状态"、"提交 dry-run"、"执行写操作"，均由 `internal/adapters/k8s` 实现；verifier 结论与 GuardrailPolicy 窗口配置由编排层翻译为领域值对象传入。
- 禁止依赖谁：`k8s.io/*`、`sigs.k8s.io/*`、OPA/MCP SDK、`internal/adapters/*`、其他上下文 domain——depguard 强制（见 `.agents/skills/aegis-ddd-layout/SKILL.md`）。
- 边界红线：逆操作规则表禁止运行期从外部加载（同 risk 表纪律）；回滚触发判定不得绕开 verifier 的窗口约束（F12）。

## 8. 测试策略与红队用例

- 规则表单测：三族全覆盖；restart 族断言"标记不可回滚 + 升 R2"；表外操作同断言。
- 验证链单测：逆操作 dry-run 失败 → 升 R2；成功 → `inverseVerified=true` 落 CR。
- 漂移场景测试：构造目标被第三方修改（T2.5 完成判据）→ 拒绝自动回滚 + 升级人工 + diff 字段级正确。
- deadline 测试：超时触发 → 转人工，绝不自动执行。
- F12 测试："收敛中"判定不触发回滚；窗口过短的 CR 在校验层被拒（T2.12）。
- 恢复语义测试：模拟回滚执行中进程死亡 → reconcile 重入核对集群实际状态，已生效补验证、未生效按策略续作（I10/F14）。
- 红队用例（M2 检查点）：回滚竞态注入（回滚过程中目标再被修改）——漂移复验与乐观并发必须拦住；伪造 diff、伪造 `inverseVerified` 的尝试必须被状态守卫拒绝。
- 覆盖率 ≥80%（L2 安全包，落地方案 R-2 全人审范围）。
