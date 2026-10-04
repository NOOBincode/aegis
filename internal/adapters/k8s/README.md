# internal/adapters/k8s — 目标集群访问的唯一出口

> 所属层：控制面/适配器层 ｜ 里程碑：M1（只读通道）–M2（写通道、informer、注册表）｜ 设计文档出处：§3.3、§4.2、§4.2.4 ｜ 关联不变量：I9（先归因后行动）、I10（失效姿态自明）｜ 关联失败模式：F5（读操作打爆 apiserver）、F14（分区/在途悬挂）

## 1. 设计初衷

多个限界上下文需要集群数据（authority-map 的对象清单与字段归属、attributor 的事件时间线原料）与集群写能力（dry-run、R1 三族直写），但铁律 2 禁止 client-go 进入任何上下文。本目录是 client-go 的唯一合法住所，把"读什么、怎么写、怎么算冲突"全部收口。

它还承担两件安全件，不是普通封装：写路径的乐观并发与冲突检出（§4.2.4-2，检出即放弃重新归因），和读路径的 QPS 计量上报会话预算（§5.2 rateLimit.maxReadQPS，F5）。这两件的语义正确性是闸门可信的前提——冲突蛮写会制造 I9 所防的对抗振荡，读限流失效会让 F5 直接兑现为 apiserver 事故。

## 2. 职责与任务清单

1. 目标集群注册表：clusterRef → 客户端配置的映射（§4.2；本期恒为单集群，多集群为 N6 预留接缝，§5.1 clusterRef 字段先行）。
2. 只读通道：为 authority-map/attributor 提供对象清单、managedFields 归属解析、事件时间线原料；支撑只读工具的分页与上限（§4.1：单次 ≤500 行日志/≤200 对象）。
3. informer/cache：authority 图谱的数据源（§4.2.2），输出带 resourceVersion 的快照。
4. dry-run 封装：server-side dry-run 统一入口，支撑 `dryRunResult` 与 `inverseVerified`（§5.1）两类字段。
5. 写执行：R1 直写三族（scale_deployment / restart_pod / cordon_node）的窄写，携带 resourceVersion 与 SSA field-manager。
6. 冲突检测：409 识别与"检出即放弃重新归因"的上报（§4.2.4-2），禁止重试蛮写。
7. 统一超时与计量：每次 apiserver 调用有 deadline；QPS 统计上报 `internal/session` 预算。

## 3. 技术选型与开源包

- client-go（typed clientset + dynamic client + informer 包）：选型出处为设计文档 §3.3"控制面语言 Go：CRD/controller 生态唯一正解，OPA、client-go 原生"。
- 本目录禁止 import controller-runtime：depguard 规则限定 controller-runtime 只出现在 `internal/controller` 与 `cmd`（`aegis-ddd-layout`），故 informer/cache 以 client-go 自建，不复用 manager 的 cache。
- 版本以 `deploy/versions.md` 钉死为准，与 CI/本地同源（`aegis-ci`）。

## 4. 具体设计

### 4.1 clientset 与 dynamic client 的使用边界

| 通道 | 用途 | 理由 |
| --- | --- | --- |
| typed clientset | 固定 GVK 的窄读写：Pod / Deployment / Node / Event / HPA / VPA 等核心对象 | 类型安全、字段级访问；只读工具族与 R1 写族的固定对象走这里 |
| dynamic client | 开放遍历：operator CRD 管辖范围、ArgoCD Application、KEDA ScaledObject 等 authority-map 需要的跨 GVK 清单 | authority 图谱按"谁拥有什么字段"建模，无法为每种三方 CRD 生成类型；dynamic + Unstructured 是唯一可行面 |

边界规则：dynamic 的遍历范围必须来自注册表/配置白名单，禁止无界全集群扫描（I7/F5 的输入面控制）。

### 4.2 informer/cache 设计（authority 图谱的数据源）

1. 每集群一组 informer，按 namespace/label 建索引；authority-map 消费 cache 快照而非直查 apiserver——§4.2.2 的确定性图谱需要低延迟、可重复读。
2. cache 数据带 resourceVersion；归因（attributor）属只读分析，允许秒级滞后，一致性级别在本条明示。
3. 写路径不依赖 cache：任何写操作前对目标对象做 fresh GET 取最新 resourceVersion（§4.2.4-2 的前提）。
4. watch 断线退避复用 client-go 自带机制；reflector 长期失败计入下游健康信号并上报 circuit-breaker（F14 的检测面）。

### 4.3 dry-run 封装

统一入口接收"工具 + 参数 + 目标对象"，转 server-side dry-run 请求，返回结构化结果：校验错误列表、变更摘要（diff 规模）、是否可执行。三类调用方：ChangeRequest 的 `dryRunResult`（§5.1）；rollbacker 的逆操作验证 `inverseVerified`（I5；逆操作生成后必须 dry-run 验证通过，失败则升级级别，§4.2.2）；回滚前的漂移复验（F6：目标已被第三方修改时拒绝自动回滚，升级人工并附漂移 diff）。dry-run 本身就是只读语义，天然适合作为一切写之前的必经步。

### 4.4 resourceVersion / SSA 冲突检测（§4.2.4-2）

1. 写请求必须携带执行前 fresh GET 得到的 resourceVersion。
2. SSA 场景使用固定 field-manager 名（命名约定：以 gatekeeper 身份可辨识为准），使冲突可归属到"闸门 vs 其他控制器"。
3. 收到 409 Conflict 时翻译为领域错误"冲突：需重新归因"并上报 attributor；禁止任何重试与蛮写。
4. 冲突事件计入 authority 图谱的写入竞争记录，反哺 I9"被干扰是默认环境"的判定素材。

### 4.5 统一超时与 QPS 计量

1. 每次调用设 deadline（默认值以部署配置为准）；写路径超时即 fail-closed（见 §5）。
2. 每次调用按 verb 计量，上报 `internal/session` 的 apiserver QPS 预算（§5.2 rateLimit.maxReadQPS，F5）；超限返回领域"限流"错误，由上层转 429 + 分页引导（§4.1）。
3. 计量只数调用次数与返回体量，不记录对象内容。

### 4.6 目标集群注册表（§4.2）

注册表维护 clusterRef → kubeconfig/rest.Config 的映射；本期注册表恒为单集群（默认集群），`clusterRef` 字段先行预留（§5.1）避免多集群期重写契约（N6 原文）。注册表接口按多集群形态设计、实现不升格；任何硬编码"默认集群"绕过注册表的捷径视为违反 N6 纪律。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级及理由 |
| --- | --- | --- | --- |
| apiserver 不可用 / 网络分区 | 连接错误、健康探测、informer 全断 | 写路径阻塞 + 告警；读路径按 I10 降级只读直连并明确标注降级态 | 写 fail-closed：I10 原文"安全组件的故障不能静默放行"，更不许拖垮被管对象 |
| 调用超时 | context deadline exceeded | 写操作拒绝并告警；读操作可标注后重试 | 写 fail-closed；超时降级为放行是一票否决的 bug（`aegis-go-quality` 原文） |
| 429 限流 | 状态码 | 翻译为领域限流错误，上层转 429 + 分页引导 | 降级：限流是可恢复状态，引导退避而非拒绝服务（F5） |
| 409 冲突 | resourceVersion / SSA field-manager | 放弃并上报重新归因，不重试 | 既不是放行也不是硬拒：升级给归因层裁决（I9；§4.2.4-2） |
| dry-run 失败 | server 校验错误列表 | 结构化返回；逆操作 dry-run 失败 → 自动升 R2（§4.2.2） | fail-closed 于"回滚可信度"：验证不过即不可宣称可回滚（I5，假安全感比没有回滚更危险） |
| 凭据失效 | 401/403 | 告警；写路径 fail-closed | 认证态不明时的放行会破坏 I1 |
| 返回体超限 | 体量计量 | 截断 + 标记 truncated，配合 §4.1 硬上限 | 降级：只读面允许截断留痕；写路径不存在此问题 |

## 6. 可观测性

- 结构化日志字段：`cluster_ref`、`gvk`、`namespace`、`name`、`verb`、`dry_run`、`latency_ms`、`error_class`、`resource_version`、`qps_window`。
- 指标：`aegis_apiserver_call_total{verb=,result=}`、`aegis_apiserver_latency_seconds_bucket{verb=}`、`aegis_apiserver_qps_current`（gauge）、`aegis_informer_watch_errors_total{cluster_ref=}`。
- trace span：`adapters.k8s.get` / `adapters.k8s.list` / `adapters.k8s.dry_run` / `adapters.k8s.apply`，属性含 `cr_ref`、`risk_level`、`dry_run`。
- 审计留痕点：本目录不直接落审计；执行与 dry-run 结果经调用方写回 CR status，由 `status.auditRef` 关联 DecisionRecord（§5.1/§5.3）；冲突事件的结构化结果是归因判定与事后回放的原料。

## 7. 依赖方向与模块边界

- 谁调我：`internal/authority`（经集群状态读取端口）、`internal/rollback` 与执行路径服务（经 dry-run/执行端口）、`cmd/gatekeeper` 装配。
- 我调谁：仅目标集群 apiserver（含 informer watch）；构造期读注册表配置。无其他出站依赖。
- 禁止依赖谁：禁止 import 上下文 domain/service（只依赖端口接口类型）；禁止 controller-runtime（depguard）；禁止 OPA/MCP/Redis/Langfuse SDK；禁止被 `mcp-servers/` import——工具层经闸门触达集群，不直连 apiserver（§4.1 职责边界）。
- 边界声明：本目录无放行判断（I1）；`internal/controller` 的 reconcile 用 controller-runtime 直管 CR，不经本目录（设计文档 §3.2 分层）。

## 8. 测试策略与红队用例

- 单测：client-go fake client（仅限单测；fake client 不模拟 resourceVersion 冲突，冲突语义用手工桩补足）；表驱动覆盖 §5 错误翻译矩阵（每类别至少一例）。
- 集成：`make test-integration` 打 kind，覆盖 dry-run、R1 三族写、409 冲突路径（对应落地方案 W15"乐观并发测试用例"）；杀进程重启后的 reconcile 重入验证归 `internal/controller` 的 envtest（T2.1 完成判据）。
- 红队用例（M2 检查点支撑）：① 诱导 R3 变体的写请求经 dry-run 全部落入校验错误（配合 OPA 层双保险）；② 复制 MCP 信封重放时，写路径因 idempotencyKey 折叠不产生第二次 apiserver 写（F15 的下游佐证）；③ 目标字段被 HPA 所有时，所有权检查前置拦截直写（F3 双层之一，另一层在 authority-map）。本目录的拒绝语义必须确定性可复现——M2 红队是项目准入门槛，本目录是其底层支撑面。
