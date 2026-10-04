# internal/adapters/redis — 会话态外置存储

> 所属层：控制面/适配器层（gatekeeper 无状态化的会话支撑）｜ 里程碑：M1（会话预算随 T1.4 起用）｜ 设计文档出处：§3.3、§4.2.5-1、§5.2 ｜ 关联不变量：I10（无状态化 / 失效姿态自明）｜ 关联失败模式：F4（诊断循环失控）

## 1. 设计初衷

I10 要求 gatekeeper 无状态化（§4.2.5-1：进程不持有不可恢复状态，多副本 standby 由 leader election 保证单写者）。但会话天然有状态：会话粘性、工具调用/token 预算、目标级冷却期（§4.2.4-4）。这些状态放进程内存，leader 切换即丢且多副本不一致；放 CRD 则高频读写压 etcd 且语义不匹。设计文档 §3.3 已定 Redis/Valkey——会话粘性、TTL 天然匹配；放弃 etcd 的理由原文即"不适合高频会话读写"。本目录是该决策的适配器实现。

本目录的存在前提是一条状态分工红线：会话态在 Redis，变更态在 CRD，崩溃恢复只信 CRD（I10、§4.2.5-3）。Redis 里的一切都必须可丢、可重建；谁把不可丢状态放进本目录，谁就是在违反 I10。

## 2. 职责与任务清单

1. 实现 `internal/session/ports.go` 的会话存储端口：会话注册、查询、续约、关闭。
2. 预算计数：`sessionBudget.maxToolCalls` 与 `maxLLMTokens` 的原子递增与剩余额度查询（§5.2，F4 的执行点）。
3. 冷却期记录：目标对象级冷却期 key 的写入与判定（§4.2.4-4：冷却期内不重复动作）。
4. 会话绑定：sessionRef 与 clusterRef、guardrailPolicyRef 的绑定关系读写（§5.1 关联字段的会话侧镜像）。
5. 可丢性维护：所有 key 带 TTL；不提供也不承诺任何持久化语义（崩溃语义见 §4.4）。

## 3. 技术选型与开源包

- Redis/Valkey 服务端：设计文档 §3.3 选型原文；中间件一律"docker-compose 从简"（落地方案 T0.5 观测基座同款取向）。
- Go 客户端：具体选型与版本以 `deploy/versions.md` 钉死为准；PR 附 license 说明（`aegis-go-quality` 依赖纪律：仅 MIT / Apache-2.0 / BSD）。
- 部署形态：本期单机单实例，不做哨兵与集群模式。理由：个人项目从简（N3 取向）+ 会话态可丢（§4.4）；哨兵/集群留作 gatekeeper 真正多副本常驻时的升格项记录于此，本期不实施——但代码不得写入"单机"假设（连接拓扑经配置注入）。

## 4. 具体设计

### 4.1 key 设计

| key 形态 | 值语义 | TTL | 写入方 |
| --- | --- | --- | --- |
| `aegis:session:{session_ref}` | 会话登记：sessionRef、clusterRef、guardrailPolicyRef、创建时间、会话状态 | 会话最大空闲时长，活跃期续约 | 会话建立 |
| `aegis:budget:{session_ref}:tool_calls` | 计数器（已用工具调用数） | 与 session key 同 TTL | 每次工具调用代理后递增 |
| `aegis:budget:{session_ref}:llm_tokens` | 计数器（已用 token 数） | 同上 | LLM 调用后递增 |
| `aegis:cooldown:{cluster_ref}:{namespace}/{kind}/{name}` | 存在即冷却中，值为置位时间 | 等于目标冷却期时长 | 写操作执行成功后置位 |

命名纪律：key 内不放对象内容、不放凭据；value 为小型字段集，体量有上限。

### 4.2 预算与冷却期判定流程

1. 闸门每次放行前查询剩余预算（F4 双上限：工具调用数与 token 数，§5.2）。
2. 超限 → 会话暂停并输出当前结论与置信度（F4 处置原文），会话状态写回 session key。
3. R1+ 写执行前查冷却期 key：存在即拒绝重复动作，并提示冷却剩余（§4.2.4-4，Agent 自身作为控制回路一员必须按回路设计稳定性）。
4. 执行成功后置位冷却期并递增相应预算计数，两步合一，避免半记账状态。

### 4.3 与 CRD status 的状态分工

| 状态 | 存储 | 理由 |
| --- | --- | --- |
| 会话上下文、预算计数、冷却期 | Redis | 高频、短生命周期、可丢 |
| 变更状态机（status.state）、审批、执行进度、验证、回滚 | CRD（ChangeRequest status） | I10 在途恢复的唯一事实源；finalizer 保证清理不悬挂（§4.2.5-3） |
| 决策链全量 | DecisionRecord（§5.3） | I6 可回放，落盘不可变存储 |

### 4.4 崩溃与恢复语义

1. Redis 全丢：会话重建（用户/Agent 重入），预算计数归零重计。属可接受降级，理由两条：单机从简部署下全丢概率与影响面都小；全局变更速率有 `rateLimit.maxChangesPerHour` 在闸门侧独立兜底（执行面归熔断/配额组件，见 `internal/breaker`）。精确的会话内历史计数以 DecisionRecord 为准。
2. Redis 与 CRD 不一致：永远以 CRD 为准（I10）；Redis 只是会话视角的缓存。
3. gatekeeper 重启：不从 Redis 恢复任何执行态；Executing/Verifying 态的 CR 由 controller reconcile 重入核对集群实际状态后续作/回滚/中止（§4.2.5-3，F14 处置）。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级及理由 |
| --- | --- | --- | --- |
| Redis 不可用（连接失败 / 实例宕） | 连接错误 + 健康探测 | 新会话拒绝建立；存量会话按只读降级并标注；写路径一律阻塞 | 写 fail-closed：预算与冷却期不可证 = I7/F4 保障失效，按 I10 写路径不得放行 |
| 调用超时 | context deadline | 短暂抖动读操作可退避重试；持续超时按不可用处理 | 写 fail-closed：同上理由 |
| key 意外丢失（驱逐 / 提前过期） | 读空 | 按 §4.4 重建语义处理（重注册、计数归零） | 降级：会话态本就可重建，不传染变更态 |
| 数据格式不识别（版本迁移残留） | 反序列化校验 | 该 key 作废重建 + 告警 | 降级：单 key 级，不传染会话 |
| 计数逼近上限 | 阈值告警 | 会话内提前告知剩余额度（F4 的用户体验面） | 非故障：预算机制的常规表现 |

理由汇总：本目录的故障永远不得影响变更执行事实（事实在 CRD），只影响"会话是否还能被服务"——这正是 I10"闸门不可用 = Agent 不可用 ≠ 集群不可用"的存储层表达。

## 6. 可观测性

- 结构化日志字段：`session_ref`、`op`、`key_pattern`（key 形态，不含 value）、`latency_ms`、`error_class`、`ttl_remaining`。
- 指标：`aegis_redis_op_total{op=,result=}`、`aegis_redis_latency_seconds_bucket`、`aegis_session_active`（gauge）、`aegis_budget_remaining{budget=tool_calls|llm_tokens}`（gauge）。
- trace span：`adapters.redis.get` / `set` / `incr`，属性 `session_ref`；预算判定的业务语义 span（如 `gatekeeper.session.budget`）归 `internal/session` 侧文档。
- 审计留痕点：本目录不直接落审计；预算耗尽导致的会话暂停事件经 `internal/audit` 随会话 DecisionRecord 留痕（F4"暂停会话"的留痕要求）。

## 7. 依赖方向与模块边界

- 谁调我：`internal/session` service（会话存储端口的唯一消费者）。
- 我调谁：Redis/Valkey 实例；无其他出站依赖。
- 禁止依赖谁：禁止 import `k8s.io/*`、OPA、MCP SDK、其他适配器；禁止 import 任何上下文 domain/service（只依赖 `internal/session/ports.go` 接口）。
- 边界声明：变更执行状态、审批状态一律不进本目录（在 CRD）；熔断滑动窗口统计不进本目录（circuit-breaker 在 `internal/breaker` 自管，口径见其文档）；本目录不承载任何放行判断。

## 8. 测试策略与红队用例

- 单测：内存版 Redis 实现做契约测试（选型与引入理由随 PR 说明并钉入 `deploy/versions.md`）；TTL 过期行为、计数原子性（并发递增配 `go test -race` + goleak，`aegis-go-quality`）、冷却期存在性判定全表驱动。
- 集成：kind 内会话预算耗尽端到端（落地方案 T1.4 完成判据原文：构造超预算会话，Agent 被暂停且输出当前结论与置信度）。
- 红队用例（M2 检查点"会话预算绕过"的底层支撑）：① 伪造或重置客户端计数无效——服务端计数唯一；② 多会话分散绕过全局速率无效——rateLimit 在闸门侧独立计数；③ Redis 宕机期间一切写尝试被阻塞且告警（fail-closed 的可观测证明）。这三条对应 M2 红队检查点的预算绕过项。
