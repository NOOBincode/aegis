# cmd/gatekeeper — L2 安全闸门主服务进程

> 所属层：L2 安全闸门（控制面）｜ 里程碑：M2（读路径消毒以库形态在 M1 先行，T1.5；M2 收敛进闸门统一执行，T2.7）｜ 设计文档出处：§3.1、§4.2、§4.2.5 ｜ 关联不变量：I1、I4、I5、I7、I10 ｜ 关联失败模式：F1、F2、F5、F14、F15

## 1. 设计初衷

gatekeeper 是 Agent 与集群之间唯一的写路径，也是读路径的统一出入口（设计文档 §3.1 数据流要点：Agent 的一切工具调用**强制途经 L2 闸门**，MCP 代理模式，运行时无法绕过）。它是本项目差异化所在（设计文档 §1.5：护城河钉死在 L2 安全闸门与 L4 评估层），因此该进程的装配必须满足三条硬约束：

- **同步判定链路在此**：风险分级、策略求值、审批前置检查、配额、熔断判断全部在请求的同步路径上完成，保证 R1 操作的交互闭环（设计文档 §4.2.3）。
- **无状态**：进程不持有不可恢复状态，分级、审批、执行进度、回滚窗口全落 CRD status（§4.2.5-1），崩溃后可被替换而不丢事实（I10/F14）。
- **业务零内嵌**：判定规则全部在 `internal/` 各上下文，本目录只做装配，便于 R-2 全人审聚焦。

## 2. 职责与任务清单

**本进程装配的 internal 上下文**（对应组件见设计文档 §4.2.2）：

| 上下文 | 组件 | 在闸门链路中的位置 |
| --- | --- | --- |
| `internal/risk` | risk-classifier | 判定流水线第 2 步 |
| `internal/policy` | opa-eval（端口；OPA 本体在 adapter） | 第 3 步 |
| `internal/authority` | authority-map / attributor | 第 4 步与在途观察 |
| `internal/rollback` | rollbacker | 第 5 步 |
| `internal/breaker` | circuit-breaker + 爆炸半径配额 | 第 6、7 步 |
| `internal/approval` | approval-svc（服务端状态机，配合 controller） | 第 8 步 |
| `internal/sanitize` | log-sanitizer | 读路径出口 |
| `internal/session` | 会话与预算（§5.2 sessionBudget 执行点） | 入口第 1 步 |
| `internal/audit` | 决策链留痕 | 全链路旁路 |

**装配的 adapters**：`internal/adapters/k8s`（client-go，读写 apiserver）、`internal/adapters/opa`、`internal/adapters/mcp`（对 L1 工具层的出站调用）、`internal/adapters/redis`（会话/预算计数，§3.3 选型）、`internal/adapters/langfuse`（LLM 调用链引用上送）。

**进程职责清单**：① MCP 代理监听（含 mTLS、签名信封、会话内序号，§4.2.5-4）；② 写路径同步判定流水线；③ ChangeRequest 落库（idempotencyKey 派生 CR 名，F15）；④ 读路径代理（消毒 + 双限流 + 分页引导）；⑤ 读路径降级直连开关与降级态标注（I10）；⑥ 探针与优雅退出（骨架见 `cmd/README.md` §4）。

**任务对照**：T2.2（risk-classifier）、T2.3（opa-eval）、T2.4（approval-svc 联调）、T2.5（rollbacker）、T2.6（breaker/quota）、T2.7（sanitizer 收敛）、T2.11（幂等与失效语义）、T2.12（冷却期校验的配合执行点）。

## 3. 技术选型与开源包

| 项 | 选型 | 理由 | 放弃项及原因 |
| --- | --- | --- | --- |
| 工具协议 | **MCP**（Go SDK 具体模块与版本以 `deploy/versions.md` 钉死为准；注册/元数据强校验复用 `internal/mcpserver` 共享框架，T1.1） | 设计文档 §3.3：MCP 已是 Agent↔工具事实标准，kagent 直接兼容；代理形态使运行时无法绕过闸门 | 私有 RPC：生态自杀（§3.3 原文） |
| K8s 客户端 | client-go（版本以 `deploy/versions.md` 为准） | 与 controller 同一代码线，版本对账一致（aegis-go-quality 依赖纪律） | — |
| 策略引擎 | OPA 经 `internal/adapters/opa` 调用；上下文中只有 port | 铁律 2：OPA 是实现细节，不得进 domain | 在闸门内直嵌 OPA API：违反依赖方向 |
| 会话/预算存储 | Redis/Valkey（§3.3 选型） | 会话粘性、TTL 天然匹配 | etcd：不适合高频会话读写（§3.3 原文） |
| LLM 客户端 | **不内嵌** | 闸门不调模型：LLM 风险初判以工具元数据 `risk_hint` 形式随调用进入，终判取查表与初判的更高者（§4.2.2，I1） | 闸门直连模型：LLM 进判定热路径，违反 I2 精神 |
| flag/配置/日志/OTel | 统一骨架选型见 `cmd/README.md` §3（pflag、env+flag 双层、log/slog、OTel SDK） | 三进程一致 | viper：重型依赖，禁 |

## 4. 具体设计（不写代码）

**MCP 代理监听形态**：集群内 Service 暴露 MCP 端点；L3 运行时（kagent 二开）的工具出口在网络层只指向闸门（与 F9 网络策略同思路：能直连者被凭证/网络双层封死）。通道安全三件套（§4.2.5-4）：mTLS 双向认证、签名信封、会话内单调序号——防重放、防串话。

**写路径同步判定流水线**（收到写意图后的编号步骤）：

1. 信封校验：签名、会话序号窗口（乱序/重放直接拒绝并留痕，F15）。
2. 会话预算检查：`internal/session` 计数工具调用数与 token 数（§5.2 sessionBudget），耗尽即暂停会话并返回当前结论与置信度（F4）。
3. risk-classifier 分级：资源类型 × 动词 × 作用域确定性查表；与工具元数据 `risk_hint` 初判取高；输出 R0–R3 与 estBlastRadius（§4.2.2）。
4. R3 硬拒：命中 GuardrailPolicy `hardDeny` 或查表判 R3 → 拒绝、留痕、告警，无审批入口（§4.2.1）。
5. opa-eval：manifest 过 Rego 策略包（§5.2 `opaPolicyBundle` 指向 `policies/`）；超时或引擎不可用 → 拒绝（fail-closed，见 §5）。
6. authority-map 字段所有权核查：目标关键字段有 controller 所有者 → 拒直写，意图翻译为对 owner API 的操作建议或拒绝（I9/§4.2.4-1），核查结果写入 CR `authorityCheck`。
7. 爆炸半径配额：estBlastRadius 对 GuardrailPolicy `blastRadiusQuota` 硬上限（I7），超限拒绝。
8. circuit-breaker：熔断状态为 open 时 R1+ 一律拒绝并提示降级态（§4.2.2）；变更频率阈值同在此检查。
9. rollbacker：生成 inverseOperation 并 dry-run 验证；无法生成或验证失败 → 自动升 R2（I5/红队 R2 整改）。
10. 落 ChangeRequest：CR 名由 `idempotencyKey` 哈希派生（F15）；状态初始按级别置 Pending/DryRunning/AwaitingApproval；R2 附带诊断链 + 影响面报告供人审（§4.2.1）。
11. 同步返回：向 Agent 返回判定结果（放行指令 / 等待审批 / 拒绝原因）。**执行不在闸门**：放行后的窄调用由 controller 状态机推进（见下"职责切分"）。

**读路径**：代理 → 双限流（会话 token 预算 + apiserver QPS 预算，F5）→ 结果集硬上限（单次 ≤500 行日志 / ≤200 对象，§4.1）→ log-sanitizer 包裹 `<untrusted_cluster_data>` 并做注入模式检测（I4）→ 返回。命中注入模式 → 告警，且该数据衍生的任何动作自动升 R2（F2）。

**读路径降级开关**：当闸门依赖（apiserver、redis）不可用且配置允许时，Agent 运行时可降级为只读直连（I10）。开关要求：①降级是显式配置行为，非自动静默切换；②降级期间一切写仍 fail-closed；③降级态标注进所有降级期响应与后续 DecisionRecord（`sanitizationEvents` 旁注明降级窗口）。

**无状态与副本**：状态全在 CRD status；多副本 standby，leader election 保证单写者（§4.2.5-1）。

**与 controller 的职责切分**：

| 维度 | gatekeeper（本进程） | controller（`cmd/controller/README.md`） |
| --- | --- | --- |
| 路径性质 | 同步判定链路（请求内完成） | 异步状态机推进（reconcile） |
| 持有的状态 | 零业务状态，只有配置与缓存 | CR 状态机事实，status 唯一写者 |
| 写 apiserver | 创建 ChangeRequest（spec 与初始 status） | 推进 status、写 finalizer、写 DecisionRecord 关联 |
| 执行与验证 | 不执行写操作 | 驱动 Executing→Verifying→Committed/RolledBack，在途恢复（§4.2.5-3） |
| 交互方式 | 经 apiserver 的 CR 契约，**无私有 RPC** | 同左 |

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| OPA 求值超时 / 引擎不可用 | 每次求值带超时（aegis-go-quality：闸门对 OPA 每次调用必须有超时） | 该写操作拒绝 + 告警 | **fail-closed**（I1/I10）：策略面是 F1 的防线，防线失效时放行 = 一票否决的 bug |
| 与 apiserver 分区 / client 错误 | 调用错误与超时 | 写路径阻塞 + 告警；读路径按 §4 开关降级直连并标注 | 写 fail-closed、读可降级（I10 原文）；闸门不可用 = Agent 不可用 ≠ 集群不可用 |
| 信封重放 / 序号乱序 | 会话内序号窗口比对 | 拒绝 + 留痕 + 计数 | fail-closed（F15）；序号乱序拒绝为 §6 指标 |
| 会话预算耗尽 | `internal/session` 双计数 | 暂停会话，返回当前结论与置信度 | 受控降级（F4）；只停会话不停集群 |
| QPS / 结果集超限 | 令牌桶 + 硬上限 | 429 + 分页引导 | 受控拒绝（F5），保护 apiserver |
| 注入模式命中 | log-sanitizer 模式库 | 包裹隔离 + 告警 + 衍生动作自动升 R2 | 升级审批（I4/F2），不静默丢弃也不放行原文 |
| 熔断器 open | breaker 状态检查 | R1+ 全拒，提示只读降级态；复位走 CLI 显式操作（F7） | fail-closed；复位权限在人不在 Agent |
| 逆操作生成 / dry-run 失败 | rollbacker 验证结果 | 自动升 R2（人审） | 宁可升级不裸奔（I5/红队 R2） |
| 目标字段有 controller 所有权 | authority-map 非空 owner | 拒直写；翻译为 owner API 操作意图或拒绝 | I9/F3：不与健康控制器争夺字段所有权 |
| 闸门进程崩溃 | 进程消失，K8s 重启 | 在途 CR 由 controller 重入核对：已生效补验证，未生效续作/回滚/中止 | I10/F14：进程无状态，死亡不造成悬挂 |
| 重复提交（网络重试 / 消息重投） | idempotencyKey 哈希派生 CR 名 | 返回已存在的同一 CR，不产生第二次执行 | F15 折叠语义 |

总原则：写路径一切不确定 = 阻塞或拒绝；读路径可降级但必须显式标注；闸门的任何故障不得静默放行（I10）。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

**结构化日志字段**（slog JSON，业务字段在装配时由对应上下文注入，字段名为约定）：

| 字段 | 含义 |
| --- | --- |
| `session_ref`、`envelope_seq` | 会话与信封序号（F15 排查） |
| `tool_name`、`tool_args_digest` | 工具名与参数摘要（**不记全量参数**，防 secret 明文进日志） |
| `risk_level_final`、`risk_level_hint` | 查表终判与 LLM/元数据初判（I1 取高审计） |
| `decision`、`deny_reason` | allow / deny / block 及拒绝原因码 |
| `cr_name`、`idempotency_key_hash` | CR 关联与幂等键摘要 |
| `blast_radius`、`quota_check` | 影响面估算与配额判定（I7） |
| `degraded` | 是否处于读降级态 |
| `sanitize_hits`、`sanitize_patterns` | 注入命中数与命中模式编号（F2） |
| `opa_eval_ms`、`eval_timeout` | OPA 求值耗时与超时标记 |

**指标**：

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `aegis_gate_decisions_total{result=allow|deny|block,risk_level=}` | counter | 判定结果分布 |
| `aegis_gate_opa_eval_timeout_total` | counter | OPA 超时次数（fail-closed 触发率） |
| `aegis_gate_opa_eval_duration_seconds` | histogram | OPA 求值耗时 |
| `aegis_sanitize_injections_total{pattern=}` | counter | 注入模式命中（F2） |
| `aegis_mcp_envelope_rejects_total{reason=}` | counter | 重放/乱序/验签拒绝（F15） |
| `aegis_gate_degraded_read_total` | counter | 降级直连读次数 |
| `aegis_gate_breaker_open` | gauge | 熔断器状态 1/0（F7 监控） |
| `aegis_cr_created_total{risk_level=}` | counter | CR 创建数（F15 折叠后计数） |

**Trace span 命名**：`gatekeeper.session.authenticate`（信封/mTLS）→ `gatekeeper.risk.classify` → `gatekeeper.opa.eval` → `gatekeeper.authority.check` → `gatekeeper.rollback.plan` → `gatekeeper.cr.create`；读路径：`gatekeeper.read.proxy`、`gatekeeper.sanitize.wrap`。span 属性携带 `session_ref`、`cr_name`、`risk_level`，与 DecisionRecord 的 `llmTraceRef`（§5.3）对账。

**审计留痕点**：① CR 创建即留痕，`auditRef` 关联 DecisionRecord；② `sanitizationEvents` 全量进 DecisionRecord（§5.3）；③ R2 审批请求附带诊断链 + 影响面报告（§4.2.1）；④ 降级开关切换动作留痕（操作者、时间、原值、新值）；⑤ R3 硬拒留痕 + 告警（F1）。

## 7. 依赖方向与模块边界

- **谁调我**：L3 Agent 运行时（kagent 二开）经 MCP 协议强制途经（§3.1）；aegis-cli 不调本进程（CLI 走 apiserver 视图，见 `cmd/aegis-cli/README.md`）。
- **我调谁**：apiserver（建 CR、读 GuardrailPolicy）；`internal/` 各上下文 service（经装配）；adapters（opa / mcp / redis / langfuse / k8s）；L1 工具层经 MCP 出站调用（进程间协议，非 Go import）。
- **禁止依赖谁**：禁止 import `mcp-servers/*` 的 Go 包（工具执行是独立进程，见 `mcp-servers/README.md`，闸门只经 MCP 协议触达）；禁止 import `internal/controller` 的 reconcile 实现（状态机推进在 controller）；禁止 import 各上下文 `domain` 之外的私货——业务规则不得上移到装配层；禁止内嵌 LLM SDK（§3）。
- **depguard 校验点**：本进程源码只允许出现在 cmd 白名单方向内；上下文 purity 规则约束的是 `internal/` 各包，本进程是规则的受益方而非豁免方。

## 8. 测试策略与红队用例

本目录为装配层，无单测义务（覆盖率要求落在 `internal/` L2 包 ≥80%，aegis-go-quality）。验证手段：

1. **kind 集成（M2 验收本体）**：三连演示脚本化（T2.13）——scale 全链路、恶意日志注入不越权且告警、删 namespace 硬拒。
2. **fail-closed 用例（T2.11 完成判据）**：杀 gatekeeper 进程 / 断 apiserver 分区 → 写路径一律阻塞 + 告警，绝不放行；读降级态标注可观测。
3. **幂等用例（F15）**：同一 idempotencyKey 重复提交返回同一 CR；复制 MCP 信封重发被拒绝；序号乱序被拒绝。
4. **红队检查点 #2 映射**（M2 准入门槛，设计文档 §10）：审批绕过（闸门不持有状态机，直改 status 无效——配合 `cmd/controller/README.md` §8 用例）、重放（用例 3）、日志注入（演示②）、越权 R3 变体（演示③）、会话预算绕过（F4 双计数在服务端）。
5. **人工审查**：本进程装配的 L2 包全部适用 R-2 逐行人审，PR 标注 AI 产出占比 + 人审人。
