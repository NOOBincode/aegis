# k8s-read — 只读 K8s 对象工具集（8 个窄接口）

> 所属层：L1 工具层 ｜ 里程碑：M1（落地方案 T1.2） ｜ 设计文档出处：§4.1 ｜ 关联不变量：I3、I4 ｜ 关联失败模式：F4、F5

## 1. 设计初衷

k8s-read 是 M1 的主战场：诊断闭环的第一步"查得到"全靠它。设计约束来自 I3（工具即权限边界）与 F5（读操作打爆 apiserver）：只读不等于无害——大范围 list 与全量日志照样能打垮 apiserver，照样能把注入文本送进 LLM 上下文。所以只读族同样走窄接口、强上限、结果包裹的完整纪律。

与 prom server 的边界：`get_resource_metrics` 只查 metrics.k8s.io 资源指标（kubectl top 一族的数值），PromQL 表达式的查询全部在 `mcp-servers/prom/`。设计文档 §4.1 示例中的 `get_metrics(query, range)` 由 prom 的 query/range 两工具承担，本目录不重复提供。

## 2. 职责与任务清单

**职责**：K8s 核心对象（Pod/Deployment/Node/Event）的只读列举、单对象描述、单 Pod 日志、资源指标的参数校验与结构化返回。不做聚合分析（多 Pod 归并见 `mcp-servers/logs/`、时间线对齐见 `mcp-servers/events/`）、不做任何写操作、不持有集群凭证以外的任何权限。

**任务清单**：

| 任务 | 交付 | 出处 |
| --- | --- | --- |
| T1.2 | 8 个只读工具，强制分页与上限（≤500 行日志 / ≤200 对象），超限 429 + 分页引导 | 落地方案 W4–W5 |
| T1.5 | 工具出口统一 `<untrusted_cluster_data>` 包裹（M1 库形态） | 落地方案 W5 |
| T1.4 | 读限流双预算（会话 token + apiserver QPS）在本层经中间件执行 | 落地方案 W7 |

## 3. 技术选型与开源包

- 出站访问经 `internal/adapters/k8s` 端口（client-go 封装），版本以 `deploy/versions.md` 钉死为准；
- 共享框架 `internal/mcpserver`：注册、元数据强校验、限流中间件；
- sanitize 出口包裹 M1 用 `internal/sanitize` 库（T1.5），M2 起由 L2 log-sanitizer 在代理层统一执行（T2.7）。

## 4. 具体设计（不写代码）

**统一返回结构**（所有 8 个工具）：`items`（条目列表）、`object_count`（实际返回数）、`truncated`（是否触顶）、`next_cursor`（分页游标，触顶时给出）、`sanitized_wrapped`（包裹标记恒为 true）。所有条目字段值一律视为不可信数据。

**工具清单与强制上限**：

| # | 工具 | 入参语义 | 强制上限 | 返回条目核心字段 |
| --- | --- | --- | --- | --- |
| 1 | get_pods | ns（必填）、label_selector、limit | limit ≤200，超者拒绝并 429 + 合法值引导 | name、phase、restarts、node、age、owner_kind |
| 2 | get_deployments | ns（必填）、label_selector、limit | limit ≤200 | name、replicas 三元组（期望/可用/更新中）、age |
| 3 | get_nodes | limit | limit ≤200（集群节点规模小，触顶罕见但纪律一致） | name、ready、taints 数、age |
| 4 | get_events | ns（必填）、since（时间窗）、limit | limit ≤200；时间窗超配置上限时按上限截断并标注 | 见 events server 的对齐格式（本工具为基础列举版） |
| 5 | get_pod_logs | ns、name（必填）、container、tail_lines、since_seconds | tail_lines ≤500；单行超长截断并标注 | 行号、timestamp（解析失败标 null）、content |
| 6 | get_resource_metrics | kind ∈ pods/nodes、ns（kind=pods 时必填）、limit | limit ≤200 | name、cpu、memory（metrics.k8s.io 数值） |
| 7 | describe_pod | ns、name（必填） | 输出超字节上限时截断尾部并标注 | name、conditions、container_states、events 摘要 |
| 8 | describe_deployment | ns、name（必填） | 同上字节上限 | name、conditions、replicas 明细、events 摘要 |

**元数据标注**（注册强制五字段）：

| 工具 | risk_hint | idempotent | reversible | est_blast_radius | timeout_ms（建议初值） |
| --- | --- | --- | --- | --- | --- |
| 8 个全部 | R0 | true | true（只读天然可逆） | 0 | 10000 |

timeout_ms 为建议初值，随 kind 压测校准后登记进各工具注册表，不在本文档钉死。

**分页纪律**：任何可能触顶的调用必须带 limit；返回触顶时 `truncated=true` + `next_cursor`，Agent 续查须显式携带游标，禁止服务端隐式翻页放大 QPS（F5 的双保险）。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级 | 对应 |
| --- | --- | --- | --- | --- |
| 参数非法（E-SCHEMA） | ns/name 空值、limit 超上限、label_selector 语法错 | 拒绝 + 语义化错误 + 合法上限提示 | 直接失败（读路径无放行概念） | I3 |
| 预算耗尽（E-QUOTA） | `internal/budget` 会话 token / QPS 窗口计数 | 429 语义 + 已消耗/上限告知；会话级耗尽时返回暂停语义 | 降级：本调用失败，会话由运行时输出当前结论（F4 处置） | F4、F5 |
| apiserver 超时/5xx（E-UPSTREAM） | 出站调用超时器与状态码 | 返回 upstream_error，不重试放大压力 | 读路径允许 L2 降级只读直连并标注降级态（I10）；本层如实报错不粉饰 | I10 |
| 对象不存在 | apiserver 404 | 返回空结果 + not_found 标记（正常路径，非错误） | — | — |
| 结果触顶（E-SIZE） | 返回前计数 | 截断 + `truncated=true` + `next_cursor` | 正常路径非失败 | F5 |
| 包裹失败（E-SANITIZE） | 包裹器自身异常 | fail-closed：整次调用失败，未包裹数据绝不出工具 | fail-closed | I4 |
| RBAC 越权读 | apiserver 403 | 返回 forbidden 语义，供 Agent 调整诊断路径 | 直接失败 | I3 |

**失效姿态**：本层无状态，进程重启即恢复（I10）。上游分区时快速失败，重试策略交给调用方，本层不自作主张重试（避免雪崩叠加 F5）。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- **日志字段**：在 L1 统一字段集（见 `mcp-servers/README.md` 第 6 节）上，本层必带 `limit_requested`、`rows_returned`、`truncated`、`qps_budget_remaining`。不落日志正文。
- **指标**：`aegis_tool_calls_total{server="k8s-read", tool, result}`、`aegis_tool_latency_seconds{server="k8s-read", tool}`、`aegis_tool_result_rows{server="k8s-read", tool}`、`aegis_read_qps_consumed_total{server="k8s-read"}`、`aegis_truncated_total{server="k8s-read", tool, kind="rows"}`。
- **Trace span**：`l1.k8sread.<tool>`，子 span `.validate` / `.sanitize` / `.upstream`（apiserver 调用）。
- **审计留痕点**：每次调用落 DecisionRecord `spec.toolCalls`（工具名、入参摘要、返回计数、truncated 标记、时间戳）；M1 的 `aegis-cli replay` 回放依赖此序列。sanitize 包裹事件（即使 M1 无命中检测）留痕包裹执行记录。

## 7. 依赖方向与模块边界

- **谁调我**：M1 为 kagent 直连；M2 起为 L2 gatekeeper MCP 代理转发。
- **我调谁**：`internal/mcpserver` 框架、`internal/budget` 限流中间件、`internal/sanitize`（M1）、`internal/adapters/k8s` 出站端口。
- **禁止依赖谁**：禁止 import L2 判定组件（risk/policy/approval/rollback/breaker/authority）；禁止依赖 `mcp-servers/k8s-write`（读写两族进程零互调）；handler 内禁止出现业务判断 if-else 森林（分页/上限属框架中间件职责）。

## 8. 测试策略与红队用例

- **单测**：参数校验边界表（limit=0、limit=201、tail_lines=501、label_selector 语法错）；截断与游标生成的表驱动用例。
- **集成测试**：kind 实集群全 8 工具；超限请求 429 + 分页引导（T1.2 完成判据）；OOMKilled 场景下 get_pod_logs 能拿到退出原因行（服务 M1 验收演示）。
- **红队用例**：
  1. 注入初测（M1 检查点 #1）：在 Pod 日志夹带指令型文本，验证返回带 `<untrusted_cluster_data>` 包裹、事件留痕、Agent 侧不产生写意图（I4/F2）；
  2. 超大 list 尝试（F5）：label_selector 全匹配 + limit=10000，验证拒绝 + 引导而非执行；
  3. 预算绕过（F4）：构造高频调用耗尽 QPS 窗口，验证 429 而非放行；
  4. 日志时间戳伪造：content 内嵌伪时间戳行，验证只按行首解析、正文一律包裹（防时间线混淆注入）。
