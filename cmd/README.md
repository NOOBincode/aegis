# cmd — 装配层总述

> 所属层：控制面装配层（cmd/）｜ 里程碑：M1–M3（三个入口分期见 §2 与各子目录文档）｜ 设计文档出处：§3.1、§3.2 ｜ 关联不变量：I10 ｜ 关联失败模式：F14、F15

## 1. 设计初衷

cmd/ 在 aegis 里只做一件事：把 `internal/` 中已经设计好的限界上下文与适配器装配成可运行的进程。设计文档 §3.2 对 cmd/ 的注释原文是"仅 main + flag + 依赖注入，禁止业务逻辑"。这条铁律带来四个具体收益：

- **可测性**：业务规则全部留在各上下文的 domain/service，可纯单测（设计文档 §3.2 分层规则 2）；如果业务逻辑爬进 main，就只能靠起进程验证，测试成本指数上升。
- **架构守卫可机检**：依赖方向五铁律由 depguard 在 CI 强制（`.golangci.yml`，见 `.agents/skills/aegis-ddd-layout/SKILL.md`），cmd 层越薄，规则越容易被机械执行，不需要靠评审肉眼盯。
- **人审成本收敛**：落地方案 R-2 要求 L2 安全包（`internal/risk`、`internal/policy`、`internal/approval`、`internal/rollback`、`internal/breaker`、`internal/authority`、`internal/sanitize`、`internal/audit`、`internal/session`、`internal/controller`）AI 产出逐行人审。cmd 禁止业务逻辑后，人审火力可以全部集中在这些目录。
- **三进程骨架一致**：gatekeeper、controller、aegis-cli 共享同一套 main 骨架要素（见 §4），行为差异全部来自装配对象而非骨架本身，降低单人项目长期维护中的样板漂移。

## 2. 职责与任务清单

**职责白名单**：解析 flag、装载配置、初始化日志与 OTel、装配依赖（构造 adapters 与上下文 service 并接线）、启动探针端点、信号处理与优雅退出。

**职责黑名单**：任何业务规则（风险分级、策略求值、状态机、审批判定）、任何对 `internal/` 领域对象的二次加工、任何"临时先写在这"的 helper。

三个入口进程：

| 进程 | 定位 | 部署形态 | 首个里程碑 | 详细设计 |
| --- | --- | --- | --- | --- |
| `cmd/gatekeeper` | L2 安全闸门主服务（MCP 代理 + 同步判定链路） | 集群内 Deployment，无状态多副本 standby | M2 | `cmd/gatekeeper/README.md` |
| `cmd/controller` | CRD controller（状态机推进 + 在途恢复） | 集群内 Deployment，leader election 单写者 | M1（DecisionRecord）/ M2（ChangeRequest、GuardrailPolicy） | `cmd/controller/README.md` |
| `cmd/aegis-cli` | 用户交互 CLI（视图与提交，无服务端逻辑） | 运维人员本机 / CI 中运行，短进程 | M1（replay） | `cmd/aegis-cli/README.md` |

统一 main 骨架九要素：① flag 解析；② 配置加载（环境变量 + flag 双层）；③ 结构化日志初始化（log/slog）；④ OTel SDK 初始化与优雅 flush；⑤ `/healthz` `/readyz` 探针；⑥ 信号处理；⑦ 依赖装配；⑧ 运行入口启动；⑨ 优雅退出序列。三个入口的骨架步骤顺序一致，仅第 ⑦ 步的装配对象不同。

## 3. 技术选型与开源包

| 要素 | 选型 | 理由 | 放弃项及原因 |
| --- | --- | --- | --- |
| flag 解析（三进程统一） | **spf13/pflag**（daemon 仅用 pflag 本体；aegis-cli 经 cobra 间接使用） | 全仓 flag 语法一致（GNU 双横线长旗标）；aegis-cli 的子命令树必须 cobra，cobra 底层即 pflag，三入口统一 pflag 后运维与文档只需一套语法 | 标准库 `flag`：单横线语法、解析至首个非 flag 参数即停、不支持子命令树，与 CLI 侧语法分裂 |
| 配置加载 | **环境变量 + flag 双层**（优先级：flag > 环境变量 > 硬编码默认值），不引配置文件层 | 配置面小（kubeconfig 路径、监听地址、命名空间、OTel 端点、日志级别），双层足以覆盖；顺序确定、可单测 | **viper**：重型传递依赖、覆盖顺序隐式、引入配置文件与热更新语义（非需求），与 aegis-go-quality"能用标准库解决不加依赖"的依赖纪律冲突 |
| 结构化日志 | 标准库 **log/slog**（JSON handler） | 零新增依赖；结构化字段满足 §6 要求 | zap/logrus：非必要第三方依赖 |
| OTel | **OpenTelemetry Go SDK**（OTLP exporter 指向 collector；collector 版本以 `deploy/versions.md` 钉死为准） | 设计文档 §3.3 观测选型：OTel 贯穿 会话→闸门→工具→集群 | 各组件自埋点：trace 无法串联 |
| 探针 | daemon 用标准库 HTTP ServeMux 暴露；controller 复用 controller-runtime manager 自带 healthz/readyz 挂载点（T2.1 工程形态） | 不重复造探针框架 | 私有探针协议：k8s 原生探针已够 |
| 信号处理 | 标准库 signal（NotifyContext 语义） | 零依赖，支持 SIGTERM/SIGINT 统一取消 | — |
| 进程框架 | controller-runtime（cmd/controller 主干；cmd/gatekeeper 复用其 leader election 语义，I10） | T2.1 明确 controller-runtime 工程；版本以 `deploy/versions.md` 钉死为准 | 自研选举/缓存：重复造轮子 |

## 4. 具体设计（不写代码）

**统一 main 骨架**（三入口同一顺序，差异仅在第 6 步装配对象）：

1. 定义 flag 集合并解析；解析失败立即打印用法并以退出码 2 退出（见 §5）。
2. 装载配置：按"flag > 环境变量 > 默认值"合成配置结构；配置非法（如监听地址不可解析）以退出码 2 退出并说明哪一层来源出错。
3. 初始化 slog：JSON handler，级别取自配置；此后所有输出走 slog，禁止 fmt 直打。
4. 初始化 OTel SDK：trace provider + OTLP exporter；记录初始化结果，失败处理见 §5。
5. 启动探针 HTTP 端点：`/healthz`（liveness）与 `/readyz`（readiness），语义见下表。
6. 依赖装配：构造 adapters（k8s client-go、opa、mcp、redis、langfuse 等按进程需要）→ 构造各上下文 service 并注入 ports 实现 → 构造进程特有运行时对象（MCP 代理 / reconcile 管理器 / CLI 根命令）。
7. 就绪前自检：daemon 在 readyz 翻 ready 前完成关键依赖探测（见下表）。
8. 启动运行入口并阻塞；同时挂起信号监听。
9. 收到 SIGTERM/SIGINT 后执行 §4 退出序列。

**探针语义表**：

| 探针 | 语义 | gatekeeper | controller | aegis-cli |
| --- | --- | --- | --- | --- |
| `/healthz` | 进程活着（不检查依赖） | 固定 200 | 固定 200 | 无探针（短进程） |
| `/readyz` | 可否接流量 | apiserver 可达 + OPA bundle 已装载 + leader 角色就位 | cache synced + leader election 已获取 | — |

**优雅退出序列**（SIGTERM 触发）：

1. 记录收到信号（signal 字段）并将 readyz 置为 not-ready，停止接新连接/新请求。
2. 停止监听（MCP 代理关闭监听 socket / manager 停 watch / CLI 不适用）。
3. Drain 在途请求：等待在途 MCP 调用与在途 reconcile 收敛，设有上限等待时长。
4. Flush OTel span（force flush 带超时）。
5. 释放 leader lease（controller/gatekeeper）。
6. 以退出码 0 退出；drain 超时等异常路径见 §5。

**退出码约定**（三进程统一，aegis-cli 在其专属文档中扩展业务码）：

| 退出码 | 含义 |
| --- | --- |
| 0 | 干净退出（SIGTERM 正常序列走完） |
| 1 | 运行期未预期错误 / panic |
| 2 | 启动期错误：flag 解析失败、配置非法 |

## 5. 错误处理与失效语义

启动期错误（fail-fast，宁可起不来也不带病运行）：

| 错误类别 | 检测手段 | 处置 | 姿态与理由 |
| --- | --- | --- | --- |
| flag 解析失败 / 未知参数 | 解析器返回错误 | 打印用法，退出码 2 | fail-closed：参数语义不明时拒绝启动，防止默认值静默偏离预期 |
| 配置非法（地址/时长/枚举值） | 装载后校验（来源分层标注便于定位） | 报错并退出码 2 | fail-fast：配置非法时拒绝启动，防止默认值静默偏离预期 |
| apiserver 不可达（daemon） | 启动探测 | 报错并退出码 1 | fail-closed 的镜像：起不来即不 ready，写路径从源头不开放 |
| OPA bundle 装载失败（gatekeeper） | 装载结果检查 | 报错并退出码 1 | 策略面不可用则进程不可用（I1/I10；运行期超时语义见 `cmd/gatekeeper/README.md` §5） |
| OTel exporter 建联失败 | 初始化返回错误 | **进程继续运行**，slog 告警 + readyz 保持 ready + 指标计数 | 观测面故障不得拖垮闸门可用性——I10 要求"平台故障不拖垮被管对象"，闸门自身同理；观测缺失要可发现（指标），不可用假装没有 |
| leader lease 获取失败 | election 回调 | 以 standby 身份活着但不 ready | 多副本 standby 语义（I10），readyz 不翻 ready 即不接流量 |

运行期错误：

- **panic**：装配层不设置 recover，panic 即进程崩溃。理由：L2 安全件"崩溃 = 不可用 = 写阻塞"优于"静默带病运行"；在途 CR 由 controller 状态机重入恢复（F14，见 `cmd/controller/README.md` §4）。
- **drain 超时**：超过上限等待时长仍有在途工作 → 记录 in_flight 数量与超时事实，退出码 1，交给 K8s 重启。理由与 panic 相同：不无限等待，不静默强杀。
- **信号重复**：第二次 SIGTERM 直接强退（退出码 1），防止退出序列卡死。
- **依赖调用错误**：装配层不做重试决策——重试策略属于业务/适配器层（如 client-go 自带重试、幂等键折叠 F15），cmd 只保证"错误被日志完整带出进程边界"。

与 I10/F14/F15 的对应：cmd 层对失效语义的贡献是"死得快、死得可见、死得起"——状态不留在进程内（全落 CRD status），所以任何死亡都不造成不可恢复损失。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

**结构化日志字段**（slog，英文 snake_case，三进程通用字段）：

| 字段 | 出现时机 |
| --- | --- |
| `process` | 每条日志常驻：gatekeeper / controller / aegis-cli |
| `config_source` | 启动期：配置各项来源（flag/env/default） |
| `listen_addr`、`readyz_state` | 探针相关事件 |
| `startup_duration_ms` | 启动完成时 |
| `signal` | 收到 SIGTERM/SIGINT 时 |
| `drain_inflight`、`drain_wait_ms` | 退出序列中 |
| `leader_held` | 选举状态变化（true/false） |
| `exit_code`、`shutdown_reason` | 进程退出前最后一条 |

**指标**（Prometheus，命名前缀 `aegis_`）：

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `aegis_process_ready{process=}` | gauge | readyz 状态 1/0 |
| `aegis_startup_duration_seconds{process=}` | histogram | 启动耗时 |
| `aegis_process_shutdown_total{process=,reason=}` | counter | 退出次数按原因分（sigterm/timeout/panic） |
| `aegis_otel_init_failed_total{process=}` | counter | OTel 初始化失败次数（§5 降级路径） |

**Trace span**：`process.bootstrap`（flag→配置→装配全链路段）与 `process.shutdown`（退出序列段）；业务 span 由各进程文档定义（如 `gatekeeper.risk.classify`，见 `cmd/gatekeeper/README.md` §6）。

**审计留痕点**：cmd 层不产生审计事件。审计是 `internal/audit` 的职责（DecisionRecord，设计文档 §5.3），cmd 只保证 OTel flush 不丢 trace——flush 失败在退出日志中显式记录。

## 7. 依赖方向与模块边界

- **谁调我**：无人。main 包不可被导入（Go 语言机制天然保证）。
- **我调谁**：标准库；第三方装配件（pflag、cobra、OTel、controller-runtime、client-go 凭证装载）；`api/v1alpha1` 类型；`internal/adapters/` 的构造函数；各上下文 `service`/`ports` 的组装入口。装配方向符合铁律 1：`cmd → internal/controller、internal/mcpserver、mcp-servers/* → internal/<context>/service → domain`。
- **禁止依赖谁**：cmd 三个入口互相禁止 import；禁止 import `mcp-servers/*` 与 `internal/controller` 之外的 reconcile 实现；禁止在 cmd 出现条件分支形式的业务规则（评审打回项，`.agents/skills/aegis-ddd-layout/SKILL.md` 反模式清单第一条）；各上下文 `domain` 不得反过来 import cmd。
- **共享骨架策略**：三进程骨架要素一致，但**不抽公共 main 包**、不建 `pkg/`（铁律 5：当前为空即不留目录）。逐进程内聚的少量重复是可接受成本；若未来重复度成为维护负担，升格方案必须先开 ADR（落地方案 R-5）。

## 8. 测试策略与红队用例

cmd 层无业务逻辑，**不设单测义务**；其正确性由以下手段覆盖：

1. **构建门禁**：CI `ci.yml` 的 build job 覆盖全部 cmd 与 mcp-servers（见 `.agents/skills/aegis-ci/SKILL.md`），`make build` 本地同源。
2. **架构门禁**：depguard 违例测试（T0.1 完成判据：构造 domain 文件 import client-go 被拦下）；cmd 若出现业务 import 同样被方向规则拦下。
3. **kind 冒烟**：`kind-smoke.yml` 自 M2 起拉起集群跑 controller 状态机冒烟（见 `cmd/controller/README.md` §8）。
4. **骨架验收清单**（每个要素对应一个可观察现象）：flag 传错 → 退出码 2 + 用法输出；readyz 在依赖未就绪时不翻 ready；SIGTERM 后日志出现 signal/drain_inflight/exit_code 三字段；OTel collector 停掉时进程仍 ready 且 `aegis_otel_init_failed_total` 递增。
5. **红队用例归属**：进程级红队（杀进程 fail-closed、断 apiserver 分区、重放折叠）落在 `cmd/gatekeeper/README.md` §8 与 `cmd/controller/README.md` §8；CLI 绕过参数审计落在 `cmd/aegis-cli/README.md` §8。cmd/README 层只保证这些测试有稳定的进程骨架可打。
