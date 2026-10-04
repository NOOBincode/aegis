# session — 会话与预算

> 所属层：L2 安全闸门/控制面 ｜ 里程碑：M1（落地方案 T1.4 双预算、T1.6 autocompact 机制侧）｜ 设计文档出处：§4.1-3、§4.3、§5.2 sessionBudget ｜ 关联不变量：I2、I10 ｜ 关联失败模式：F4、F5

## 1. 设计初衷

设计文档 §6 的两个失败模式都在本目录设防：

- F4 诊断循环失控：Agent 反复调工具不收敛，token 与 apiserver 双重失控（这是红队记录 R4 的整改落点——GuardrailPolicy 因此增加 `sessionBudget`）。
- F5 读操作打爆 apiserver：大范围 list/全量日志，需要 QPS 限流 + 结果集硬上限（429 + 分页引导）。

预算是自治的前提：M3 起 R1 全自主（T3.5），自主的 Agent 必须有确定的"油表"——预算执行判定必须是确定性逻辑（I2：LLM 不进热路径，预算判定不在例外之列），阈值来自 GuardrailPolicy 注入而非硬编码。

会话同时是审计轴：sessionRef 贯穿 ChangeRequest（§5.1 `spec.sessionRef`）与 DecisionRecord（§5.3 `spec.sessionRef`），一次诊断到修复的全部动作可经会话归并回放。没有会话模型，双预算与 taint 传递（见 `internal/sanitize/README.md`）都没有挂载点。

路径口径说明：落地方案 T1.4 写作 `internal/budget/`，设计文档 §3.2（v1.4）定为 `internal/session/`（会话与预算，§5.2 sessionBudget 执行点）。按落地方案自身的冲突规则"凡与设计文档冲突处以设计文档为准"，双预算执行统一收敛在本目录，不另立 budget 目录。

## 2. 职责与任务清单

1. 会话生命周期管理：sessionRef 生成、状态机（active/paused/closed）、会话状态存取。
2. 会话状态存储：Redis/Valkey 适配（`internal/adapters/redis` 实现 port，§3.3 选型：会话粘性、TTL 天然匹配；etcd 不适合高频会话读写）。
3. 双预算执行：工具调用数 `maxToolCalls` + LLM token 数 `maxLLMTokens`（§5.2 sessionBudget）与 apiserver QPS `maxReadQPS`（§5.2 rateLimit）的计数、判定、耗尽处置。
4. 预算耗尽暂停语义：暂停会话、通知输出当前结论与置信度（F4）、恢复路径。
5. autocompact：长会话压缩的触发判定、压缩产物存储、关键决策留存结构（prompt 侧归 `agent/`）。
6. 会话级关联面：为 sanitize taint、审计归并提供 sessionRef 挂载。

## 3. 技术选型与开源包

- 会话/状态存储：Redis/Valkey（§3.3 给定选型）；Go 客户端选型与版本以 `deploy/versions.md` 钉死为准；本目录只依赖 port 接口，不 import 具体客户端（适配器纪律）。
- 预算计数中的 apiserver QPS 令牌桶：进程本地实现（标准库），不依赖 Redis——QPS 判定必须在适配器层就近完成，断连时仍能独立工作（失效语义见 §5）。
- token 计数采集点在 L3 模型抽象层（`agent/` 的 OpenAI 兼容抽象，§4.3），判定逻辑与阈值管理在本目录；跨层衔接通道（会话存储直写或闸门 API）实现期定，若影响依赖方向则先走 ADR。
- 无 LLM SDK、无 k8s 客户端依赖；本目录是纯会话域，集群面经 controller 编排交互。

## 4. 具体设计（不写代码）

### 4.1 会话模型

- sessionRef 生成：CLI/Agent 会话建立时签发，之后贯穿：每次工具调用携带、CR `spec.sessionRef`（§5.1）、DR `spec.sessionRef`（§5.3）、sanitize taint 登记。
- 会话状态值对象（语义描述）：`{sessionRef, createdAt, status=active|paused|closed, budgetCounters{toolCallsUsed, tokensUsed}, compactCount, lastCompactAt}`；taint 引用与快照本体同库存储。
- 存储纪律：会话键设 TTL（≥ 会话生命周期，closed 后保留至审计组装完成）；预算是每会话独占计数，不做跨会话共享池（共享池会让单会话失控无法被切断，违背 F4 的处置精度）。

### 4.2 双预算执行的埋点位置

| 预算 | 埋点位置 | 计数来源 | 判定位置 |
| --- | --- | --- | --- |
| 工具调用数 `maxToolCalls` | 闸门 MCP 代理（`internal/mcpserver` 限流中间件） | 每次工具调用进出各计一次 | 本目录 service（读 Redis 计数 + GuardrailPolicy 阈值） |
| LLM token 数 `maxLLMTokens` | L3 模型抽象层采集（`agent/`），写入会话计数 | LLM 响应 usage 确定性上报 | 本目录 service（同左） |
| apiserver QPS `maxReadQPS` | `internal/adapters/k8s` 客户端封装（令牌桶） | 每会话滑动窗口内读请求数 | 适配器本地（就近判定，超限 429 + 分页引导，F5） |

阈值一律从 GuardrailPolicy 注入（§5.2），每次判定前读 informer 缓存的最新配置——改 GuardrailPolicy 即改预算，无需重启。

**调用链时序**：工具调用进入 → mcpserver 中间件请求会话预算检查（本目录 service）→ 通过则放行并计数 → 返回时更新计数；写准入在 controller 侧再校验一次会话状态（paused/closed 会话的新写提议直接拒绝）。预算检查失败与闸门检查失败的优先级：熔断/硬拒等安全判定优先于预算判定（安全永远先于成本）。

### 4.3 预算耗尽的暂停语义（F4）

F4 处置原文："暂停会话，输出当前结论与置信度"。展开为：

1. 会话置 paused；正在执行的写路径动作处理：会话内新工具调用一律拒绝（budget_exhausted 错误码），但在途 ChangeRequest 必须走完终态（I10 在途恢复语义——有副作用的操作不能挂半空）；controller 的验证/巡检查询属系统回路，走独立配额，不受会话预算影响（系统回路不能因会话预算耗尽而悬挂）。
2. 通知 L3 输出当前结论与置信度：autocompact 机制侧保证此时仍有"当前结论 + 关键决策"可用（见 4.5）。
3. 恢复路径：人工提高 GuardrailPolicy `sessionBudget` 或显式 resume（CLI），resume 事件留痕（审计见 §6）。

### 4.4 Redis/Valkey 失效语义

分层失效设计（与 §5 呼应）：QPS 令牌桶进程本地，Redis 断连时读限流仍工作；会话级计数（工具调用/token）在 Redis，断连时无法证明"还有预算"——**判定上视为耗尽**：新写路径阻塞、新会话暂停，只读诊断可降级继续但会话标注 degraded_budget。理由：I10 的写路径 fail-closed 精神在预算面的直接应用；把"计数不可信"当"预算充足"是拿 apiserver 和钱包赌博（F4/F5 的失控面正是我们设防的对象）。

### 4.5 autocompact：摘要 + 关键决策留存

§4.3 上下文工程给定"长会话走 autocompact（摘要+关键决策留存）"。与 `agent/` 的分工红线：**机制在本目录，prompt 在 `agent/`**。

- 本目录负责：压缩触发判定（会话上下文 token 估算超阈值，估算口径实现期定并复用 token 预算口径）、压缩产物存储（会话键内新世代）、留存结构定义、向 L3 提供"压缩后上下文装配"接口。
- `agent/` 负责：摘要生成的提示词、关键决策选取的提示词工程（见 `agent/README.md`）。
- 关键决策留存结构（机制层强制的最小集，每条）：`{decision, conclusion, confidence, evidenceRefs, constraints}`；其中 constraints 必须带：未决 CR 引用、熔断态、冷却期对象（`internal/authority` 产出）、taint 数据块引用（`internal/sanitize` 产出）——这些是闸门正确性的输入，不是可摘要的细节，压缩不得丢弃。
- 审计正交：autocompact 只压缩 prompt 上下文，全量事件流仍落 DecisionRecord（I6）——压缩是省 token 的机制，不是省留痕的借口。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| Redis/Valkey 断连（会话计数不可达） | 适配器健康探测 | 写路径阻塞 + 新会话暂停；只读诊断降级继续并标注 degraded_budget | **fail-closed 于写面**。无法证明有预算即视为耗尽（I10 精神在成本面的应用） |
| GuardrailPolicy 的 sessionBudget 非法（`maxToolCalls` ≤ 0 等） | GuardrailPolicy 准入校验 | 拒绝该更新，沿用上一份有效配置 + 告警 | fail-closed 于配置面。非法预算生效 = 预算机制失效（F4 防线塌方） |
| token 采集延迟导致瞬时超限 | 事后核对（Langfuse 精确值 vs 会话计数） | 已发生的读操作保守放行并告警；写操作面本就已被预算拦截，不追惩 | 降级。采集管道延迟是工程现实，追惩无意义，告警提示口径修偏即可 |
| 会话状态竞态/序号乱序 | MCP 信封会话内序号校验（F15 同源） | 乱序拒绝 | fail-closed。会话面防重放防串话是 F15 的内核版 |
| autocompact 产物写入失败 | 写错误返回 | 不切换上下文世代，沿用旧上下文继续 + 告警；连续失败则按预算耗尽路径暂停 | 降级。压缩是优化不是正确性前提，失败回退到"不压缩" |
| 快照本体存储失败（审计协作面） | `internal/audit` 完整性校验反馈 | 按 audit 的留痕失败姿态联动（见 `internal/audit/README.md` §5） | 单一决策点，不自行发明姿态 |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志字段：
  - `session_created{session_ref, policy_ref, budget_snapshot}`
  - `budget_exhausted{session_ref, budget_type=tool_calls|tokens|qps, used, limit}`
  - `session_paused{session_ref, reason=budget_exhausted|manual|storage_degraded}`
  - `session_resumed{session_ref, by}`
  - `compact_triggered{session_ref, before_tokens, after_tokens, decisions_kept}`
- 指标：`aegis_session_active`（gauge）、`aegis_session_budget_exhausted_total{budget_type}`、`aegis_session_tool_calls_total`、`aegis_apiserver_qps_throttled_total`、`aegis_session_compact_total`、`aegis_session_storage_degraded`（gauge，Redis 断连期置 1）。
- trace span：`gatekeeper.session.budget_check`（每次工具调用预算判定子 span）、`gatekeeper.session.compact`（压缩 span）。
- 审计留痕点：耗尽/恢复/resume 事件落会话关联的 DecisionRecord（会话 outcome 的组成部分）；GuardrailPolicy 预算配置变更本身经 K8s audit log 留痕（谁改了上限，F4 防线的人为面）；degraded_budget 标注进会话结论，随 DR 永久可回放。

## 7. 依赖方向与模块边界

- **谁调我**：`internal/mcpserver`（每次工具调用的预算中间件）；`internal/controller`（写准入时校验会话状态）；`cmd/aegis-cli`（resume/status 子命令装配）；`agent/` 侧模型抽象层（token 采集上报，跨层经端口/API 衔接，不反向 import Go 内部包——agent 是 L3 独立世界）。
- **我调谁**：`internal/adapters/redis`（会话存储，经 port）；`internal/audit`（预算事件留痕，经 service 协作）。
- **禁止依赖谁**：`internal/adapters` 具体实现（尤其 k8s 适配器——QPS 桶虽在适配器内，但策略与阈值由本目录注入配置，不构成反向依赖）、其余上下文 domain、任何 LLM SDK。
- 五铁律对照：domain（预算判定、会话状态机、压缩规则，纯单测）+ ports（会话存储接口）+ service（预算检查与暂停用例）；领域文件不 import `k8s.io/*`、Redis 客户端。

## 8. 测试策略与红队用例

- 领域纯单测（表驱动）：预算判定边界（恰达上限放行、超 1 拒绝）、暂停语义（在途 CR 豁免与系统回路配额分离）、压缩规则（关键决策最小集不丢、constraints 齐全）。覆盖率 ≥80%（L2 安全包，`internal/session` 在列）。
- 完成判据锚定 T1.4：构造超预算会话，Agent 被暂停且输出当前结论与置信度。
- F5 集成：大范围 list 触发 429 + 分页引导（kind 实测）。
- Redis 断连演练：写路径阻塞 + 只读降级标注（I10 杀进程/断依赖测试清单项）。
- 红队用例（红队检查点 #2 含"会话预算绕过"）：
  1. 绕中间件直调底层工具/集群：协议层会话凭证与序号校验拦截（F15 交叉）；
  2. 多会话分摊逃避单会话上限：预算是每会话独占设计，跨会话共享池不存在，构造此类用例验证判定；
  3. 预算耗尽瞬间的在途写 CR：走完终态不悬挂，新提议被拒；
  4. autocompact 绕过留痕：压缩后回放 DR，全量事件链完整（I6 正交性验证）。
