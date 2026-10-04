# internal/adapters/opa — 进程内嵌的策略求值器

> 所属层：控制面/适配器层（L2 闸门的策略执行细节）｜ 里程碑：M2 ｜ 设计文档出处：§3.3、§4.2.1、§4.2.2 ｜ 关联不变量：I1（确定性闸门）、I10（失效姿态自明）｜ 关联失败模式：F1（LLM 幻觉产出危险操作）

## 1. 设计初衷

opa-eval 需要一个求值引擎实现。设计文档 §3.3 已定 OPA/Rego：K8s admission 事实标准，与 Kyverno 策略可复用；放弃自研 DSL（无生态）。本目录回答"以什么形态嵌入"：**Go SDK 进程内嵌，而非独立 sidecar 或远端服务**。四条理由，均从材料决策推出：

1. fail-closed 的简单性：I10 要求策略引擎超时即拒绝（落地方案 T2.3 明示"策略超时/引擎不可用 → 拒绝"）。进程内调用的超时判定路径短；跨进程多一层网络故障面，fail-closed 语义不变但故障类别翻倍。
2. 部署面收敛：个人项目不做平台化外壳（N3），gatekeeper 单进程交付优于"每个实例挂 sidecar"的 YAML 与生命周期运维。
3. 无额外信任边界：sidecar 引入本地监听端口与鉴权管理；内嵌无监听面。
4. 行为同源：策略包开发与 CI 验证同用仓库 `policies/` 目录，AI 变更与人类变更走同一策略库（§4.2.2），不分叉。

## 2. 职责与任务清单

1. bundle 加载：从 GuardrailPolicy.spec.opaPolicyBundle 指向的路径（`policies/<bundle>`）装载 Rego 策略包。
2. 热更新：策略包内容变更后重建求值引擎并原子切换，不重启 gatekeeper 进程。
3. 求值编排：对每个 ChangeRequest 组装策略输入文档并求值（§4.2.2 opa-eval）。
4. 超时控制：每次求值有 deadline；超时或引擎不可用 → 拒绝（fail-closed）。
5. 结果翻译：违例结构化为策略标识、规则名、说明、严重度，供 CR status 与 DecisionRecord 使用。
6. 留痕支撑：求值输入摘要与违例列表作为审计原料送回 `internal/policy`。

## 3. 技术选型与开源包

- OPA Go SDK（进程内嵌形态）：版本以 `deploy/versions.md` 钉死为准；核心依赖版本与 versions.md 对账是 `aegis-go-quality` 的硬性要求。
- 放弃项：sidecar/远端服务形态（理由见 §1）；自研 DSL（设计文档 §3.3 放弃项原文）。
- bundle 源即仓库 `policies/` 目录，随 helm/镜像发布；Rego 策略编写与人审归落地方案 W12，本目录只管装载与求值。

## 4. 具体设计

### 4.1 引擎生命周期

1. 启动：校验 bundle 可编译 → 构建引擎 → 进入服务；校验失败则进程拒绝进入服务态（启动即 fail-closed，见 §5 理由）。
2. 热更新：监听 bundle 内容版本（内容哈希/发布版本）；变更触发"构建新引擎 → 冒烟用例集双跑校验 → 原子切换 → 旧引擎释放"；构建或冒烟失败则保留旧引擎并告警——策略更新失败不回滚安全性。
3. 引擎只读使用：求值并发安全；引擎自身不持有会话态（I10：进程内存不持有不可恢复状态）。

### 4.2 策略输入文档组装

| 输入域 | 内容 | 来源 |
| --- | --- | --- |
| `change_request` | 意图、操作清单（工具 + 参数）、riskLevel、estBlastRadius | CR spec（§5.1） |
| `target_objects` | 目标对象清单（当前 spec 摘要、关键字段值） | `internal/authority` 经 k8s 适配器提供的快照 |
| `session_context` | sessionRef、source（agent/human）、同会话近期变更摘要 | `internal/session` |
| `policy_ref` | 绑定的 GuardrailPolicy 关键字段：hardDeny、blastRadiusQuota、rateLimit、opaPolicyBundle | CR spec.guardrailPolicyRef 所引对象 |

组装原则：输入只放策略判定所需的最小事实集；对象全文不进输入（体积与泄漏面双控），关键字段与内容哈希足矣。

### 4.3 求值与输出契约

- 输出：顶层 allow/deny + 违例列表；违例元素含策略标识、规则名、人类可读说明、严重度（阻断/告警）。
- hardDeny 的双层关系必须澄清：R3 硬拒清单是代码级强制（I1）——risk 查表据同一清单将操作定级 R3，代码级匹配器住在 `internal/policy`、先于 OPA 求值执行且不可被 Rego 关闭（见 `internal/policy/README.md` §4.2，§4.2.1/§5.2）；本适配器的 Rego 层负责策略语义——禁 hostPath、禁特权容器、命名空间白名单、资源配额上下限（§4.2.2 原文列举）。两层都要：代码层兜策略层的盲角（策略没写到的不等于安全），策略层承载可评审的 YAML 语义（GuardrailPolicy 是"可评审的 YAML"，§5.2）。
- AI 与人类变更共用同一策略库、同一输入文档形状（§4.2.2），不允许分叉。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级及理由 |
| --- | --- | --- | --- |
| 策略求值超时 | context deadline | 拒绝该 CR + 告警 | fail-closed：I10 与 T2.3 明示"策略超时 → 拒绝" |
| 引擎不可用（内部错误） | 求值返回引擎级错误 | 拒绝该 CR + 告警；进程级故障则退出服务态 | fail-closed：策略不可执行 = 不可证明安全 = 不放行 |
| bundle 语法/编译错误 | 启动校验、热更新双跑 | 启动期拒绝进入服务态；热更新期保留旧引擎 + 告警 | fail-closed：拒绝启动好于带坏策略放行；热更新失败宁可继续旧策略 |
| 输入文档组装失败 | 上游数据缺失校验 | 拒绝 + 指明缺失域 | fail-closed：输入不全的判定无效 |
| 违例（业务结果） | deny + 违例列表 | CR 置 Rejected + 留痕告警；R3 类硬拒无审批入口（§4.2.1） | 非错误：这是 F1 的主处置路径，不依赖 prompt 护栏（I1） |
| 热更新冒烟失败 | 冒烟用例集未全过 | 不切新引擎 + 告警 | 降级为旧策略继续服务：可用性让位于已验证策略 |

## 6. 可观测性

- 结构化日志字段：`policy_bundle`、`bundle_hash`、`eval_latency_ms`、`violation_count`、`violation_rules`（规则名列表）、`error_class`、`input_digest`（输入摘要哈希，非原文）。
- 指标：`aegis_opa_eval_total{result=allow|deny|error}`、`aegis_opa_eval_latency_seconds_bucket`、`aegis_opa_bundle_reload_total{result=ok|skipped|failed}`。
- trace span：`adapters.opa.eval`，父级为 `gatekeeper.opa.eval`（span 归属见 `internal/policy` 侧文档），属性含 `cr_ref`、`bundle_hash`、`violation_count`。
- 审计留痕点：求值输入摘要（input_digest）与违例结构经 `internal/policy` 进入 DecisionRecord（§5.3 决策链原料）；R3 硬拒的留痕告警同时推送（§4.2.1"留痕告警"原文）。

## 7. 依赖方向与模块边界

- 谁调我：`internal/policy` service（策略求值端口的唯一消费者）。
- 我调谁：进程内 OPA 引擎；bundle 文件系统/挂载卷。不触达 apiserver、Redis、MCP。
- 禁止依赖谁：禁止 import `k8s.io/*`、MCP SDK、Redis 客户端、其他适配器；禁止 import 任何上下文 domain/service（只依赖 `internal/policy/ports.go` 的接口与输入输出值对象）。
- 边界声明：风险分级不在本目录（risk-classifier 是确定性查表，属 `internal/risk`）；本目录对"这条变更是否违规"作答，不对"这条变更有多危险"作答，两者不可混（I1 的语义边界）。

## 8. 测试策略与红队用例

- 单测：表驱动求值测试，fixture 覆盖 §4.2.2 策略清单逐项（禁 hostPath、禁特权容器、命名空间白名单、配额上下限）；bundle 编译检查进 CI lint job。
- 契约测试：与 `internal/policy` 的端口契约测试对齐错误翻译（超时 → 领域超时错误等 §5 矩阵）。
- 红队用例（M2 演示③与准入门槛的底层）：① 删 namespace / 读 secret 明文 / 动 kube-system / 改 clusterrole 全部硬拒且留痕告警（落地方案 T2.3 完成判据原文，代码层硬清单与 Rego 层各挡一次）；② 构造绕过变体（间接对象、参数拼装）不得使 deny 漏报——每加一条 Rego 策略随 PR 附此类用例（红队注入测试为准入门槛，设计文档 §9）；③ 引擎故障注入（注入延迟/销毁引擎）→ 写路径拒绝且告警（I10 验收方法原文：fail-closed 测试）。
