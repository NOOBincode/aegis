# events — 事件时间线工具：归因对齐格式供给方

> 所属层：L1 工具层 ｜ 里程碑：M1（落地方案 T1.3） ｜ 设计文档出处：§4.1、§4.2.2（attributor） ｜ 关联不变量：I4、I9 ｜ 关联失败模式：F2、F5、F11

## 1. 设计初衷

K8s Events 是集群自己写的"发生了什么"流水，也是 I9"先归因后行动"的第一手输入：attributor 的归因流水线消费所有权图谱 + 事件时间线 + 指标佐证（设计文档 §4.2.2），其中事件时间线必须来自一个**确定性、可对齐的格式**——如果每个工具各说各话，归因就是在垃圾上建楼。本 server 的存在意义有二：给诊断 Agent 提供时序化的事件视图（OOMKilled、FailedScheduling、Unhealthy 的时间分布），给 authority 归因流水线提供机器可消费的对齐格式（attributor 与 authority-map 按同一 schema 读数据）。

事件 message 同样是工作负载与控制器可控的字符串（I4 不可信数据），与日志同列注入面（F2），出口纪律一致。

## 2. 职责与任务清单

**职责**：按命名空间与时间窗拉取事件，归一化为对齐格式，按时间线输出。不做事件聚合统计之外的推断（"是不是故障"是 attributor 与 diagnoser 的事）、不读 audit log（那是更高权重的留痕系统，不在工具层暴露）。

**任务清单**：

| 任务 | 交付 | 出处 |
| --- | --- | --- |
| T1.3 | get_event_timeline：事件时间线输出，对齐格式定义 | 落地方案 W5 |
| T1.5 | 出口统一 sanitize 包裹（message 为不可信字符串） | 落地方案 W5 |
| T2.9 | 对齐格式被 authority 归因流水线消费（attributor 的输入契约） | 落地方案 W16 |

## 3. 技术选型与开源包

- 出站访问经 `internal/adapters/k8s` 端口（events 资源只读 list，带 fieldSelector 时间窗裁剪）；
- 共享框架 `internal/mcpserver`；sanitize 出口 `internal/sanitize`（M1）/ L2 log-sanitizer（M2 起）；
- 对齐格式为文档级契约（本 README 定义），authority 上下文按契约消费，不共享代码级结构之外的隐式约定。

## 4. 具体设计（不写代码）

### 4.1 工具清单

| # | 工具 | 入参语义 | 强制上限 | 返回结构 |
| --- | --- | --- | --- | --- |
| 1 | get_event_timeline | namespaces（可多选）、since（时间窗起点）、type_filter（Normal/Warning，可选）、involved_kind（如 Pod/Node/Deployment，可选）、limit | 事件总数 ≤200（设计文档 §4.1 对象上限同口径）；时间窗超配置上限按上限截断并标注 | `events[]`（对齐格式，见 4.2）；`window`（实际生效时间窗）；`truncated`；`source_note`（采集侧降级的说明，如有） |

### 4.2 归因对齐格式（attributor 消费契约）

每个事件条目固定字段，字段名即契约：

| 字段 | 语义 | 不可信标注 |
| --- | --- | --- |
| `timestamp` | 事件发生时间（eventTime 优先，缺失退 series.lastTimestamp） | 可信（系统时钟） |
| `type` | Normal / Warning | 可信 |
| `reason` | 归一化原因码（OOMKilled、FailedScheduling、BackOff……） | 可信（控制器枚举） |
| `involved_object` | kind + name + namespace 三元组 | 字符串值不可信 |
| `source_component` | 产生者（kubelet、default-scheduler、hpa-controller……） | 归因关键字段 |
| `count` / `first_seen` / `last_seen` | 重复次数与首尾时间 | 可信 |
| `message` | 事件全文（截断至字节上限） | **不可信，sanitize 包裹** |
| `collector` | 采集工具标识（本工具名与版本锚） | 可信 |

对齐要求：按 timestamp 升序；`reason` 与 `source_component` 保持 apiserver 原值不做自由映射（归因依赖原值对齐，自由映射会引入二次误差）；message 截断处显式标注。attributor 与 authority-map 消费的是同一字段集，任何格式演进必须双边同步改契约（本文件与 `internal/authority/README.md` 交叉引用）。

### 4.3 元数据五字段

| 工具 | risk_hint | idempotent | reversible | est_blast_radius | timeout_ms（建议初值） |
| --- | --- | --- | --- | --- | --- |
| get_event_timeline | R0 | true | true | 0 | 10000 |

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级 | 对应 |
| --- | --- | --- | --- | --- |
| 参数非法（E-SCHEMA） | 时间窗/since 非法、filter 值不在枚举 | 拒绝 + 合法值引导 | 直接失败 | I3 |
| 事件触顶（E-LIMIT/E-SIZE） | 返回前计数 | 截断 + `truncated=true` + 收紧引导（缩短窗口或加 filter） | 正常路径 | F5 |
| apiserver 超时（E-UPSTREAM） | 超时器 | upstream_error，不重试 | 读路径失败如实返回 | I10 |
| message 注入命中（E-SANITIZE） | sanitize 模式检测（M2 起） | 包裹 + 告警；衍生动作升 R2 在 L2 执行 | 包裹失败整次调用失败 | I4、F2 |
| 采集侧降级（如时间窗被截） | 窗口裁剪发生 | `source_note` 显式标注，不静默 | 降级明示 | I10 标注精神 |

**为什么归因输入也要 fail-closed 包裹**：F11 的反面是"控制器正常行为被误判为故障"，attributor 若吃到未包裹的事件文本，注入者可以伪造一条"看起来像 HPA 在伸缩"的事件诱导错误归因。对齐格式里 message 不可信 + 包裹，正是给归因流水线划清"事实字段"与"文本字段"的边界。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- **日志字段**：L1 统一字段集 + `namespaces`、`window_since`、`window_until`、`events_returned`、`truncated`、`degraded_collection`（source_note 触发标记）、`sanitize_hit`（M2 起）。
- **指标**：`aegis_tool_calls_total{server="events", tool, result}`；`aegis_tool_latency_seconds{server="events", tool}`；`aegis_truncated_total{server="events", tool, kind="rows"}`；`aegis_sanitize_hit_total{server="events"}`。
- **Trace span**：`l1.events.get_event_timeline`，子 span `.collect` / `.normalize`（对齐格式化）/ `.sanitize`。attributor 侧的 span（`gatekeeper.authority.attribute` 一族）以 `window` 字段与本 span 关联，归因证据链可拼接。
- **审计留痕点**：调用落 DecisionRecord `spec.toolCalls`；M2 起注入命中落 `spec.sanitizationEvents`。归因结论的可回放性（I6）依赖本工具返回的窗口与版本锚可复现。

## 7. 依赖方向与模块边界

- **谁调我**：两类消费方——诊断 Agent 的工具调用（M1 直连 / M2 起经 MCP 代理）；attributor 归因流水线（L2 authority 上下文经端口拉取，不经过 Agent 会话）。**归因消费是服务端到服务端调用，与 Agent 会话解耦**——Agent 感知到的状态变化由闸门触发归因，而非 Agent 自己发起。
- **我调谁**：`internal/mcpserver` 框架、`internal/budget`、`internal/sanitize`（M1）、`internal/adapters/k8s` 出站端口。
- **禁止依赖谁**：禁止反向依赖 `internal/authority`（对齐格式是文档契约，不是代码依赖；消费方向永远是从 authority 读契约）；禁止 import L2 判定组件；不做"事件→根因"的自由推断（那是 attributor 与 diagnoser 的职责，本层只做归一化）。

## 8. 测试策略与红队用例

- **单测**：对齐格式字段完备性；时间排序与退避字段（eventTime 缺失路径）；截断标注。
- **集成测试**：kind 集群注入 OOMKilled + FailedScheduling 事件序列，验证时间线输出与 M1 诊断演示的证据链；T2.9 验收中 attributor 消费本格式完成 HPA 正常伸缩场景归因。
- **红队用例**：
  1. message 注入（F2）：事件 message 夹带指令文本，验证包裹与留痕；
  2. 伪造归因诱导：注入"伪 HPA 伸缩事件"尝试污染 attributor，验证对齐格式下 attributor 只信 `source_component`/`reason` 事实字段、文本字段被隔离（F11 防线）；
  3. 窗口放大（F5）：since 拉满全量事件，验证 200 条截断 + 引导；
  4. 契约漂移：构造字段缺失事件，验证归一化不崩溃且缺失显式标注（不向归因流水线喂半真数据）。
