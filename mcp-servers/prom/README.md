# prom — Prometheus 窄查询工具集（query / range 两个接口）

> 所属层：L1 工具层 ｜ 里程碑：M1（落地方案 T1.3） ｜ 设计文档出处：§4.1 ｜ 关联不变量：I3、I4 ｜ 关联失败模式：F5（变体）、F4

## 1. 设计初衷

Prometheus 是诊断闭环的核心证据源（OOMKilled 的内存曲线、CPU 节流、重启风暴的时间分布都要靠它），但它也是最容易被打爆的只读面：一条大范围 PromQL range 查询可以在秒级拉出海量时序，既压 TSDB 又爆 LLM 上下文。所以本 server 只开两个窄接口——instant query 与 range query——并把"查询超时、结果集截断、token 预算"做成协议级纪律，对应失败模式 F5 的指标面变体（设计文档 §4.1 的读限流与上限同样适用于此处）。

与 k8s-read 的边界：`get_resource_metrics`（metrics.k8s.io 的 kubectl top 一族数值）在 k8s-read；一切 PromQL 表达式查询在本目录。设计文档 §4.1 示例 `get_metrics(query, range)` 即由本目录承担。

## 2. 职责与任务清单

**职责**：把 Agent 的指标查询意图翻译成两条只读 HTTP 调用（instant / range），校验参数合法性，按上限截断结果，结构化返回。**不解析 PromQL 语义、不做告警规则管理、不做任何写操作**（不触碰 Prometheus 的管理与配置 API）。

**任务清单**：

| 任务 | 交付 | 出处 |
| --- | --- | --- |
| T1.3 | query/range 两个窄接口，结果结构化返回 + token 截断 | 落地方案 W5 |
| T1.4 | 查询面纳入会话双预算（本层结果 token 截断与预算计数配合） | 落地方案 W7 |
| T1.5 | 结果出口统一 sanitize 包裹（指标标签值同样是租户可控字符串） | 落地方案 W5 |

## 3. 技术选型与开源包

- 出站访问经 `internal/adapters/prom` 端口：只调用 Prometheus 只读 HTTP API（`/api/v1/query`、`/api/v1/query_range`），版本以 `deploy/versions.md` 钉死为准；
- 共享框架 `internal/mcpserver`（注册、元数据、限流中间件）；
- sanitize 出口包裹：`internal/sanitize` 库（M1）/ L2 log-sanitizer（M2 起）。

## 4. 具体设计（不写代码）

### 4.1 工具清单

| # | 工具 | 入参语义 | 强制上限 | 返回结构 |
| --- | --- | --- | --- | --- |
| 1 | prom_query | query（PromQL 字符串，必填）、time（缺省当前） | 查询超时硬上限；返回序列数 ≤50；单序列样本点数超限截断 | `series[]`：metric（标签键值对）、value、timestamp；`truncated`；`next_hint`（缩小范围建议） |
| 2 | prom_range | query（必填）、start、end、step | 区间时长 ≤1h；step ≥15s；计算点数 ≤720（超出按 step 放大重算，重算后仍超限则拒绝并给合法参数引导）；序列数 ≤50 | `series[]`：metric、values[]（时间戳+值）；`truncated`；`params_resolved`（实际生效的 start/end/step） |

超时与上限的具体数值为建议初值，随 kind 环境压测校准后登记工具注册表；协议形状（超时必配、截断必标）不变。

### 4.2 元数据五字段

| 工具 | risk_hint | idempotent | reversible | est_blast_radius | timeout_ms（建议初值） |
| --- | --- | --- | --- | --- | --- |
| prom_query | R0 | true | true | 0 | 10000 |
| prom_range | R0 | true | true | 0 | 15000 |

### 4.3 结果截断三级纪律

1. 参数级：step/区间在入口校验，能重算则重算，不能则拒绝（429 语义 + 合法值引导，F5）；
2. 序列级：返回序列数触顶时按样本总量排序截断，`truncated=true` 并附被截断序列的标签摘要（不让 Agent 误以为世界只有 50 条序列）；
3. token 级：序列标签值统一过字节预算，单工具返回超预算时压缩标签集并标注，配合会话 token 预算（F4）记账。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级 | 对应 |
| --- | --- | --- | --- | --- |
| PromQL 语法非法 | Prometheus 400 语义透传 | 返回 syntax_error + 原始错误定位信息（透传但不改写语义） | 直接失败 | I3 |
| 查询参数超限（E-LIMIT） | 入口校验（区间/step/点数） | 可重算则重算执行；不可则拒绝 + 合法值引导 | 直接失败 | F5 |
| 查询超时（E-UPSTREAM） | 客户端超时器（≤timeout_ms） | 返回 timeout，不重试（重试是放大器） | 读路径失败如实返回；由 L2 决定会话级降级 | F5 变体 |
| Prometheus 5xx/不可达 | 状态码与连接错误 | 返回 upstream_error + 已等待时长 | 同上 | I10 |
| 结果触顶（E-SIZE） | 返回前计数 | 三级截断纪律，标注 truncated | 正常路径 | F5 |
| 序列级注入面 | sanitize 模式检测（标签值中的指令型文本） | 包裹 + 命中告警（M2 起）；M1 包裹 + 留痕 | 包裹失败则整次调用失败 | I4、F2 |

**PromQL 注入面说明**（威胁模型口径）：PromQL 本身是只读表达式语言，不存在"写注入"；真正的风险面在两点——其一，出站通道只放行只读 HTTP GET 端点，管理/配置/删除类 API 在适配器层不存在（协议形状上不可能，而非运行时拦截）；其二，指标标签值是工作负载可控的任意字符串（Pod 名、容器名、annotation 值），属于 I4 不可信数据，返回必过 sanitize。

**失效姿态**：无状态，快速失败，不自作主张重试。Prometheus 整体不可达时本层如实报错，诊断路径由 Agent 降级到 events/logs 证据（读路径降级语义在 L2 标注，I10）。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- **日志字段**：L1 统一字段集 + `query_fingerprint`（PromQL 归一化哈希，不落原始表达式全文）、`range_span_seconds`、`step_seconds`、`series_returned`、`points_returned`、`truncated`、`timeout_hit`。
- **指标**：`aegis_tool_calls_total{server="prom", tool, result}`；`aegis_tool_latency_seconds{server="prom", tool}`；`aegis_prom_query_points_total{tool}`（返回总点数水位）；`aegis_truncated_total{server="prom", tool, kind="series"}`；`aegis_prom_query_timeout_total{tool}`。
- **Trace span**：`l1.prom.query` / `l1.prom.range`，子 span `.validate` / `.sanitize` / `.upstream`。
- **审计留痕点**：每次查询落 DecisionRecord `spec.toolCalls`（query_fingerprint、生效参数、序列数、truncated）；M1 回放时指标证据链可复现。

## 7. 依赖方向与模块边界

- **谁调我**：M1 为 kagent 直连；M2 起为 L2 gatekeeper MCP 代理转发。
- **我调谁**：`internal/mcpserver` 框架、`internal/budget` 限流中间件、`internal/sanitize`（M1）、`internal/adapters/prom` 出站端口。
- **禁止依赖谁**：禁止触碰 Prometheus 管理/写类 API（适配器内不存在对应端点）；禁止 import L2 判定组件；与 k8s-read 的 metrics 工具零重叠（PromQL 只在本层）。

## 8. 测试策略与红队用例

- **单测**：参数校验边界表（step=1s、区间 24h、点数 100000）；截断与标签摘要生成；token 预算压缩路径。
- **集成测试**：kind 环境内 Prometheus 实查（M1 的 OOMKilled 场景验证内存曲线可取）。
- **红队用例**：
  1. 大范围查询（F5 变体）：构造 7 天 / 1s step 的 range 查询，验证重算或拒绝 + 引导，验证 TSDB 未被拖垮；
  2. 标签注入（I4/F2）：Pod 名携带指令型文本，验证返回带包裹且留痕；
  3. 出口面探测：构造指向管理类 API 路径的 query 参数，验证适配器层无此端点（协议形状不可能）；
  4. 超时纪律：构造慢查询，验证超时即返回、无重试风暴。
