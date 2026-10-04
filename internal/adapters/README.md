# internal/adapters — 出站适配器层总述

> 所属层：控制面（L2 闸门与各限界上下文的出站实现层）｜ 里程碑：M1–M2（逐适配器见各篇）｜ 设计文档出处：§3.2、§3.3 ｜ 关联不变量：I10（失效姿态自明）｜ 关联失败模式：F14（闸门与外部系统分区）

## 1. 设计初衷

设计文档 §3.2 铁律 2 要求"领域纯净"：风险分级表、审批状态机、归因判定必须与 K8s 客户端、OPA、MCP SDK 解耦，可纯单测。本目录就是这条铁律的落地形态：所有与外部系统（apiserver、OPA 引擎、Redis、Langfuse、工具 server）的交互收口在 `internal/adapters/<name>/`，各限界上下文只在 `ports.go` 声明出站接口，不感知实现技术。

第二个初衷是可替换性：适配器是按端口契约可整体替换的实现。今天 OPA 以 Go SDK 进程内嵌，日后策略引擎升级或更换，改动只落在 `internal/adapters/opa/` 与端口调用处，领域规则不动。

第三个初衷是纪律统一：超时、重试、熔断、错误翻译、凭据处理若分散在各上下文，必然漂移。本目录以同一套适配器纪律（见 §4.3）约束全部出站调用，并保证各适配器的失效语义可预期——这是 I10"闸门不可用 = Agent 不可用 ≠ 集群不可用"在代码结构上的表达。

## 2. 职责与任务清单

- 实现各限界上下文 `ports.go` 声明的出站接口（映射总表见 §4.2）。
- 对每个外部系统封装：连接建立与池化、调用超时、重试与熔断上报、错误翻译为领域错误、调用计量。
- 提供各适配器的 fake/内存实现，供服务层单测与 CI 使用（见 §8）。
- 不承载任何业务规则：风险分级、放行判断、状态机推进一律不属于本目录（铁律 1、I1）。

## 3. 技术选型与开源包

| 适配器 | 底层技术 | 选型出处 | 版本纪律 |
| --- | --- | --- | --- |
| k8s | client-go（clientset、dynamic client、informer） | 设计文档 §3.3（控制面 Go：生态唯一正解） | 以 `deploy/versions.md` 钉死为准 |
| opa | OPA Go SDK（进程内嵌，非 sidecar，理由见该篇 §1） | 设计文档 §3.3、§4.2.2 | 同上 |
| mcp | 官方 Go MCP SDK | 设计文档 §3.3（MCP 为 Agent↔工具事实标准） | 同上 |
| redis | Redis/Valkey 协议客户端 | 设计文档 §3.3（会话粘性、TTL 天然匹配） | 同上 |
| langfuse | OpenTelemetry Go SDK + OTLP exporter，对接自托管 Langfuse | 设计文档 §3.3、§4.6 | 同上 |

本文档不出现任何具体版本号；新增或升级依赖按 `aegis-go-quality` 纪律：PR 写明理由与 license（仅 MIT / Apache-2.0 / BSD），能用标准库解决不加依赖。

## 4. 具体设计

### 4.1 目录与装配

每个适配器一个子目录：`internal/adapters/k8s/`、`opa/`、`mcp/`、`redis/`、`langfuse/`，各配一篇 README 说明机制细节。构造依赖由 `cmd/`（gatekeeper、controller、aegis-cli）在装配时注入；配置经 flag/env 进入，禁止在包内硬编码地址、凭据路径、超时值。各进程启动时对适配器做健康自检，自检口径见各篇 §5。

### 4.2 ports & adapters 映射总表

端口类型名称以各上下文 `ports.go` 的声明为准，下表为设计口径的职责描述（入参 → 出参语义）：

| Port（声明位置） | 职责（入 → 出语义） | Adapter 实现 | 消费方 |
| --- | --- | --- | --- |
| `internal/policy/ports.go` 策略求值端口 | 入：策略输入文档（CR 意图 + 目标对象清单 + 会话上下文）；出：allow/deny + 结构化违例列表 | `internal/adapters/opa` | `internal/policy`（opa-eval 编排） |
| `internal/authority/ports.go` 集群状态读取端口 | 入：对象选择器与字段路径；出：对象清单、`metadata.managedFields` 归属解析、事件时间线原料 | `internal/adapters/k8s` | `internal/authority`（authority-map、attributor，I9） |
| `internal/rollback/ports.go` dry-run 验证端口 | 入：逆操作定义（工具 + 参数）；出：server-side dry-run 通过/失败与校验信息 | `internal/adapters/k8s` | `internal/rollback`（rollbacker，I5） |
| 执行端口（执行路径上下文的 `ports.go`） | 入：已放行操作 + 乐观并发前提（resourceVersion）；出：执行结果或冲突错误 | `internal/adapters/k8s` | 闸门执行路径（R1 直写三族） |
| `internal/session/ports.go` 会话存储端口 | 入：会话注册、预算计数、冷却期读写；出：当前值与剩余额度 | `internal/adapters/redis` | `internal/session`（§5.2 sessionBudget 执行点） |
| `internal/audit/ports.go` trace 记录端口 | 入：会话标识；出：`llmTraceRef`（§5.3）与 trace 写入能力 | `internal/adapters/langfuse` | `internal/audit`（DecisionRecord 组装） |
| 闸门 MCP 代理出站通道（装配层声明） | 入：已通过闸门校验的工具调用；出：工具执行结果 | `internal/adapters/mcp` | `cmd/gatekeeper` 装配的强制代理（§3.1） |

### 4.3 适配器共同纪律

1. 超时、重试、熔断：每个出站调用必须有 `context.Context` 截止时间；重试仅限幂等安全操作，带退避并计数；对 apiserver、Redis、工具 server 的连续失败接入 circuit-breaker 的下游健康信号——熔断判定本身在 `internal/breaker`，本目录只上报，不做判定。
2. 错误翻译：所有底层错误包装为领域错误（`%w` 链），调用方可用 `errors.Is/As` 判定类别（冲突、超时、不可用、被拒绝），禁止把底层库的错误类型泄露出端口边界。
3. 连接池：client-go 的 QPS/burst、Redis 连接池等资源在构造期建立，禁止逐调用创建连接。
4. 凭据纪律：kubeconfig、token、信封密钥只经构造注入；结构化日志只记录连接标识（集群名、端点地址）与错误类别，绝不记录凭据内容与请求/响应体全文。

### 4.4 fake 与测试替身

- 上下文领域单测（铁律 2 要求的"纯单测"）不使用本目录任何代码，由各上下文测试包内的手写 fake（实现端口接口）承担——fake 不进 `internal/adapters/`，避免上下文测试反向依赖适配器（depguard 的 `internal/adapters` 拒绝规则对测试代码同样成立）。
- 服务层测试可使用适配器提供的内存版实现（如 OPA 内存引擎、client-go fake client）；按 `aegis-go-quality`，fake client 仅限单测，集成测试一律打 kind（`make test-integration`）。
- 每个适配器在其 README 列出自己维护的 fake 与适用场景。

## 5. 错误处理与失效语义

统一错误类别（各适配器在自己的 README 展开到具体机制粒度）：

| 错误类别 | 检测手段 | 统一处置 | 相关 F/I |
| --- | --- | --- | --- |
| 下游不可用（连接失败、DNS、拒连） | 连接错误 + 健康探测 | 写路径 fail-closed 并告警；读路径按 I10 可降级并标注 | F14、I10 |
| 调用超时 | context deadline exceeded | 超时即拒绝写操作（fail-closed；超时降级为放行是一票否决的 bug） | I10 |
| 被下游限流（如 apiserver 429） | 状态码/错误类别 | 翻译为领域"限流"错误，由上层转 429 + 分页/退避引导 | F5 |
| 乐观并发冲突（409） | resourceVersion / SSA field-manager 冲突 | 放弃本次写并上报"需重新归因"，禁止蛮写重试 | I9、§4.2.4-2 |
| 凭据失效（401/403） | 认证错误 | 告警 + 写路径 fail-closed | F14 |
| 结果无法翻译（未知格式） | 反序列化/schema 校验失败 | 视同下游不可用处理，不留半解析状态 | — |

总则：适配器永不静默吞错（禁止 `_ =` 与空分支，错误信息小写无结尾标点）；凡涉及写路径的下游故障，默认 fail-closed——I10 的原文理由是"安全组件的故障不能静默放行"。读路径的降级必须显式标注降级态，不得假装正常服务。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志字段（英文 snake_case，全层统一）：`adapter_name`、`operation`、`target_ref`（外部系统标识，非凭据）、`latency_ms`、`error_class`、`retry_count`、`timeout_budget_ms`。
- 统一指标前缀：`aegis_adapter_call_total{adapter=,operation=,result=}`、`aegis_adapter_latency_seconds_bucket{adapter=,operation=}`；各适配器补充专属指标（见各篇 §6）。
- 统一 trace 命名：出站 span 以 `adapters.<name>.<operation>` 命名（如 `adapters.k8s.dry_run`、`adapters.opa.eval`），作为闸门 span 的子级；span 属性只放标识与判定结果，不放内容体。
- 审计边界：本目录不产生审计记录；审计留痕点在 `internal/audit`（DecisionRecord）与 CR status（`status.auditRef`，§5.1）。适配器只负责把可留痕的原料（调用结果、违例结构、计量数据）正确送回。

## 7. 依赖方向与模块边界

- 谁调我：`cmd/`（构造注入）；各上下文 service（经各自 `ports.go` 接口）；`cmd/gatekeeper` 装配的 MCP 强制代理。
- 我调谁：仅外部系统（apiserver、进程内 OPA 引擎、Redis/Valkey、工具 server、otel-collector/Langfuse）与各上下文 `ports.go` 的接口定义（依赖倒置，不触碰对方 domain 实现）。
- 禁止依赖谁：禁止 import 任何上下文的 domain/service 实现（只依赖端口接口类型与输入输出值对象）；适配器之间禁止相互依赖（如 opa 适配器不得 import redis 适配器）；禁止被领域层 import（depguard 强制，规则见 `aegis-ddd-layout` 与 `.golangci.yml`）。
- 本目录不承接的职责：放行/拒绝判断（I1，属 L2 领域）、审计本体（`internal/audit`）、熔断状态机（`internal/breaker`，本目录只上报健康信号）、CRD reconcile（`internal/controller`，用 controller-runtime 直管 CR，不经本目录）。

## 8. 测试策略与红队用例

- 契约测试：每个适配器维护"端口契约测试"，证明其满足对应 `ports.go` 语义，覆盖正常路径与错误翻译矩阵（§5 每类别至少一例）。
- 单元测试：表驱动为默认形态；client-go fake client 仅限单测；并发路径配 `go.uber.org/goleak` 且 `go test -race` 默认开启（`aegis-go-quality`）。
- 覆盖率：适配器按 ≥60% 务实线执行；涉及写路径 fail-closed 的错误分支必须被覆盖，不刷数字。
- 红队支撑关系：M2 红队检查点（项目准入门槛）的审批绕过、重放、越权、会话预算绕过四类用例，底层都依赖适配器给出确定性的拒绝语义；各适配器在自己的 README 列出直接支撑的红队用例。适配器自身不单独设检查点，但任何适配器变更须对照 I1–I10 检查单过一遍（`aegis-security-review`）。
