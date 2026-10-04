# internal/adapters/mcp — Agent↔工具 server 的强制代理通道

> 所属层：控制面/适配器层（L2 闸门与 L1 工具层之间的唯一通道）｜ 里程碑：M1（通道建立）–M2（强制代理 + 信封安全）｜ 设计文档出处：§3.1、§3.3、§4.1、§4.2.5-4 ｜ 关联不变量：I3（工具即权限边界）、I10-4（幂等与防重放）｜ 关联失败模式：F15（重试双执行）、F4（诊断循环失控）

## 1. 设计初衷

设计文档 §3.1 数据流要点：Agent 的一切工具调用强制途经 L2 闸门（MCP 代理模式，运行时无法绕过）。本目录是这条强制链路的协议实现。若 Agent 可直连工具 server，闸门即被架空，I1/I3 同时失效——所以本适配器的存在理由不是"协议转换"，而是"物理上只此一条通道"。它还承载 I10-4/F15 的通道安全：mTLS + 签名信封 + 会话内序号，防重放防串话（§4.2.5-4 原文）。LLM 幻觉（F1）与注入诱导（F2/F4）的写意图，都要先过这条通道才被讨论是否放行。

## 2. 职责与任务清单

1. 强制代理拓扑：Agent 侧仅配置闸门一个 ToolServer 入口；闸门内维护到各 `mcp-servers/`（k8s-read / k8s-write / prom / logs / events / gitops / runbook）的出站连接。
2. 通道安全：mTLS 双向认证、签名信封编解码、会话内序号校验、重放检测（I10-4）。
3. 参数形状强制：write 类工具调用参数仅为 ChangeRequest ID 引用（`cr_ref`），自由参数在协议层拒绝（§4.1、新写工具准入第 2 条）。
4. 超时分级：以工具元数据 `timeout_ms` 为准的分级超时（§4.1 元数据五字段之一，注册时强制填写，缺一注册失败——T1.1 完成判据）。
5. 会话计量：每次代理调用上报 `internal/session` 的 `sessionBudget.maxToolCalls` 预算（F4 执行点）。
6. 结果回送：工具结果原样回送；读结果的 `<untrusted_cluster_data>` 包裹在闸门侧执行（M1 为工具出口库形态，落地方案 T1.5；M2 起收敛进闸门统一执行，T2.7），不在本目录。

## 3. 技术选型与开源包

- 官方 Go MCP SDK：设计文档 §3.3 选型原文——MCP 已成为 Agent↔工具事实标准，kagent/生态直接兼容。
- SDK 版本以 `deploy/versions.md` 钉死为准；升级属单独 PR（`aegis-ci` 版本纪律）。
- 与本仓库其他构件的边界：服务端共享框架在 `internal/mcpserver/`（注册、元数据强校验、限流中间件），本目录是 client/代理侧。两者职责不重叠——mcpserver 回答"工具如何被注册与暴露"，本目录回答"调用如何被代理与防护"。

## 4. 具体设计

### 4.1 强制代理拓扑与数据流

1. Agent（kagent 运行时）的 ToolServer 配置指向闸门地址；工具白名单经 Agent CRD 声明（§4.3：tools 为 ToolServer 引用，白名单制）。
2. 调用到达闸门：先校验 mTLS 与签名信封（会话身份、序号、载荷哈希），再做 L2 流程（分级、策略、审批）。
3. L2 放行后，本适配器把调用代理到目标 `mcp-servers/<族>`；未放行的调用不进入本适配器。
4. 工具结果经本适配器回送；读结果的隔离包裹在闸门侧（I4；M1 库形态、M2 收敛，见 §2 第 6 条）。
5. 全链路 span 挂靠：`adapters.mcp.proxy` 是闸门 span 与工具 server span 之间的桥（见 §6）。

### 4.2 签名信封与序号（I10-4 / F15）

信封字段（名称 + 语义，字段名以协议实现为准）：

| 字段 | 语义 |
| --- | --- |
| `session_id` | 会话标识；信封密钥按会话分发 |
| `seq` | 会话内单调递增序号；乱序即拒 |
| `timestamp` | 发送时刻，与序号配合判定超窗 |
| `payload_hash` | 调用参数摘要，防篡改 |
| `signature` | 上述字段的签名；密钥经构造注入，永不出现在日志 |

重放规则：同 `session_id` + 同 `seq` 再次到达 = 重放，拒绝并告警。这是 F15 的信道侧防线；更上层的 idempotencyKey → CR 名哈希折叠（§5.1）是独立第二道，两道互补。序号状态维护在代理会话内——leader election 保证单写者（§4.2.5-1），序号校验点因此唯一。

### 4.3 写工具参数形状强制

write 类工具（scale_deployment / restart_pod / cordon_node）的入参仅允许 ChangeRequest ID 引用；本适配器在代理前校验参数形状，含自由参数的请求不转发、直接拒（T2.8 完成判据原文：Agent 直接拿自由参数调写工具被协议层拒绝）。工具 server 侧另有二次校验，双保险构成 I3 的纵深——工具层无法被 Agent 直接触达写能力（§4.1 原文）。

### 4.4 调用超时的分级

1. 每工具注册时强制填写 `timeout_ms`（§4.1 五字段之一）。
2. 本适配器为每次调用设"连接超时 < 读写超时 ≤ timeout_ms"的层级，且总预算取 min(timeout_ms, 会话剩余预算允许值)。
3. 读工具超时：可安全重试（只读幂等）；写工具超时：绝不盲重试——结果标记超时后由上层凭 CR 幂等键折叠语义裁决（F15 与 §5.1 idempotencyKey 的配合点）。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级及理由 |
| --- | --- | --- | --- |
| 签名验证失败 | 信封校验 | 拒绝 + 告警 | fail-closed：身份不可证即不可转发（视同攻击面，F15 同族） |
| 序号乱序 / 重放 | seq 窗口 | 拒绝 + 告警；写调用不得触达工具层 | fail-closed：F15 原文"重复提交返回同一 CR；序号乱序拒绝" |
| mTLS 握手失败 | 传输层 | 拒绝 + 告警 | fail-closed：同上 |
| 工具 server 不可用 | 连接错误 / 超时 | 读工具降级（明确标注）；写路径阻塞 + 告警 | 写 fail-closed：与 I10 写路径语义一致 |
| 调用超时 | deadline + timeout_ms | 读可重试；写标记超时上报，由 CR 状态机裁决 | 写不盲重试：F15 的源头治理，重试是双执行事故的第一成因 |
| 会话预算耗尽 | 计量回执（F4） | 整会话暂停，输出当前结论与置信度 | fail-closed 于会话：预算不可证 = I7/F4 保障失效 |
| 写工具参数形状违规 | 代理前校验 | 拒绝 + 告警 | fail-closed：I3，工具层不可被自由参数触达 |

## 6. 可观测性

- 结构化日志字段：`session_id`、`seq`、`tool_name`、`tool_server`、`envelope_valid`、`latency_ms`、`error_class`、`timeout_budget_ms`。
- 指标：`aegis_mcp_proxy_call_total{tool=,result=}`、`aegis_mcp_envelope_reject_total{reason=}`、`aegis_mcp_proxy_latency_seconds_bucket{tool=}`。
- trace span：`adapters.mcp.proxy`，属性 `session_ref`、`tool_name`、`cr_ref`（写调用）；与 `internal/mcpserver` 的 server 侧 span 首尾相接，构成"会话→闸门→工具"全链（§4.6 观测要求）。
- 审计留痕点：全量工具调用序列（含被拒调用）经 `internal/audit` 进入 DecisionRecord.toolCalls（§5.3）；信封拒绝事件作为独立告警类留痕，与 sanitizationEvents（§5.3）分列。

## 7. 依赖方向与模块边界

- 谁调我：`cmd/gatekeeper` 装配的强制代理；闸门执行路径服务（放行后的调用出口）。
- 我调谁：各 `mcp-servers/` 进程（出站连接）；计量上报经 `internal/session` 端口（不反向依赖其实现）。
- 禁止依赖谁：禁止 import 上下文 domain/service（只依赖端口接口与配置值对象）；禁止 import client-go / OPA / Redis / Langfuse SDK；`mcp-servers/` 禁止 import 本目录——工具层只见协议不见代理。
- 边界声明：放行判断永不下沉到本目录或工具层（§4.1 职责边界，也是 `aegis-ddd-layout` 反模式清单原文）；本目录只做"已放行的调用如何安全到达"。

## 8. 测试策略与红队用例

- 单测：fake tool server 做契约测试（参数形状、超时分级、错误翻译）；信封与序号用表驱动覆盖（正常、乱序、重放、超窗、篡改五类）。
- 集成：kind 内"闸门 + 真实 mcp-servers/k8s-read"的代理链路，属 M1 联调路径（T1.10）。
- 红队用例（M2 检查点直接对应项，该检查点是项目准入门槛）：① 复制 MCP 信封重发 → 拒绝（F15）；② 自写客户端绕开 CLI/Agent 直连工具 server → mTLS 与代理注册表层面不可达；③ 写工具自由参数注入 → 协议层拒绝（I3）；④ 会话预算绕过（伪造客户端计数）→ 服务端计数为准，绕过无效。本目录是用例 ①③ 的主战场，用例 ②④ 与它共同构成纵深。
