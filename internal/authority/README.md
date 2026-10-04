# authority — 所有权图谱与变更归因

> 所属层：L2 安全闸门/控制面 ｜ 里程碑：M2（落地方案 T2.9 基础版，M3 归因正确率/误修复率验收） ｜ 设计文档出处：§4.2.2、§4.2.4 ｜ 关联不变量：I9 ｜ 关联失败模式：F3、F11、F12、F13

## 1. 设计初衷

I9 的判断依据是本项目最重要的世界观：K8s 默认即多控制器并发系统。HPA、VPA、cluster autoscaler、descheduler、各类 operator、ArgoCD 各自正确地改写集群状态，"被干扰"是这个环境的默认态而非异常。一个只看得见"当前状态"的 Agent 会把控制器的正常动作误读为故障，进而做出"修复"——误修复比不修复更糟（F11），而且 Agent 自身也是控制回路的一员，动作频率不受约束就会与其他控制器互相激励、放大振荡（设计文档 §4.2.4-4）。

authority-map 与 attributor（设计文档 §4.2.2）是这道防线的两个面：

- **authority-map（所有权图谱）** 回答"这个字段归谁管"。任意对象的关键字段（replicas、resources、调度约束）都能给出确定性的归属答案，零 LLM 参与。有了它，闸门才能执行 §4.2.4-1 的"不抢字段"铁律。
- **attributor（变更归因）** 回答"这次变化是谁干的"。Agent 感知到的任何状态变化先过确定性归因流水线，能归因到健康控制器正常行为的变更 = 非事件，只留痕、不进诊断、不触达写路径；解释不了的残差才进入 LLM。归因先行同时大幅压缩 token 消耗（§4.2.2）。

这也是差异化叙事的支柱之一（设计文档 §1.5："归因正确率与误修复率为一等指标"），本目录按重点组件写透。

## 2. 职责与任务清单

对应落地方案 T2.9（authority-map + attributor 基础版）。完成判据引用 T2.9：HPA 正常伸缩场景下 Agent 零动作（为 M3 误修复率指标打底）；被 HPA 所有的 replicas 字段直写被拒，意图被翻译为改 `minReplicas` 建议。

1. 字段级所有权图谱的构建、增量刷新与查询（authority-map）。
2. authorityCheck 三态判定（clear / via-owner-api / rejected），结果写入 CR `spec.operations[].authorityCheck`（§5.1）。
3. 意图翻译规则：把 Agent 意图确定性映射为对字段所有者自身 API 的操作（§4.2.4-1）。
4. 变更归因流水线：所有权图谱 + 事件时间线 + 指标佐证的三段式确定性归因（§4.2.2 attributor）。
5. 跨控制器振荡模式库与巡检（F13），输出"控制器组合失配"报告。
6. 目标对象冷却期规则执行（§4.2.4-4）。
7. 向 verifier 提供"相关控制器稳定窗口"查询，支撑验证窗口 ≥ 稳定窗口的校验（F12，与 `internal/rollback` 协作）。

## 3. 技术选型与开源包

- 图谱与事件时间线均为**可重建派生态**：权威事实永远在集群侧（`metadata.managedFields`、ownerReferences、各控制器 status、K8s Events），本目录经 informer 增量维护内存索引，进程崩溃后全量重建，不违反 I10"进程内存不持有不可恢复状态"。
- informer 基于 client-go（经 `internal/adapters/k8s` 的 port 使用，版本以 `deploy/versions.md` 钉死为准）。
- 指标佐证需要读取 Prometheus 数据，authority 在 ports 声明指标读取接口，由新增的 `internal/adapters/prom` 适配器实现（遵循 aegis-ddd-layout 速查：新外部系统对接放 `internal/adapters/<name>/`）。
- 图谱匹配、翻译规则表、振荡模式匹配全部确定性实现，不加 LLM SDK、不加第三方规则引擎。

## 4. 具体设计（不写代码）

### 4.1 字段级所有权图谱（authority-map）

**图模型**：节点 = 对象字段（group/version/kind/namespace/name/fieldPath）；边 = 所有权声明，记录 `{field, ownerKind, ownerRef, source, lastSeenAt}`。ownerKind 例如 hpa/vpa/scaledobject/argocd/operator/descheduler。

**数据来源**（任务书与设计文档 §4.2.2 给定的七类，全部确定性解析）：

1. `metadata.managedFields`：SSA 字段管理者，最细粒度的字段级来源；manager 名与控制器类型对照表维护在本目录（确定性映射）。
2. HPA 的 `scaleTargetRef`：声明其对目标工作负载 replicas 的所有权。
3. VPA 的目标引用与 recommendation：声明其对 resources 的所有权。
4. KEDA ScaledObject 的 scaleTargetRef：同 HPA 语义。
5. operator CRD 管辖：CRD 存在即其 controller 管辖其实例的 spec；配合 managedFields 的 manager 名交叉确认。
6. ArgoCD Application：经其追踪注解/标签与资源树确定 GitOps 管辖面（与 F3 双写检查同源）。
7. descheduler / cluster autoscaler 配置：其策略影响的对象集合（如 descheduler 策略命中的命名空间/pod 选择器）。

**构建与刷新（informer 增量）**：

1. 启动时全量 list（分页 + 对象数上限，防 F5），建立初始索引。
2. watch 增量：对象 add/update → 解析 managedFields 与各类控制器引用 → 与旧快照 diff → 更新字段所有权索引；delete → 摘边。
3. 控制器配置对象（HPA/VPA/ScaledObject/Application/descheduler 配置）各自独立 informer，映射为"目标引用边"。
4. 索引新鲜度上界 = informer 同步延迟；准入判定容忍短暂陈旧，由乐观并发兜底（§4.2.4-2：写前 resourceVersion/SSA 冲突检测，检出即放弃重归因）。

### 4.2 authorityCheck 三态判定

在 CR 准入路径中，risk-classifier 输出目标对象清单后，本目录对每个待写字段判定（结果落 §5.1 `spec.operations[].authorityCheck{fieldOwner, decision}`）：

| 判定 | 语义 | 后续 |
| --- | --- | --- |
| clear | 目标字段无已知 owner，或 owner 是 aegis 自身（本次闸门写入的 field-manager） | 允许进入直写路径（仍走 OPA/配额等后续闸门） |
| via-owner-api | 目标字段有控制器所有权 | 禁止直写；进入意图翻译（§4.3），产出对所有者 API 的操作建议或转 PR 通道 |
| rejected | 有 owner 且无法安全翻译，或翻译规则明确要求人审 | 拒绝该操作，CR 记录拒绝理由，告警 |

**保守默认**：字段无任何所有权证据（无 managedFields、无 ownerReference、无控制器引用）时按"未知所有权"处理——倾向 via-owner-api 或 rejected，不轻易 clear。理由：双写冲突（F3）的代价是控制器互搏，远高于一次被拒的交互代价。

### 4.3 意图翻译规则（确定性规则表）

翻译规则表 = `{意图模式, 字段 owner 类型, 翻译动作}` 三元组，逐条人审入库（L2 安全包纪律）。示例（设计文档 §4.2.4-1 给定）：

- 意图"要更多副本" + owner=HPA → 翻译为修改 HPA `minReplicas`（或附"暂停 HPA"备选建议），而非改 Deployment `replicas`。
- 意图"调整资源 requests/limits" + owner=VPA → 翻译为修改 VPA 策略，而非直接改 Pod spec。
- 目标为 ArgoCD 管辖资源 → 一律转 GitOps PR 通道（ADR-001 的 R2 强制面，F3）。
- 无匹配规则 → rejected，附人类可读的"该字段归谁管、应该走哪条路"说明；**不猜、不自由发挥**（I1 的确定性精神）。

翻译产物仍是一个新的 ChangeRequest（例如"改 HPA minReplicas"），必须走全套闸门——翻译不豁免任何下游检查。

### 4.4 变更归因流水线（attributor）

输入 = Agent 感知到的状态变化项 `{objectRef, fieldPath, 变化摘要, observedAt}`。编号步骤：

1. **图谱查询**：查 authority-map，取该字段的 owner 集合；无 owner 直接进第 4 步（残差候选）。
2. **事件时间线核对**：拉取该 owner 的行为证据——K8s Events、audit log、HPA status（currentReplicas/lastScaleTime）、VPA recommendation、autoscaler 扩缩记录、ArgoCD sync 记录、descheduler 日志；按时间窗对齐 observedAt。
3. **指标佐证**：读取相关指标（replicas 曲线、利用率、节点数）在变化时点前后的确定性比对（经 ports 的指标接口）。
4. **判定**：变化时点落在控制器动作窗口内、方向与幅度一致 → `attributed`（= 非事件）；证据对不上或缺口过大 → `unattributed` 残差，交 L3 诊断 Agent；部分吻合 → `partial`，附证据缺口说明，同样交 L3 但带"疑似某控制器"线索。
5. **留痕**：判定结果与证据引用落结构化审计日志，会话级按 sessionRef 归并。

输出 verdict 三态：`attributed`（非事件，只留痕，不进诊断、不触写路径）/ `unattributed` / `partial`。全流水线确定性，LLM 只对未归因残差做解释（§4.2.2）。

### 4.5 跨控制器振荡模式库（F13）

模式库条目 = `{pattern_id, 涉及控制器组合, 指标特征描述, 确定性判据, 输出动作}`。内置模式（设计文档 §4.2.4-5 / F13 给定）：

- VPA + HPA 同指标互搏：requests 与副本数反相位周期波动，周期不长于两者控制周期之和。
- VPA 抬 requests → cluster autoscaler 加节点的成本螺旋：requests 阶梯上升 + 节点数单调上升 + 实际利用率不升。
- descheduler 驱逐 ↔ PDB 阻止拉锯：驱逐事件与 PDB 拒绝事件交替，Pod churn 率超基线。

巡检（确定性定时任务）：对历史指标窗口做周期性波动检测，命中模式 → 输出"控制器组合失配"报告。报告是**只读产物**：进诊断结论与人工报告，任何写建议仍走闸门，模式库自身不触发动作。

### 4.6 冷却期规则（§4.2.4-4）

- 索引：按目标对象（kind/namespace/name）维护 `{lastActionAt, actionSummary, cooldownUntil}`，数据来源于已 Committed 的 ChangeRequest（可重建，不单独持久化）。
- 时长：按字段 owner 类型查表（类比 HPA `stabilizationWindowSeconds`），本期默认值实现期定并随 GuardrailPolicy schema 演进。
- 冷却期内对同一目标对象的同向/重复动作 → 拒绝并返回"冷却中"说明（指向触发冷却的历史 CR）。
- **回滚豁免**：反向纠错动作（含逆操作执行）不受冷却期限制——纠错被节流会把可回滚窗口拖过 `rollback.deadline`，违背 I5。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| 某类数据源 informer 同步失败（如 HPA informer 断连） | 缓存同步健康探测 | 该来源标记 stale；authorityCheck 对该来源可见的 owner 保守化（视为"有未知 owner"） | **保守降级**。误判方向是拒写与转 owner API，代价远小于双写互搏（F3） |
| managedFields 缺失（老对象/非 SSA 写入） | 解析结果为空 | 确定性回退：ownerReferences + 控制器特征注解识别；仍不明 → 未知所有权保守判定 | 降级不放行。老对象不代表无主 |
| 事件时间线缺口（Events 默认 1h 过期） | 证据计数与时效校验 | verdict 降为 partial 并标注缺口，不硬归因 | 归因宁可缺不可错，错归因直接喂养误修复（F11） |
| 指标佐证源超时 | 指标接口错误返回 | 归因继续但证据标注缺失；缺指标佐证时 verdict 不高于 partial | 降级留痕。佐证是增强项不是门槛项 |
| 图谱未就绪时收到写准入 | 启动自检（初始 list 完成度） | authorityCheck 无法完成 → 该 CR 拒绝（reason=authority_unavailable） | **fail-closed**（I10）。无所有权信息放行=赌博 |
| attributor 自身崩溃 | 进程退出 | 归因流水线暂停，读路径与图谱查询不受影响；恢复后从未决变化队列续跑 | 归因是降噪层，崩溃不得拖垮写路径之外的任何面 |
| 振荡巡检误报（正常波动被当失配） | 模式判据的人审与 eval 校准（F10 同源） | 报告标注"疑似"，低严重度模式先观察后告警 | 主动巡检宁可少报不可滥报，滥报制造告警疲劳（F8） |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志字段：
  - `authority_check_decision{cr_ref, field, field_owner, decision, translated_intent, latency_ms}`
  - `intent_translated{cr_ref, from_intent, to_action, owner_kind}`（翻译成功）与 `intent_rejected{cr_ref, reason, owner_kind}`（翻译失败）
  - `attribution_verdict{object_ref, field, verdict=attributed|unattributed|partial, controller, evidence_count, gap_note}`
  - `cooldown_reject{cr_ref, target_ref, cooldown_until, original_cr}`
  - `oscillation_pattern_hit{pattern_id, subjects, window}`
- 指标：
  - `aegis_authority_check_total{decision=clear|via_owner_api|rejected}`
  - `aegis_attribution_total{verdict=attributed|unattributed|partial}`
  - `aegis_attribution_suppressed_total`（非事件只留痕计数，直接对应 F11 防线收益）
  - `aegis_oscillation_pattern_hits_total{pattern_id}`
  - `aegis_cooldown_rejects_total`
- trace span：`gatekeeper.authority.check`（authorityCheck 子 span）；`gatekeeper.authority.attribute`（归因流水线 span，下挂 `graph.query` / `timeline.fetch` / `metrics.correlate` 子 span）。
- 审计留痕点：authorityCheck 结果随 CR `spec.operations[].authorityCheck` 留痕（§5.1，随 CR 全生命周期可回放）；归因 verdict 落结构化审计日志并按 sessionRef 归并到会话；振荡报告作为只读工件留存并引用相关会话；冷却拒绝在拒绝的 CR 中留 original_cr 指针。

## 7. 依赖方向与模块边界

- **谁调我**：`internal/controller`（CR 准入编排调用 authorityCheck；变化巡检触发归因）；`internal/rollback`（verifier 查询相关控制器稳定窗口，F12 校验消费方）；`cmd/aegis-cli`（振荡报告查询等只读子命令装配）。
- **我调谁**：`internal/adapters/k8s`（informer 与 Events/audit log 读取，经 port）；`internal/adapters/prom`（指标佐证，经 port）；`internal/audit`（判定与报告留痕，经 service 协作）。
- **禁止依赖谁**：risk/policy/approval/rollback/breaker/sanitize/session 的 domain（各管一段准入，由 controller 编排组合）；`internal/adapters` 具体实现；任何 LLM SDK（I9 要求零 LLM 参与）。
- 五铁律对照：domain（图模型、翻译规则表、模式判据、冷却算法，纯单测）+ ports（集群状态/指标读取接口）+ service（authorityCheck 与归因用例）；领域文件不 import `k8s.io/*`。

## 8. 测试策略与红队用例

- 领域纯单测（表驱动）：managedFields 解析对照表、翻译规则全组合、模式判据在合成指标序列上的命中/不误报、冷却期边界（恰到期放行、回滚豁免）。覆盖率 ≥80%（L2 安全包，`internal/authority` 在列）。
- envtest：authorityCheck 写入 CR spec 字段；finalizer 行为。
- kind 集成：T2.9 完成判据场景——HPA 正常伸缩时 Agent 零动作；被 HPA 所有的 replicas 直写被拒且意图翻译为 `minReplicas` 建议；VPA+HPA 互搏场景模式库命中且不误修复（M3 验收④同源）。
- 红队用例：
  1. 伪造 managedFields（SSA manager 伪造为"aegis"）企图让字段显示无主——判定须与 HPA scaleTargetRef 等独立来源交叉验证，伪造成立仍被拒（F3/I9）；
  2. 构造控制器干扰并发（HPA 伸缩与故障注入同时发生）：attributed 不误报故障、unattributed 不漏交残差（F11 双向）；
  3. 意图翻译绕过：Agent 提议"改 Deployment replicas"被翻译后，直写 Deployment 的变体路径仍被 authorityCheck 拦截；
  4. 冷却期绕过：改对象名/换命名空间发起同意图动作，按"同工作负载同意图"归组判定，识别并拒绝（§4.2.4-4 的精神检验）；
  5. 图谱服务被杀：写准入 fail-closed 拒绝（I10 杀进程测试清单项）。
