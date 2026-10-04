# policies/default — 默认护栏策略包

> 所属层：L2 安全闸门 ｜ 里程碑：M2（落地方案 T2.3）｜ 设计文档出处：§4.2.2 ｜ 关联不变量：I1、I7 ｜ 关联失败模式：F1

## 1. 设计初衷

默认包是 GuardrailPolicy `default`（§5.2 示例 `opaPolicyBundle: policies/default`）指向的开箱基线，覆盖设计文档 §4.2.2 点名的四条默认策略：禁 hostPath、禁特权容器、命名空间白名单、资源配额上下限。意图：

- kind 演示环境与个人集群**开箱即用**（M2 三连演示的底层依赖之一，落地方案 T2.3）。
- 作为后续自定义 bundle 的**样板**：新 bundle 从复制本包起步，评审有参照。
- 把"AI 变更与人类变更共用同一策略库"落到具体规则（§4.2.2）——默认包就是那句原则的可执行形态。

## 2. 职责与任务清单

1. 承载四条默认策略及各自正反测试用例（§4、§8）。
2. 定义本 bundle 的违例消息文本规范（面向审批人与审计）。
3. 与 GuardrailPolicy.spec 的代码级强制（`hardDeny`、`blastRadiusQuota`）保持分工不重叠（§4 分工表）。
4. 作为策略评审样板：组织方式、测试密度、消息格式被新 bundle 复制。

## 3. 技术选型与开源包

同 `policies/README.md` §3：OPA（Rego）+ OPA 内置测试运行器，CI 跑 `opa test`。本包不引入任何额外依赖；OPA 版本以 `deploy/versions.md` 钉死为准。本包不含代码与脚本，纯声明资产。

## 4. 具体设计（不写代码）

**四条默认策略**（逐条意图）：

| 策略 | 意图 | 拦截的变更形态 | 对应不变量/失败模式 |
| --- | --- | --- | --- |
| 禁 hostPath | 节点文件系统挂载是容器逃逸跳板，R3 级风险（§4.2.1 定义列名 hostPath） | 任何将 hostPath 引入 Pod spec 的对象变更（直挂卷与间接组合） | I1 / F1 |
| 禁特权容器 | `privileged: true` 即 R3 级提权（§4.2.1） | 安全上下文声明特权的对象变更 | I1 / F1 |
| 命名空间白名单 | Agent 只在业务命名空间活动；kube-system/gatekeeper-system 已由 hardDeny 覆盖，本策略补业务侧边界 | 目标命名空间不在白名单内的写操作 | I1 |
| 资源配额上下限 | 对象级资源声明的合理区间：副本数上下限、requests/limits 的上下界，防配置错误型扩缩与资源失控 | 超出区间的资源类变更 | I7 |

**输入文档结构**：复用 `policies/README.md` §4 的输入契约。本包四策略实际消费字段：`input.request.target.namespace`（白名单）、`input.request.manifest.spec.*`（hostPath、特权上下文、资源字段）、`input.context.policy`（白名单与配额数值来源——由绑定的 GuardrailPolicy 摘录注入，本包不硬编码数值）。

**违例消息格式**：五元组（策略标识/规则标识/字段路径/解释文本/建议处置）。消息语义示例："default/no_host_path：变更在字段 spec.template.spec.volumes[0] 引入 hostPath 卷，违反禁 hostPath 策略；处置：拒绝"。消息人写人读；CI 用例校验每条 violation 非空且含字段路径。

**与 hardDeny 的分工**（本包设计的关键边界）：

| 维度 | hardDeny（代码级强制） | Rego 策略（本目录） |
| --- | --- | --- |
| 性质 | GuardrailPolicy.spec.hardDeny 清单，opa-eval 代码执行 | 可评审、可演进的策略包 |
| 覆盖 | R3 硬禁止：delete namespaces、secrets 明文、kube-system、clusterroles 等（§5.2 清单） | 对象形态级规则与业务软边界 |
| 可关闭性 | **不可被策略关闭；无审批入口**（§4.2.1 R3） | 换 bundle / 改 GuardrailPolicy 可调整（走 PR 评审） |
| 失效姿态 | 闸门不可用整体 fail-closed（I10） | 同左（装载失败即拒绝） |
| 执行顺序 | 先短路：命中即 Rejected，不再消耗策略求值 | 未命中 hardDeny 的变更进入 Rego 求值 |

关键原则：**hardDeny 是 I1 的代码级兜底，任何策略都无权放宽它**；策略只能比 hardDeny 更严，不能更松。四条策略均 deny 默认（`policies/README.md` §4 默认立场）。

## 5. 错误处理与失效语义

通用类别同 `policies/README.md` §5（装载失败、求值超时、输入非法，全部 fail-closed），本包补充：

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| `input.context.policy` 缺白名单/配额数值 | 求值前 schema 校验 | 白名单按空集判定、配额按最严界判定——即拒绝一切目标写操作 | fail-closed：宁可全拒不可全放（空配置≠无限制） |
| 配额数值类型非法（非数值字符串） | 同上 | 拒绝并记 `policy.input_invalid`，指向字段 | fail-closed |
| 单条策略测试缺失 | CI `opa test` + 用例计数检查 | bundle 级红 | fail-closed（流程级） |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

通用埋点同 `policies/README.md` §6。本包补充**按策略粒度的违例计数**：`aegis_policy_violations_total{policy="no_host_path|no_privileged|ns_whitelist|resource_bounds"}`——白名单违例的持续抬头提示命名空间治理漂移，配额违例抬头提示 Agent 提议尺寸演化，均为反哺分级规则调优的输入（F8 审批疲劳缓解的数据面）。违例五元组全文进 ChangeRequest status 与 DecisionRecord；审批人在 approval-svc 审批视图可见（§4.2.1 R2 人审附件要求"附诊断链+影响面报告"）。

## 7. 依赖方向与模块边界

同 `policies/README.md` §7。本包特有的两条边界：

- 本包**不引用其他 bundle**，策略间不互相 import（每条策略独立可测）。
- 白名单与配额数值**不硬编码于策略文件**：单一事实源在 GuardrailPolicy CRD（`spec` 白名单经 `input.context.policy` 注入），策略是执行者不是配置者——改数值走 GuardrailPolicy 变更评审，改判定逻辑走策略 PR，两条线不混。

## 8. 测试策略与红队用例

- 每条策略正反用例（T3.2 同等纪律，策略侧先行于 M2）：
  - no_host_path：正例 emptyDir 变更放行；反例 hostPath 直挂与间接组合拒绝。
  - no_privileged：正例非特权变更放行；反例 privileged true 拒绝。
  - ns_whitelist：正例白名单内命名空间；反例白名单外命名空间与命名空间缺失。
  - resource_bounds：正例区间内；反例超上限、低于下限、数值非法。
- 红队用例：
  1. **等价变体**：大小写差异、initContainers 内特权声明；MVP 工具集不含 ephemeral containers 操作，相关用例标注范围外并留档（M2 检查点 #2"越权变体"的策略侧对应）。
  2. **双重覆盖验证**：命中 hardDeny 的请求应被代码层先拒——用例验证该路径不依赖策略兜底，防"策略被换包后 R3 失守"。
  3. **空配置攻防**：GuardrailPolicy 摘录缺失时验证最严界判定（§5）。
- 人审：四条默认策略与各自用例在 M2 过 R-2 逐行人审（落地方案 W12 时段），后续变更单独 PR。
