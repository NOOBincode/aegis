# audit — 决策链留痕与回放

> 所属层：L2 安全闸门/控制面 ｜ 里程碑：M1（落地方案 T1.8，M1 验收含 `aegis-cli replay` 回放完整决策链）｜ 设计文档出处：§4.6、§5.3、不变量 I6 ｜ 关联不变量：I6、I10 ｜ 关联失败模式：F1（留痕侧）、F14

## 1. 设计初衷

I6：决策可回放。审计刚需不是"记个日志"——输入快照、推理链、工具调用、审批记录、执行结果全量留痕，并且同样症状不同动作必须可解释（G4）。诊断 Agent 说"我判断根因是 OOM 所以重启了 Pod"，三个月后必须能逐帧还原：它当时看到了什么快照、调了哪些工具、每一步工具返回了什么、闸门判了什么级、谁批的、执行结果如何。

这也是项目叙事的安全底线：闸门类组件天然面临"你怎么证明 Agent 没越权"的质疑，唯一诚实的回答是完整决策链的可回放（M1 验收即含 `aegis-cli replay` 回放完整决策链，M2 三连演示①"Agent 提议 scale→dry-run→审批→执行→一键回滚"每一帧都经得起回放）。

本目录是 DecisionRecord（§5.3 定义的审计对象）的唯一组装者与回放服务。另一个设计动机来自 I10：留痕组件自身不能成为单点——留痕失败时系统该停还是该走，必须给出明确设计（见 §5），不能留给实现期临场发挥。

## 2. 职责与任务清单

对应落地方案 T1.8：DecisionRecord CRD（§5.3 字段全量）+ `cmd/aegis-cli` 的 `replay` 子命令。完成判据：一次诊断会话的输入快照/工具序列/结论完整可回放。

1. 决策事件流的接收与会话级归并（会话全程）。
2. DecisionRecord 组装：输入快照哈希、工具调用序列、sanitizationEvents、CR 引用、结论（§5.3 全字段）。
3. 不可变存储语义保证与 `retentionDays` 到期清理。
4. 回放重建逻辑（供 `aegis-cli replay` 消费，全程只读）。
5. 与 Langfuse trace 的引用关系维护（`llmTraceRef`，§5.3）。
6. 留痕失败的处置执行（写路径阻塞 / 读路径降级，姿态定义见 §5）。

## 3. 技术选型与开源包

- DecisionRecord 是 §5.3 定义的 CRD，存储即 K8s API（etcd）：控制面已有的数据面，不引入外部不可变存储系统（N3 非目标，且个人项目零运维优先级最高）。
- 不可变语义靠权限设计而非产品：controller 运行身份（ServiceAccount）对 DecisionRecord 仅持有 create/delete 权限、**无 update 权限**——RBAC 层面物理杜绝篡改；retention 清理由同一身份按 `retentionDays` 执行。
- 快照本体（对象清单）体积可超 CR 合理载荷，DR 只存引用与哈希；本体存储介质复用会话存储（Redis/Valkey，见 `internal/session/README.md`），DR 记 `clusterSnapshotRef` + 哈希值。该取舍若实现期有异议，走 ADR 重审。
- K8s 客户端经 `internal/adapters/k8s` 的 port（版本以 `deploy/versions.md` 钉死为准）；Langfuse  trace 引用查询经 `internal/adapters/langfuse` 的 port。
- 回放渲染为纯只读逻辑，不执行任何工具调用（回放绝不再碰集群）。

## 4. 具体设计（不写代码）

### 4.1 组装点（会话全程）

编排约定：各层事件经 `internal/controller` 编排上报到本目录 service 的"记录追加"接口，事件流包括：

1. 会话建立：sessionRef 生成，开始采集；输入快照拍摄（相关对象清单规范化序列化 + 哈希），快照本体入会话存储，DR 记录 `clusterSnapshotRef`。
2. 会话中：每次工具调用的请求/响应摘要（经 MCP 代理中间件上报）、sanitize 事件（`internal/sanitize` 协作）、闸门判定（risk 终判、authorityCheck 结果）、审批事件（`internal/approval` 协作）、CR 状态迁移（`internal/controller` 原生可见）。
3. LLM 侧：每次模型调用的 trace 标识由 L3 上报，组装时写入 `llmTraceRef`（§5.3）。
4. 组装触发：会话结束，或关联的全部 CR 到达终态（Committed/RolledBack/Aborted/Rejected）。DR 名派生示例见 §5.3（audit-<date>-<seq>）；重复组装由名字派生天然幂等。

产出 DR spec 全字段：`sessionRef` / `clusterSnapshotRef` / `llmTraceRef` / `toolCalls` / `sanitizationEvents` / `changeRequests` / `outcome` / `retentionDays`（§5.3）。

### 4.2 不可变存储语义与 retentionDays

- 不可变三支柱：RBAC 无 update（见 §3）；任何"补写"以追加新 DR + 引用旧 DR 实现，不改正文；K8s audit log 对 create/delete 天然旁证。
- `retentionDays`（§5.3 示例默认 365）到期：controller 定时巡检删除过期 DR；**删除动作本身**落一条结构化审计日志（自指审计：清理行为也要可追溯）。

### 4.3 回放重建逻辑（`aegis-cli replay`）

编号步骤：

1. CLI 解析 DR 引用，`cmd/aegis-cli/` 装配调用本目录 service。
2. 读取 DR（只读权限），校验完整性：必备字段齐全、快照哈希可对（快照本体仍在会话存储时）。
3. 按 `toolCalls` 时序逐步展开渲染：本步输入（快照引用）→ 工具与参数 → sanitize 命中标记 → 闸门判定链（R 级、authorityCheck、审批记录）→ 执行结果。
4. 关联 `llmTraceRef` 输出 Langfuse 跳转引用；trace 已被 Langfuse 清理时降级为"无 LLM 链"模式并明确标注。
5. 回放全程零集群写、零工具执行——回放是展示链，不是重演。

验收级断言：构造两个症状相同但处置不同的会话，回放逐步比对能精确定位分歧帧（I6"同样症状不同动作必须可解释"的检验方法）。

### 4.4 与 Langfuse trace 的引用关系

- `llmTraceRef` 是**弱引用**：DR 只记引用，Langfuse 侧的 trace 保留策略由观测层自治；回放时引用失效按 §4.3 第 4 步降级标注。
- 组装期的引用写入失败不阻塞 DR 创建：`llmTraceRef` 置空 + `outcome` 不变 + 告警（LLM 链缺失是观测缺口，不是决策链缺口）。
- 方向纪律：DR 引用 Langfuse，Langfuse 不反向引用 DR（避免观测层成为审计依赖，I10）。

## 5. 错误处理与失效语义

**核心设计决策：留痕失败时分路径处置（用户点名问题，给出明确答案与理由）。**

| 场景 | 处置 | 姿态与理由 |
| --- | --- | --- |
| 写操作路径留痕失败（CR 已执行但 DR 组装/写入失败） | 该 CR 不得标记 Committed；状态停在 Verifying 并置"留痕未完成"告警，人工介入补齐留痕后才允许推进 | **fail-closed 阻塞**。I6 是硬不变量，"做了但说不清"比"没做"更糟——审计刚需恰在于写操作必须可解释；宁可暂停自动推进，不留无法回放的已执行变更 |
| 读路径（纯诊断会话）留痕失败 | 会话标注 degraded_audit，继续只读；落盘重试，重试无果则降级标注进会话结论 | **降级**。I10 读路径失效姿态本就是可降级只读；阻塞诊断拖垮可用性且读操作无集群副作用，风险面不同不可与写路径同姿态 |
| etcd 不可用 | 写路径随闸门整体 fail-closed（I10）；读路径降级标注 | fail-closed。留痕依赖的存储不可用时，写准入本就应拒绝（T2.11 的杀进程/断分区测试同源） |
| DR 写入名字冲突 | 名字由 sessionRef+序号派生，冲突即重复组装，幂等跳过 | 幂等处理，防 F15 双写同源 |
| 快照哈希计算失败 | 该会话 DR 记 snapshot_missing + 告警 | 降级留痕。无快照的 DR 明确标注残缺，不伪装完整 |
| retention 清理失败 | 重试 + 告警，不阻塞主链路 | 清理是卫生任务，失败不危及决策链正确性 |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志字段：
  - `audit_record_created{dr_ref, session_ref, outcome, cr_count, event_count}`
  - `audit_write_failed{stage, path=write|read, reason}`（留痕失败事件，本身即自指审计）
  - `audit_retention_sweep{deleted, kept, oldest_age_days}`
  - `audit_replay_served{dr_ref, steps, degraded_llm_trace=true|false}`
- 指标：`aegis_audit_records_total{outcome}`、`aegis_audit_write_failures_total{path}`、`aegis_audit_replay_total`、`aegis_audit_retention_deleted_total`。
- trace span：`gatekeeper.audit.assemble`（组装 span，挂在会话根 span 下）、`gatekeeper.audit.replay`（回放 span）。
- 审计留痕点：DR 本体即审计工件（K8s audit log 旁证 create/delete）；留痕失败事件双写结构化日志与指标（自指）；回放行为本身落审计日志（谁回放了什么，防回放面被滥用为窥探通道的留痕）。

## 7. 依赖方向与模块边界

- **谁调我**：`internal/controller`（CR 终态/会话结束触发组装）；`cmd/aegis-cli`（replay 子命令装配）；各 L2 上下文经 controller 编排上报事件（本目录提供追加接口，各上下文不 import 本目录 domain 之外的东西）。
- **我调谁**：`internal/adapters/k8s`（DR 读写，经 port）；`internal/adapters/langfuse`（trace 引用查询，经 port）；`internal/session`（快照本体存取协作，经 service）。
- **禁止依赖谁**：risk/sanitize/breaker/authority 等上下文的 domain（事件经编排层流入，不反向耦合）；`internal/adapters` 具体实现；任何 LLM SDK（回放不重演推理，只展示留痕）。
- 五铁律对照：domain（组装规则、完整性校验、回放渲染模型，纯单测）+ ports（DR 存储/快照存储/trace 查询接口）+ service（组装与回放用例）；领域文件不 import `k8s.io/*`。

## 8. 测试策略与红队用例

- 领域纯单测（表驱动）：给定事件流 → DR 字段全量断言（§5.3 逐字段）；回放渲染逐步快照比对；幂等组装（同事件流重复触发不产生重复 DR）。覆盖率 ≥80%（L2 安全包，`internal/audit` 在列）。
- envtest：RBAC 无 update 的不可变语义测试（update 调用被 apiserver 拒绝）；retention 到期删除；finalizer 行为。
- F14 交叉测试：组装中途杀进程 → 重启后从 CR 状态 + 已收事件恢复组装，不重复不丢。
- 红队用例：
  1. 篡改已落 DR：伪造 update 正文（改 outcome 掩盖越权）被 RBAC 拒绝，且尝试本身进 K8s audit log；
  2. 写路径留痕失败：CR 执行完成但 DR 写入被注入故障 → CR 不得 Committed，人工补齐前写准入保持阻塞（§5 决策的验证）；
  3. 回放越权面：replay 子命令被注入工具执行参数 → 回放路径协议层拒绝（回放零执行的不变量测试）；
  4. 双会话同症状不同动作：回放比对精确定位分歧帧（I6 验收方法）。
