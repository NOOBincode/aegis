# internal/adapters/langfuse — LLM 调用链追踪与回放出口

> 所属层：L4 观测层支撑（OTel 数据面的 aegis 侧实现）｜ 里程碑：M1（T0.5 观测基座起）｜ 设计文档出处：§3.3、§4.6 ｜ 关联不变量：I6（决策可回放）｜ 关联失败模式：无直接对应编号（降级语义见 §5）

## 1. 设计初衷

设计文档 §4.6 要求：OTel trace 贯穿会话→闸门→工具→集群；Langfuse 回放 LLM 调用链（§3.3 选型理由：OTel 标准留痕 + Langfuse 做 LLM 调用链回放成本低）；每次会话落 DecisionRecord（§5.3）。本目录是这套观测链的 aegis 侧出口：把散落在闸门、工具、集群调用里的 span 汇成一条可追溯链，并负责两件安全敏感的事——`llmTraceRef` 的生成与回写、敏感字段脱敏（红线：不留 secret、不留完整日志体）。

观测是 I6 的回放原料，但它自己绝不能成为故障源或泄漏面：trace 系统全挂，闸门一个请求都不该多等——这是本目录全部降级设计的出发点。

## 2. 职责与任务清单

1. OTel exporter 装配：trace 经 OTLP 导出至 otel-collector，collector 再接 Langfuse（自托管，T0.5 基座先 docker-compose 从简）。
2. span 结构约定：统一"会话→闸门→工具→集群"的命名与属性白名单（§4.2），供各进程埋点遵守。
3. `llmTraceRef` 生成：会话根 span 的 trace 标识作为 llmTraceRef 来源，回写 DecisionRecord.spec.llmTraceRef（§5.3）。
4. 敏感字段脱敏：trace 属性与事件不落 secret、不留完整日志体（§4.4）。
5. 降级：Langfuse/collector 不可用时 trace 落本地 collector 缓冲，不阻塞业务（§4.5）。

## 3. 技术选型与开源包

- OpenTelemetry（Go SDK + OTLP exporter）：观测选型原文"OpenTelemetry + Langfuse（自托管）"（设计文档 §3.3）——OTel 是标准留痕面，Langfuse 只做 LLM 调用链回放，分工不重叠。
- Langfuse 自托管：docker-compose 从简（落地方案 T0.5 执行说明原文），集群内部署为后续可选项，不提前实施。
- collector 与 Langfuse 版本以 `deploy/versions.md` 钉死为准；Go SDK 版本随 go.work / versions.md 对账（`aegis-go-quality`）。

## 4. 具体设计

### 4.1 链路拓扑

1. aegis 各进程（gatekeeper / controller / mcp-servers）内嵌 OTel SDK，span 经 OTLP 发往 otel-collector。
2. collector 分流：trace 全量送 Langfuse；指标与日志走各自的观测面（部署细节归 `deploy/observability/`，本目录只管 aegis 侧出口语义）。
3. Langfuse 提供 LLM 调用链回放；`aegis-cli replay`（M1，落地方案 T1.8）以 DecisionRecord 为主链、Langfuse 为 LLM 细节补充——两者是回放的主从关系，不是互备。

### 4.2 span 设计（会话→闸门→工具→集群）

| span 名 | 层级 | 关键属性 | 产出方 |
| --- | --- | --- | --- |
| `aegis.session` | 会话根 | `session_ref`、`source`、`model_channel` | L2 会话边界 |
| `gatekeeper.risk.classify` | 闸门 | `cr_ref`、`risk_level`、`blast_radius` | risk-classifier |
| `gatekeeper.opa.eval` | 闸门 | `cr_ref`、`bundle_hash`、`violation_count` | opa-eval（经 `internal/adapters/opa`） |
| `gatekeeper.approval` | 闸门 | `cr_ref`、`state`、`channel` | approval-svc |
| `gatekeeper.execute` / `gatekeeper.verify` / `gatekeeper.rollback` | 闸门 | `cr_ref`、`state` | 执行路径 / verifier / rollbacker |
| `tool.<name>` | 工具 | `tool_name`、`cr_ref`（写调用） | MCP 代理（`internal/adapters/mcp`） |
| `adapters.k8s.*` | 集群 | `verb`、`gvk`、`dry_run` | k8s 适配器（见 `internal/adapters/k8s/README.md` §6） |

命名纪律：span 名统一为 `层.组件.动作`（适配器前缀 `adapters.`）；属性值只放标识与判定结果，不放内容体——内容体纪律由 §4.4 强制执行。

### 4.3 llmTraceRef 生成与回写

1. 会话建立时生成会话根 span（`aegis.session`），`session_ref` 入属性。
2. `internal/audit` 组装 DecisionRecord 时，经 trace 记录端口取会话根 trace 标识，写入 `spec.llmTraceRef`（§5.3 字段，示例值 langfuse-trace-yyy）。
3. 回写失败（trace 系统抖动）时允许 llmTraceRef 为空并标注 `trace_missing`；DecisionRecord 其余字段不受影响——审计完整性的主链是 I6 的 CRD/落盘链，不依赖 trace 系统可用性。

### 4.4 敏感字段脱敏

红线：不留 secret、不留完整日志体（含集群返回的日志/事件原文）。机制四层：

1. 属性白名单：span 属性只允许 §4.2 表中的标识类与判定类字段；自由文本属性默认禁止。
2. 不可信数据只留摘要：I4 下集群返回数据经 log-sanitizer 包裹后，进入 trace 的只有长度、命中标记、摘要哈希。
3. 模式复用：脱敏判定复用 `internal/sanitize` 的注入检测模式——值对象级纯函数共享（`aegis-ddd-layout` 铁律 4 允许），保证 trace 与业务面同一套模式库，不漂移。
4. 采集端再挡一道：collector 侧配置 drop 规则（部署面，见 `deploy/observability/`），aegis 侧漏出的敏感形态在出口被二次拦截。

### 4.5 Langfuse 不可用时的降级

1. 第一级：collector 可达、Langfuse 不可达——trace 在 collector 缓冲，恢复后补传。
2. 第二级：collector 不可达——SDK 内存队列短时缓冲，随后采样丢弃。
3. 全程零阻塞：业务路径不等待 exporter 回执，异步导出；闸门任何判定的时延不得因本目录增加。
4. 理由：观测是旁路面——I6 的完整链在 DecisionRecord 与 CR status，I10 的恢复语义以 CRD 为准。丢失 trace 不影响任何安全判定，宁可丢 trace，不可拖慢闸门。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级及理由 |
| --- | --- | --- | --- |
| exporter 不可达（collector / Langfuse 故障） | 导出失败回调、队列深度指标 | 缓冲 → 采样丢弃；告警 | 降级不阻塞：观测旁路化，业务零等待（§4.5 理由） |
| 脱敏校验不通过（疑似敏感字段进属性） | 白名单 + 模式命中 | 丢弃该属性/事件，宁可缺 span 细节 | fail-closed 于泄漏面：宁丢观测不泄密（安全优先于可观测性） |
| llmTraceRef 回写失败 | trace 记录端口错误 | 置空 + `trace_missing` 标注；审计主链不受影响 | 降级：I6 主链不依赖 trace 系统 |
| 采样导致的链路缺口 | 采样配置核查 | 会话根 span 永不采样丢弃（llmTraceRef 恒可得）；子 span 可采样 | 降级可控：会话级恒留，细节级可裁 |
| collector 缓冲打满 | 队列深度指标 | 丢弃最旧 + 告警 | 降级：容量有界，拒绝无界堆积拖垮进程（I10：不拖垮被管对象，也不拖垮自己） |

## 6. 可观测性

本目录的产物即观测本身；以下是对观测链路的自观测（meta-observability）：

- 结构化日志字段：`exporter_target`、`queue_depth`、`dropped_total`、`error_class`、`session_ref`（仅会话级事件）。
- 指标：`aegis_trace_export_total{result=ok|buffered|dropped}`、`aegis_trace_buffer_depth`（gauge）、`aegis_trace_dropped_total{reason=}`、`aegis_llm_trace_ref_missing_total`（gauge，回写失败计数）。
- trace：exporter 自身的诊断走 OTel SDK 内部机制，不进 Langfuse 业务项目，避免自引用噪声。
- 审计留痕点：`llmTraceRef` 回写 DecisionRecord（§4.3）+ `trace_missing` 标注事件；脱敏丢弃事件计入会话级告警并留痕。

## 7. 依赖方向与模块边界

- 谁调我：`cmd/` 各进程装配（exporter 初始化）；`internal/audit`（经 trace 记录端口取 llmTraceRef）。各进程的业务 span 埋点按 §4.2 约定直调 OTel API，不经本目录代码。
- 我调谁：otel-collector（出站 OTLP）；`internal/sanitize` 的纯函数脱敏判定（值对象级共享，见铁律 4）。
- 禁止依赖谁：禁止 import client-go / OPA / Redis / MCP SDK；禁止 import 上下文 service；禁止承载审计本体——DecisionRecord 的组装与落盘在 `internal/audit`，本目录只供 trace 标识与出口。
- 边界声明：本目录不参与任何放行/熔断/预算判断；I10 的恢复语义以 CRD 为准，与本目录无关（观测挂了对集群零影响）。

## 8. 测试策略与红队用例

- 单测：脱敏规则表驱动（secret 样例、token 样例、注入文本样例不得出现在属性值）；llmTraceRef 生成与 `trace_missing` 回退路径；降级两级用 fake exporter 验证"业务调用零阻塞"。
- 集成：T0.5 基座验收（手测调用在 Langfuse 可见，落地方案 T0.5 完成判据）+ M1 验收的 trace 贯穿检查（OOMKilled 诊断链在 Langfuse 可回放，§7 M1 口径）。
- 红队用例：① M1 检查点的注入文本（"忽略之前的指令……"类）不得在 trace 明文出现——白名单 + 脱敏双层验证（I4 的观测面延伸）；② 构造仿 secret 字段的工具返回 → trace 只留摘要哈希；③ exporter 故障注入（断 collector）→ 闸门写路径无显著退化、无 span 挂起泄漏（goleak 验证）。
