# breaker — 熔断器与爆炸半径配额

> 所属层：L2 安全闸门/控制面 ｜ 里程碑：M2（落地方案 T2.6，M3 T3.5 演示验收） ｜ 设计文档出处：§4.2.2、§4.2.5、§5.2 ｜ 关联不变量：I2、I7、I10 ｜ 关联失败模式：F7、F14

## 1. 设计初衷

设计文档 §1.1 的定位说得很直白：AI 的变更和人类变更过同一套门禁，且比人类变更多一层熔断。熔断器（circuit-breaker）就是这一层的载体，它回答两个问题：

- 连续失败时谁来拉手刹。R1 级操作在 M3 起全自主执行（T3.5），自主意味着没有人类在回路里看着。一旦 Agent 进入"反复尝试、反复失败"的螺旋（幻觉、错误归因、环境恶化都可能导致），必须有一个确定性机制在阈值处强制降级为只读（设计文档 §4.2.2：滑动窗口失败率超阈值或单位时间变更数超阈值 → 全局降级只读），把破坏力冻结在当前时点。
- 幻觉的破坏力必须有天花板（I7）。爆炸半径配额给单次变更的影响面（Pod 数/节点数/命名空间数）设硬上限，超限直接拒绝，不依赖任何事后补救。

另一个初衷来自 I10 与 F7 的合流：闸门进程自身必须无状态（§4.2.5-1），熔断状态若只活在 gatekeeper 的进程内存里，一次重启就等于"静默复位熔断"，F7 要求的人工复位纪律形同虚设。因此本目录的核心设计约束是：熔断的权威状态落在 CRD status，进程内存只放可重建的缓存。

## 2. 职责与任务清单

本目录承担 circuit-breaker（熔断器）与 quota（配额执行）两个内聚职责，对应落地方案 T2.6：

1. 滑动窗口失败率监测：按 GuardrailPolicy 注入的窗口与阈值，确定性统计窗口内 R1 执行失败率。
2. 单位时间变更数监测：对集群变更速率设兜底熔断触发器（设计文档 §4.2.2）。
3. 熔断判定与生效：触发后全局降级只读（R0 放行、R1+ 拒绝）、推送告警、置位在途 CR 的中止标记。
4. 人工复位受理：仅接受经身份认证的显式复位（CLI），强制携带理由，全程留痕（F7）。
5. 爆炸半径配额执行（I7）：`maxPodsPerChange` / `maxNodesPerChange` / `maxNamespacesPerChange` 在 CR 准入路径的硬判定。
6. 单位时间变更数配额执行：`rateLimit.maxChangesPerHour` 超限拒绝新写 CR。
7. 熔断状态持久化与崩溃重建：权威标志写 GuardrailPolicy status，进程重启后恢复（I10）。

完成判据直接引用落地方案 T2.6：连续失败注入后自动降级；超限变更被拒；复位需 CLI 显式操作且留痕。

## 3. 技术选型与开源包

- 熔断判定、滑动窗口计数、配额比较均为确定性逻辑，Go 标准库足以完成，**不引入第三方依赖**（依赖纪律：能用标准库解决不加依赖）。
- 窗口数据源使用 controller 已有的 CR informer 缓存（client-go，经 `internal/adapters/k8s` 的 port 使用，版本以 `deploy/versions.md` 钉死为准），不为熔断单独新增 watch，避免放大 apiserver 读压力（F5）。
- 熔断状态写回复用 controller 的 K8s 客户端（经 port）；多副本 standby 与 leader election 沿用 §4.2.5-1 的既有结论，本目录不重复实现。
- 复位与状态查询的 CLI 子命令装配在 `cmd/aegis-cli/`，按 aegis-ddd-layout 速查：CLI 只装配，逻辑在本上下文 service 层。

## 4. 具体设计（不写代码）

### 4.1 配置注入

全部窗口与阈值来自 GuardrailPolicy spec（§5.2），经 informer 缓存注入，变更即时生效：

- `spec.circuitBreaker.failureRateThreshold`（默认 0.3）、`spec.circuitBreaker.window`（默认 30m）、`spec.circuitBreaker.action`（默认 downgrade_to_readonly）。
- `spec.blastRadiusQuota.maxPodsPerChange` / `maxNodesPerChange` / `maxNamespacesPerChange`。
- `spec.rateLimit.maxChangesPerHour`。

### 4.2 熔断状态机

状态迁移表（全部确定性迁移，I2，零 LLM 参与）：

| 当前状态 | 事件 | 动作 | 下一状态 |
| --- | --- | --- | --- |
| closed | 窗口失败率超阈值，或单位时间变更数超阈值 | 写 GuardrailPolicy status 熔断标志；推送告警；对在途 CR 按 §4.3 处置 | open |
| open | 新写 CR 到达准入点 | 置 Rejected（reason=circuit_breaker_open）并告警；R0 读请求放行 | open |
| open | 人工复位（CLI 显式操作） | 校验操作者身份与理由；写复位留痕；恢复写准入 | closed |
| closed | 进程重启 | 内存窗口计数丢失但权威标志在 status，直接恢复 | closed |
| open | 进程重启 | 从 status 恢复熔断标志；从 CR 历史重建窗口计数 | open |

本期不引入 half-open（半开）状态：F7 的处置列写死"CLI 显式复位"，自动半开会让"熔断后无人复位"重新变成默认可 recovery，削弱人工复位的仪式性与留痕价值。该取舍在实现期可经 ADR 重审。

### 4.3 熔断在 CR 准入路径的位置

准入管线顺序（由 `internal/controller` 编排，本上下文只提供判定步骤）：

1. CR 进入 Pending，risk-classifier 完成终判，输出 `riskLevel` 与 `estBlastRadius`（§5.1）。
2. **熔断检查（本目录）**：若熔断中，R1+ 新 CR 直接置 Rejected 并告警，不进入 dry-run（省掉注定被拒的开销）；R0 读路径放行，按 §4.2.5-2 标注降级态。
3. **爆炸半径配额检查（本目录）**：`estBlastRadius` 任一维度超 `blastRadiusQuota` 对应上限 → 置 Rejected（reason=blast_radius_quota_exceeded）。
4. **变更数配额检查（本目录）**：滚动 1 小时已提交 CR 数（informer 缓存可查）超 `maxChangesPerHour` → 置 Rejected（reason=change_rate_quota_exceeded）。
5. 通过后继续 dry-run → OPA → 审批 → Executing（后续步骤见 `internal/risk/README.md`、`internal/policy/README.md`、`internal/approval/README.md`）。

熔断触发时点在途 CR（Executing/Verifying）的处置：熔断器置位 §5.1 `status.abort.requested=true`（§5.1 abort 注释明确"可随时人工/熔断器置位"），由 controller 按 §4.2.5-3 的在途恢复语义核对集群实际状态后续作/回滚/中止，finalizer 保证不悬挂（F14 交叉点）。

### 4.4 失败率与窗口口径

- 窗口：滑动时间窗（默认 30m），数据源为 informer 缓存中的 CR 状态流，零额外 apiserver 压力。
- 失败定义（确定性）：CR 终态为 RolledBack/Aborted；或 `status.verification.healthy=false` 触发回滚；或执行器报错终止。分子为失败数，分母为窗口内进入 Executing 的 CR 数。
- 小样本保护：窗口内执行样本数低于最小样本数（配置随 GuardrailPolicy schema 演进，默认值实现期定）时不做失败率熔断。理由：2 次执行 1 次失败即 50%，小样本直接熔断会制造大量误降级；这是防误熔断的必要代价。
- 变更数触发器与 `maxChangesPerHour` 的关系：配额在准入口拒绝，熔断器作为兜底看"单位时间变更数超阈值"（设计文档 §4.2.2）——即使准入拒绝全部生效，异常请求涌入本身达到阈值也说明环境异常，值得全局只读止损。

### 4.5 状态持久化与崩溃重建（I10）

- 权威熔断标志落 GuardrailPolicy status：`status.circuitBreaker.state`（closed/open）、`openedAt`、`openedReason`、`lastReset{by, at, reason}`。
- 进程内存只保留窗口计数缓存——它是可重建派生态：重启后从 CR 历史的 `status.state` 迁移时间戳重算。
- 多副本 standby + leader election 保证 status 单写者（§4.2.5-1），写回冲突按乐观并发重试收敛。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| GuardrailPolicy 缺失或不可读 | informer 缓存为空且读取失败 | 熔断器按"熔断中"处理新写准入（拒绝）并告警 | **fail-closed**。I10：写路径故障不放行；无配置放行等于无限额裸奔 |
| 阈值配置非法（failureRateThreshold 越界、window 为零、quota 为负） | GuardrailPolicy 准入校验 | 拒绝该 GuardrailPolicy 更新，沿用上一份有效配置，告警 | fail-closed 于配置面。非法配置生效 = 配额机制失效 |
| informer 与 apiserver 分区，窗口数据陈旧 | 缓存同步健康探测 | 以最近可用快照判定；判定不了时按熔断中处理新写 | **保守降级**。误判代价（只读降级）可恢复，漏判代价（连续误写）不可控 |
| 熔断标志写回冲突（多副本竞争） | resourceVersion 冲突检测 | 乐观重试；leader 单写者最终收敛 | 与 §4.2.4-2 乐观并发精神一致，不蛮写 |
| 复位来源不可信或缺理由 | K8s RBAC（仅授权身份可 update GuardrailPolicy status）+ 复位请求字段校验 | 非法来源由 apiserver 拒绝；缺理由拒绝 | fail-closed 于复位面。复位是安全敏感动作，F7 要求留痕 |
| 熔断判定模块自身崩溃 | 进程退出/健康检查失败 | 多副本 standby 接管：从 status 恢复标志、从 CR 历史重建窗口；接管间隙新写一律拒绝 | **fail-closed**。熔断不可用时准入默认拒绝，绝不"熔断挂了先放行" |

熔断后的持续失效语义（F7）：保持只读直到人工复位；未复位期间告警按固定间隔重复推送，防止"告警被忽略后系统永久只读无人发现"演变成另一种故障。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志字段（JSON，英文 snake_case）：
  - `breaker_state_change{from_state, to_state, trigger=failure_rate|change_rate, failure_rate, window, policy_ref}`
  - `breaker_admission_reject{cr_ref, reason=circuit_open|quota_exceeded}`
  - `quota_reject{cr_ref, quota_type=pods|nodes|namespaces|changes_per_hour, requested, limit}`
  - `breaker_reset{by, reason, policy_ref, at}`
- 指标：
  - `aegis_breaker_state`（gauge：0=closed，1=open）
  - `aegis_breaker_window_failure_rate`（gauge，当前窗口失败率）
  - `aegis_breaker_admission_rejections_total{reason}`
  - `aegis_blast_radius_quota_rejections_total{quota_type}`
  - `aegis_breaker_resets_total`
- trace span：`gatekeeper.breaker.evaluate`（准入路径子 span，属性携带 policy_ref、window、failure_rate）。
- 审计留痕点：
  - 熔断触发写 GuardrailPolicy status，K8s audit log 天然旁证；
  - 被拒 CR 的 `status.state=Rejected` 携 reason，且 CR 自带 `auditRef` 进 DecisionRecord（§5.1）；
  - 复位操作写 `lastReset{by, reason}`，并经 `internal/audit` 协作产生 human 来源审计事件；
  - 未复位期间每次重复告警自身也是一条留痕事件。

## 7. 依赖方向与模块边界

- **谁调我**：`internal/controller` 的 ChangeRequest reconcile 编排（在 risk 分级后、dry-run 前调用本目录 service 的准入检查步骤）；`cmd/aegis-cli`（复位/状态查询子命令，仅装配）。
- **我调谁**：`internal/adapters/k8s`（经 port 读 CR 状态流、写 GuardrailPolicy status）；`internal/audit`（熔断/复位/拒绝事件留痕，经 service 协作，不 import 其 domain）。
- **禁止依赖谁**：其余上下文（risk/policy/approval/rollback/authority/sanitize/session）的 domain——准入编排由 controller 组合各 service，本目录不做编排；`internal/adapters` 的具体实现包；任何 LLM SDK（熔断判定零 LLM，I2）。
- 五铁律对照：本目录按 domain（状态机、窗口计数、配额比较，纯单测）+ ports（状态读写接口）+ service（准入检查用例）三段组织；领域文件不 import `k8s.io/*`、OPA、MCP SDK。

## 8. 测试策略与红队用例

- 领域层纯单测（表驱动，不起集群）：滑动窗口计数边界（窗口首尾样本归属）、失败率口径（恰 30%、小样本不触发）、状态机全部迁移路径、配额比较边界（恰等于上限放行、超 1 拒绝）。覆盖率 ≥80%（L2 安全包纪律，`internal/breaker` 在列）。
- envtest：熔断标志写回、resourceVersion 冲突重试。
- kind 集成（`make test-integration`）：连续失败注入后自动降级只读（与 M3 验收②演示脚本同源，T3.5）。
- 红队用例：
  1. 杀 gatekeeper 进程后重启：熔断标志未被静默清除，写准入仍拒绝，复位必须走 CLI（F7、I10）；
  2. 提交非法 GuardrailPolicy（阈值 0、无限额）：被准入校验拒绝，沿用上一份有效配置；
  3. 无权限身份/缺理由调用复位：拒绝并留痕；
  4. 熔断触发瞬间存在 Executing 态 CR：abort 置位后在途恢复无悬挂（F14 交叉）；
  5. 配额绕过尝试（申报材料缩小 estBlastRadius）：本目录联动断言 risk 终判与 dry-run 复核能识别，超真实影响面的变更仍被拒（I7）。
