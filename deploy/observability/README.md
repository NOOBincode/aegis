# deploy/observability — 观测基座部署（OTel + Langfuse + Prometheus/Grafana）

> 所属层：L4 评估与观测层的部署面 ｜ 里程碑：Phase 0（T0.5，W2），M1 起被验收依赖 ｜ 设计文档出处：§3.1、§4.6、§5.3 ｜ 关联不变量：I6 ｜ 关联失败模式：F7、F4、F2

## 1. 设计初衷

观测是 G4（决策可审计）与 I6（决策可回放）的工程载体：OTel trace 贯穿 会话→闸门→工具→集群（设计文档 §3.1 数据流要点、§4.6），Langfuse 做 LLM 调用链回放（§3.3 选型理由：自托管成本低），DecisionRecord 落审计（§5.3）。MVP 纪律是够用、从简：告警通道在 MVP 期就是 CLI 与日志，不建复杂告警系统；先做管道正确性，再做花哨呈现。

## 2. 职责与任务清单

| # | 职责 | 交付物 | 对应任务 |
| --- | --- | --- | --- |
| 1 | OTel collector 管道（traces/metrics/logs） | collector 配置与部署 | T0.5、M1 依赖 |
| 2 | Langfuse 自托管 | docker-compose 编排（先行），M2 后可迁集群内 | T0.5 |
| 3 | Prometheus + Grafana 看板规划 | 闸门核心指标看板 | M2 起 |
| 4 | 告警通道 | CLI 显式输出 + 结构化日志 + 指标持续可见 | F7 处置口径 |
| 5 | 接入规范 | 各进程埋点与注入约定（§4.5） | T1.1 起各进程遵守 |

## 3. 技术选型与开源包

OpenTelemetry（标准留痕，K8s 生态事实标准方向）、Langfuse 自托管（§3.3：回放成本低；docker-compose 先行是 T0.5 从简决策）、Prometheus + Grafana（指标与看板）。全部组件版本以 `deploy/versions.md` 钉死为准。不引入商业 SaaS 观测：私域/离线叙事（设计文档 §1.5）要求整条观测链自托管。

## 4. 具体设计（不写代码）

### 4.1 OTel collector 管道设计

| 管道 | 数据源 | 处理 | 目的地 | 说明 |
| --- | --- | --- | --- | --- |
| traces | gatekeeper、controller、mcp-servers 各进程、kagent（ADK 自带 OTel）、eval runner | batch | Langfuse exporter（LLM 调用链回放） | 会话→闸门→工具→集群全链 |
| metrics | 各进程自埋点（闸门核心指标） | batch | Prometheus（抓取或 remote write） | 闸门核心指标唯一入口 |
| logs | 各进程结构化日志 | 属性规整 | 本地文件/标准输出（MVP 从简） | 后续可接日志系统，属接缝不在本期范围 |

注入方式：各进程经 OTLP 指向 collector。Langfuse 接入三要素（host、public key、secret key）只经环境变量注入，不进 git。

### 4.2 Langfuse 自托管

- **形态**：docker-compose 先行（T0.5 从简）；M2 后可迁集群内。迁移判据：eval 回归频度上来后，集群内部署可降低 WSL2 常驻内存压力（≥16GB 底线见 `deploy/kind/README.md` §1）。
- **定位**：LLM 调用链回放与 prompt 观测，服务 `DecisionRecord.spec.llmTraceRef` 的互引（§5.3）。审计事实源仍是 DecisionRecord——Langfuse 丢数据不等于丢审计（I6 的兜底语义，见 §5）。

### 4.3 Prometheus + Grafana 看板规划（闸门核心指标）

| 看板 | 指标 | 服务的问题 |
| --- | --- | --- |
| 闸门总览 | `aegis_cr_total{risk_level, result}`（CR 通过率）、`aegis_policy_eval_total{result}` | 放行/拒绝结构是否健康（观察 F1/F8） |
| 审批效率 | `aegis_approval_duration_seconds`（直方图） | 审批耗时分布（F8 审批疲劳监控：通过率/耗时） |
| 熔断与降级 | `aegis_circuit_breaker_state{state}`、降级事件计数 | 熔断状态持续可见（F7：熔断后无人复位） |
| 预算消耗 | `aegis_session_budget_used{session_ref, kind=tool_calls\|llm_tokens}` | 会话预算消耗（F4 诊断循环失控） |
| eval 层（M3） | 诊断准确率/归因正确率/误修复率/MTTR | M3 验收口径①（eval 报告呈现，T3.3） |

### 4.4 告警通道

MVP 期不做告警系统。熔断触发（F7）、R3 硬拒（F1）、注入命中（F2）、预算耗尽（F4）四类事件，一律以三处可见为通道：CLI 显式输出、结构化日志、§4.3 指标保持非健康态。"持续告警"的语义 = 指标持续非健康 + 日志持续记录，直至人工复位并留痕（F7 处置口径：保持只读、CLI 显式复位、留痕）。Web 告警台不在范围内（N3 口径）。

### 4.5 接口契约（名称 + 职责 + 入出参语义）

| 接口 | 职责 | 入参语义 | 出参语义 |
| --- | --- | --- | --- |
| OTLP 接收端 | 接收各进程遥测 | traces/metrics/logs 三类信号 | 入队即返回；背压时 SDK 侧缓冲、超限丢弃并计数 |
| Langfuse exporter | LLM span 回放上送 | trace/span 及生成式属性 | 异步批量上送，失败计入丢弃计数 |
| Prometheus 抓取决口 | 指标外露 | /metrics 文本端点 | 抓取语义 |
| Grafana 数据源 | 看板查询 | Prometheus 查询语句 | 面板渲染 |

### 4.6 与其他目录的边界

埋点实现属于各进程（`internal/` 各上下文与 mcp-servers 只依赖 OTLP SDK 抽象）；本目录只负责管道与呈现。决策链留痕的写盘责任在 audit 上下文（`internal/audit`），本层不替代。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| collector 不可用 | SDK 连接失败 | 各进程直落结构化日志到 stdout（降级），进程日志标注 otel_export=degraded | **降级**：观测非控制面，类比 I10"闸门不可用 ≠ 集群不可用"；闸门写路径的 fail-closed 不因观测失效改变 |
| Langfuse 不可用 | exporter 错误率 | SDK 缓冲后丢弃，计数 `aegis_otel_export_dropped_total` | **降级**：回放增强丢失不影响 DecisionRecord 审计（I6 事实源在 CRD，§4.2） |
| Prometheus 不可用 | 抓取失败 | 指标断档，无主动处置 | 接受：闸门功能不依赖指标；恢复后自然续传 |
| 配置错误（端点/key 缺失） | 启动校验 | 组件拒绝启动 | fail-fast：配置错 = 环境坏，响在早期 |
| 预算耗尽（F4） | `aegis_session_budget_used` 达 GuardrailPolicy.sessionBudget 上限 | 执行点在闸门 session 上下文：暂停会话并输出当前结论与置信度 | 观测层只呈现与留痕，不执行处置 |
| 注入命中（F2） | sanitizer 模式命中 | 告警 + 数据衍生动作自动升 R2（闸门行为）；观测层保证三处可见 | 降级不适用：这是安全事件，通道必须可达 |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- **结构化日志字段**：ts、level、component、pipeline、signal、action、result、error_class、dropped_total、duration_ms。
- **自观测指标**：collector 进程自身健康（接收/导出速率）、`aegis_otel_export_dropped_total`。
- **Trace span 命名规范**：gatekeeper.risk.classify、gatekeeper.opa.eval、gatekeeper.approval.decide、gatekeeper.exec.op、gatekeeper.rollback.exec、controller.reconcile、session.tool.call、sandbox.exec——会话→闸门→工具→集群全链命名，供 Langfuse 回放按名检索。
- **审计留痕点**：DecisionRecord（`spec.llmTraceRef` 指向 Langfuse trace，`spec.sanitizationEvents` 记录注入命中，I4/F2）；熔断复位 CLI 操作留痕（F7）；eval 报告归档（M3，T3.3）。

## 7. 依赖方向与模块边界

- **谁调我**：`scripts/env-up.sh` 按序安装；开发者经浏览器访问 Grafana/Langfuse UI。
- **我调谁**：docker-compose（Langfuse）；Prometheus/Grafana/OTel collector 官方组件；`deploy/versions.md`（版本）。
- **禁止依赖谁**：不依赖 `internal/` 业务逻辑；collector 配置只含管道与采样/脱敏参数，不含放行规则（判定永远在闸门，设计文档 §4.1）；各进程只依赖 OTLP 抽象，不反向依赖本目录的具体文件路径。

## 8. 测试策略与红队用例

- **T0.5 完成判据**：一次手测 HTTP 调用的 trace 在 Langfuse 可见。
- **M1 验收依赖**：OOMKilled 诊断会话的 trace 完整可查（G4 支撑）。
- **M2 演示②的告警可见性**：注入命中后 CLI/日志/指标三处可见，是 T2.7 完成判据的一部分。
- **红队用例**：观测通道不得泄露 secret（接入三要素只走环境变量，入库扫描）；降级路径演练（停 collector，验证各进程不阻塞且日志标注降级）是 I10 失效演练的组成部分；伪造 trace 注入DecisionRecord 的尝试在 CRD 校验层被挡（`spec.llmTraceRef` 只作引用不作证据）。
