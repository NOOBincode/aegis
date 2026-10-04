# mcpserver — MCP server 共享框架（工具注册、元数据强校验、统一中间件链）

> 所属层：L1 工具层共享框架（服务 `mcp-servers/*` 各进程） ｜ 里程碑：M1（T1.1 骨架）→ M2 收敛进闸门统一执行（T2.7/T2.8） ｜ 设计文档出处：§4.1 ｜ 关联不变量：I3（工具即权限边界）、I4（读路径消毒）、I10（超时 fail-closed） ｜ 关联失败模式：F2（读路径注入）、F4（会话预算）、F5（读打爆 apiserver）、F15（重放）

## 1. 设计初衷

`mcp-servers/` 下每个子目录是一个独立进程（k8s-read / k8s-write / prom / logs / events / gitops / runbook），但"如何成为一个合格的 aegis 工具"是共性问题：工具元数据五字段必须强校验、每个出口必须过 sanitizer 包裹、每个调用必须限流与审计埋点、每个调用必须有超时。把这些横切能力收进 `internal/mcpserver` 共享框架，保证三件事：其一，任何工具作者省不掉安全动作——包裹、埋点、限流在中间件链里默认执行，绕过框架等于绕过编译期入口；其二，工具层只做"参数校验 + 执行 + 结果结构化返回"，不含任何放行判断（设计文档 §4.1 职责边界，放行永远在 L2 闸门）；其三，M1 尚无完整 L2 闸门时，I4 的消毒以框架中间件形态先行落地（T1.5 的执行层说明），M2 收敛进闸门统一执行时不改工具代码。

## 2. 职责与任务清单

职责：

1. 提供工具注册流程与注册表，元数据五字段缺一即注册失败（注册期强制，非运行期发现）。
2. 提供统一中间件链：鉴权信封校验 → 限流 → sanitize 包裹 → 审计埋点 → 超时控制。
3. 定义工具结果结构化返回与错误协议（工具错误 vs 协议错误的区分）。
4. 承载 MCP 通道安全：mTLS + 签名信封 + 会话内序号（防重放防串话，F15 的通道侧）。
5. 给 `mcp-servers/*` 提供进程骨架（生命周期、健康检查、OTel 接入），使各进程只有 handler 差异。

任务清单：T1.1（MCP 工具框架与注册机制，完成判据=空壳 server 注册成功且被 kagent 发现、缺元数据的工具被拒绝注册）、T1.5 的框架侧接入（包裹库函数挂进中间件）、T2.7（sanitizer 收敛进闸门统一执行）、T2.8（k8s-write 工具"仅接受 ChangeRequest ID"的协议层拒绝）、T2.11（信封序号防重放）。

## 3. 技术选型与开源包

- MCP SDK（Go）：工具协议的事实标准（设计文档 §3.3），具体版本以 `deploy/versions.md` 钉死为准。
- OTel SDK：trace 贯穿会话→工具→集群。
- 限流与超时基于标准库 + 进程内令牌桶实现，不引第三方限流库（能用标准库解决不加依赖）。
- sanitize 中间件调用 `internal/sanitize` 的 service（经 service 协作，铁律 4），审计埋点调用 `internal/audit`。
- 不引入通用 HTTP/gRPC 框架：本框架就是薄封装，避免为框架而框架。

## 4. 具体设计（不写代码）

工具注册流程（编号步骤）：

1. 工具作者定义 handler：名称、入参 schema（强校验）、出参语义、元数据五字段。
2. 框架对元数据做注册期校验：`risk_hint`（R0–R3 初判）/ `idempotent`（bool）/ `reversible`（bool）/ `est_blast_radius`（pods/nodes/namespaces 估算）/ `timeout_ms`（执行超时上限）——缺一字段或字段值非法（如 risk_hint 不在 R0–R3）→ 注册失败并给出缺失项（T1.1 完成判据）。
3. 校验通过的工具挂入统一中间件链后进注册表，向 kagent ToolServer 暴露。
4. 闸门（L2）据注册表元数据做分级复核：LLM 初判仅作参考，终判取查表与初判中更高者（设计文档 §4.2.2，I1）。

统一中间件链（每个工具调用固定顺序经过，顺序即语义）：

1. 鉴权信封校验：mTLS 通道身份 + 签名信封验签 + 会话内序号检查（乱序/重发拒绝，F15）。
2. 限流：会话 token 预算与 apiserver QPS 预算双上限检查（F4/F5），超限返回限流错误并告知预算口径。
3. sanitize 包裹：工具出口返回数据统一包裹 `<untrusted_cluster_data>` 分隔符；读路径注入模式命中→告警 + 标记该数据衍生动作升 R2（I4/F2，M2 起检测，M1 先只做标记+留痕）。
4. 审计埋点：调用方、工具名、参数摘要（哈希）、结果摘要写入审计流，供 `internal/audit` 组装 DecisionRecord（I6）。
5. 超时控制：以工具元数据 `timeout_ms` 为上界包住执行；超时即失败返回，绝不无限挂起（I10 的通道侧）。

工具结果结构化返回：统一返回体分三段——`data`（业务结果，读工具带分页游标与截断标记）、`meta`（是否截断、包裹标记、剩余预算）、`error`（见错误协议）。读工具强制分页与上限（单次 ≤500 行日志 / ≤200 对象，设计文档 §4.1）。

错误协议（工具错误 vs 协议错误，二者不混用）：

- 协议错误（调用本身不成立）：参数 schema 校验失败、信封验签失败、序号乱序、未注册工具名、限流拒绝。返回协议级错误码，不触达 handler。
- 工具错误（调用成立但执行失败）：集群对象不存在、超时、apiserver 返回错误。由 handler 返回结构化工具错误，带 `error_class` 与是否可重试标记；**工具错误不得被框架解释为"降级放行"**——k8s-write 的任何工具错误一律向上抛给闸门状态机处置。

写工具的骨架约束：`mcp-servers/k8s-write` 的 scale_deployment / restart_pod / cordon_node 只接受 ChangeRequest ID；带自由参数的调用在协议层被拒绝（T2.8 完成判据）。执行侧走乐观并发（resourceVersion / SSA field-manager 冲突检测，检出即放弃重新归因，不蛮写重试）。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态 |
| --- | --- | --- | --- |
| 参数注入/schema 绕过 | 协议层强 schema 校验 | 协议错误拒绝，记审计并计数（红队素材） | fail-closed |
| 重放/串话（复制信封重发、序号乱序） | 签名信封 + 会话内序号检测 | 拒绝并告警，留痕（F15，M2 红队检查点项） | fail-closed |
| 会话预算 / QPS 耗尽 | 限流中间件双预算检查 | 暂停并告知用户当前结论与置信度（F4）；超限读返回 429 + 分页引导（F5） | 拒绝/暂停 |
| 工具执行超时 | timeout_ms 上界的 context 超时 | 取消执行，返回工具错误（可重试标记由 handler 语义决定）；写工具超时不产生"部分成功"幻觉，结果交闸门状态机核对实际状态 | fail-closed（I10） |
| 下游 apiserver 不可用 | 调用错误分类 | 读路径可降级直连并明确标注降级态；写路径一律阻塞/拒绝并告警 | 写 fail-closed，读可降级 |
| sanitize 注入命中 | 模式库命中 | 包裹 + 告警 + 衍生动作升 R2（F2/I4），事件落 DecisionRecord `sanitizationEvents` | 升级审批 |
| 框架自身 panic | 每调用 recover 包裹 | 转为协议错误，进程不死于单次调用；连续 panic 触发健康检查失败 | 隔离降级 |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志字段：`tool_name`、`tool_category`（read/write/gitops）、`caller_identity`、`session_ref`、`seq_no`、`params_hash`、`result_truncated`、`sanitized`、`error_class`、`latency_ms`、`timeout_ms`。
- 指标：`aegis_mcp_tool_calls_total{tool, category, error_class}`、`aegis_mcp_rate_limited_total{kind}`（session_budget / qps）、`aegis_mcp_envelope_rejected_total{reason}`（replay / out_of_order / bad_signature）、`aegis_mcp_tool_timeout_total{tool}`、`aegis_mcp_result_truncated_total{tool}`。
- Trace span 命名：`mcp.tool.invoke`、`mcp.middleware.authn`、`mcp.middleware.ratelimit`、`mcp.middleware.sanitize`、`mcp.middleware.audit`，与 OTel 全链路（会话→闸门→工具→集群）同一 trace 贯穿。
- 审计留痕点：每次注册（含元数据五字段快照，变更即新审计事件）、每次调用的鉴权结论、限流命中、包裹标记与注入命中、超时与工具错误——全部喂给 `internal/audit` 组装 DecisionRecord。

## 7. 依赖方向与模块边界

谁调我：`mcp-servers/*` 各进程（import 本框架完成注册与启动）；`cmd/` 的装配入口实例化框架。

我调谁：

- `internal/sanitize`、`internal/audit`、`internal/session` 的 service（经 service，不触 domain，铁律 4）。
- MCP SDK 与 OTel SDK（本层是框架层，允许协议依赖；上下文层不允许）。

禁止依赖谁：

- 禁止 import `internal/controller`（方向：controller 编排上下文，不感知 MCP）。
- 禁止在框架内实现任何放行/分级判断（I1/I3：放行只在 L2 闸门；框架只做通道安全、限流、包裹、埋点、超时）。
- 框架不得依赖具体工具实现，`mcp-servers/*` 依赖框架，反向禁止。
- 本层不属 depguard `context-purity` 的上下文清单，但仍受铁律 1 的单向依赖约束；框架代码改动随 PR 过 I1–I10 检查单。

## 8. 测试策略与红队用例

- 单测：注册期元数据校验（每个字段各造一个缺失/非法用例，证明注册失败）；错误协议分类（协议错误不触达 handler、工具错误可携带可重试标记）；中间件链顺序与短路语义。
- 集成（kind）：空壳 server 注册成功且被 kagent 发现；超限读请求返回 429 + 分页引导；带自由参数调 k8s-write 被协议层拒绝。
- 红队用例（新写工具准入单与本框架直接相关，每加一个写工具过一遍）：
  1. 参数注入：向读/写工具注入畸形与恶意参数，全部被 schema 拒绝并留痕。
  2. 越权目标：诱导拼出 R3 操作变体（如借 gitops 工具改 ArgoCD 管资源的集群直写），通道侧无法绕过"写工具只接受 CR ID"。
  3. 幂等重放：复制 MCP 信封重发、打乱序号，全部被拒且告警（F15）。
  4. 会话预算绕过：多工具并发分摊消耗试图绕过单调用限流，双预算按会话聚合计数仍然命中。
- M2 检查点 #2 的全量项（审批绕过/日志注入/越权/重放/预算绕过）以本框架为测试靶面之一；M1 检查点 #1（日志注入初测）验证包裹隔离 + 留痕 + 不产生写意图。
