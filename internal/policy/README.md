# policy — 策略求值端口（opa-eval 的语义层）

> 所属层：L2 安全闸门（控制面） ｜ 里程碑：M2（落地方案 T2.3） ｜ 设计文档出处：§4.2.2、§5.2 ｜ 关联不变量：I1、I10 ｜ 关联失败模式：F1、F14

## 1. 设计初衷

闸门的第二道判定是策略校验。本上下文刻意只做一件事：定义"策略求值"端口的语义，把 OPA 降级为可替换的适配器实现细节——OPA SDK 只允许出现在 `internal/adapters/opa`（depguard 强制）。理由是 I1 要求放行/拒绝由代码与策略共同决定，而求值语义（输入是什么、输出是什么、失败时怎么办）必须先于任何引擎选型固定下来，否则引擎替换将改变安全语义。

两条设计红线直接来自必读材料：其一，hardDeny 是代码级强制（§5.2），先于 OPA 执行，且不可被 Rego 策略关闭——改策略包不能豁免硬拒条目；其二，策略超时、引擎不可用一律拒绝（fail-closed，I10/F14）——"策略层挂了先放行"是一票否决的缺陷（与 `aegis-go-quality` 的"超时即拒绝"同口径）。AI 变更与人类变更共用同一策略库（§4.2.2）。

## 2. 职责与任务清单

1. 定义 PolicyEngine 端口语义：输入 = CR manifest + 求值上下文，输出 = allow/deny + 违例清单（见 §4.1）。
2. 编排两级求值：先 hardDeny 代码级匹配，命中即拒；未命中再过 OPA Rego bundle。
3. 实现 GuardrailPolicy 绑定语义：求值所用策略包由 `guardrailPolicyRef` → `GuardrailPolicy.spec.opaPolicyBundle`（§5.2）解析，bundle 内容随仓库 `policies/` 目录版本演进。
4. 执行 fail-closed：装载失败、求值超时、引擎不可用、输入非法 → 一律 deny 并留痕。
5. 求值结果留痕：decision、违例清单、bundle 标识与内容哈希写入审计链，支撑 I6 回放。
6. 维护"人类/AI 同库"不变式：`spec.source`（agent|human，§5.1）只是求值上下文的一个维度，不构成差异化放行通道。

## 3. 技术选型与开源包

- 求值引擎为 OPA（Rego），版本以 `deploy/versions.md` 钉死为准；策略包形态为 bundle，存放于仓库 `policies/<bundle>/`（设计文档 §3.2 目录约定）。
- 本上下文（domain + service）不 import `github.com/open-policy-agent/*`；OPA SDK 只出现在 `internal/adapters/opa`。
- hardDeny 匹配为纯代码实现（无外部依赖）：对 `operations[]` 逐条匹配 `verbs × resources × namespaces` 规则。
- 内置 Rego 策略集范围以设计文档 §4.2.2 明示为准：禁 hostPath、禁特权容器、命名空间白名单、资源配额上下限；策略变更与代码变更同走 PR 评审。

## 4. 具体设计（不写代码）

### 4.1 PolicyEngine 端口语义

| 接口 | 职责 | 入参语义 | 出参语义 |
| --- | --- | --- | --- |
| Evaluate | 对一份待生效 manifest 执行策略求值 | `manifest`：CR 中待校验的操作清单（结构化操作，非自由文本）；`context`：目标命名空间/集群标识、来源（agent/human）、关联 CR 标识 | `decision`：allow 或 deny；`violations[]`：每条含 `policy_id`、`message`、`subject`（命中操作下标）；`engine_meta`：bundle 标识与内容哈希、求值耗时 |

### 4.2 hardDeny 与 Rego 策略的关系

- 求值顺序固定：hardDeny 先，OPA 后。hardDeny 命中即短路拒绝，不再消耗 OPA 求值。
- hardDeny 的数据载体是 `GuardrailPolicy.spec.hardDeny`（§5.2 四条示例：删 namespaces、secrets 明文读取、kube-system 等系统命名空间、clusterroles/clusterrolebindings 通配），执行侧是代码级强制（§5.2 注释"I1：代码级强制"）。
- "不可被策略关闭"的精确含义：任何 Rego 规则都不存在"豁免 hardDeny 命中"的通道——deny 由代码先行判定且终局；OPA 的 allow 只对"未被 hardDeny 命中"的操作有意义。
- hardDeny 清单本身的修改是 YAML 变更，走 PR 评审（§5.2 对 GuardrailPolicy 的定位即"可评审的 YAML"）。

### 4.3 求值流程

1. 接收 manifest + context；manifest 为空或结构非法 → 直接 deny（理由见 §5）。
2. hardDeny 逐条匹配 `operations[]`；命中 → deny，`violations` 记录 hardDeny 条目，短路返回。
3. 解析 `opaPolicyBundle` 并装载 bundle；装载失败 → deny（F14）。
4. 调 PolicyEngine 端口执行 Rego 求值，调用强制携带超时；超时 → deny（I10）。
5. 汇总输出 decision 与违例清单；结果与 bundle 哈希进入审计链（§6）。
6. deny 且命中 R3 特征的操作，按 §4.2.1 触发"留痕告警"，无审批入口。

### 4.4 策略包版本绑定与回放

- `GuardrailPolicy.opaPolicyBundle` 指向 `policies/` 下的 bundle 路径（§5.2 示例值 `policies/default`）；bundle 内容与仓库同版本演进，改动随 PR 评审。
- 每次求值记录 bundle 路径 + 内容哈希：I6 回放时以同一哈希复现同一求值结果，保证"同样输入同样结论"可验证。
- 策略包只随仓库发布节奏升级；运行期不从集群外拉取策略（与 risk 判定表同一确定性纪律）。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| hardDeny 命中 | 代码匹配 | deny + 留痕告警 | F1 处置口径即"拒绝+留痕"；无审批入口（§4.2.1 R3） |
| bundle 不存在/装载失败/编译错误 | 装载阶段校验 | deny + 告警（F14） | fail-closed：校验不了的 manifest 不放行（I10） |
| 求值超时 | 每次调用强制超时 | deny + 告警 | fail-closed；"超时降级为放行"是一票否决的 bug |
| 引擎进程不可用 | 端口调用失败/健康巡检 | deny + 告警（F14） | fail-closed：闸门不可用 = 写路径阻塞（I10） |
| manifest 为空/结构非法 | 入参校验 | deny | fail-closed：没有可校验对象的放行没有意义 |
| hardDeny 单条条目配置非法 | CRD schema 校验为主，运行期兜底跳过该条并告警 | 其余条目继续生效 | 局部降级：单条坏规则不拖垮整个清单，也绝不因单条损坏而整体放行或整体拒绝 |
| gatekeeper 与引擎分区 | F14 状态机巡检 | 在途 CR 由 reconcile 重入核对 | 恢复语义归 controller（I10），求值结果不落内存 |

整体原则：策略路径不存在任何"降级放行"分支。理由：安全组件的故障不能静默放行（I10），且 F1 的攻击模型正是"趁防线失效时溜过去"。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志（事件 `policy_evaluation`）：`cr_name`、`policy_ref`、`bundle_path`、`bundle_hash`、`hard_deny_hit`、`hard_deny_rule`、`opa_decision`、`violations_count`、`violation_ids`、`eval_duration_ms`、`fail_closed_reason`。
- 指标：
  - `aegis_policy_evaluation_total{decision, policy_ref}`：求值结果计数；
  - `aegis_policy_fail_closed_total{reason}`：fail-closed 触发计数（装载失败/超时/引擎不可用分列），F14 巡检重点；
  - `aegis_policy_hard_deny_hit_total{rule}`：硬拒命中计数，F1 度量；
  - `aegis_policy_eval_duration_seconds`（histogram）：求值耗时，超时阈值调参依据。
- Trace：span `gatekeeper.policy.evaluate`；子 span `policy.harddeny.check`、`policy.opa.eval`（后者落在适配器内）。
- 审计留痕点：decision、违例清单、bundle 路径与内容哈希随 CR 链路进入 DecisionRecord（§5.3 `changeRequests`/`auditRef`，I6）；hardDeny 命中按 §4.2.1 记"留痕告警"事件，纳入 `docs/threat-model.md` 统计。

## 7. 依赖方向与模块边界

- 谁调我：`cmd/gatekeeper` 的求值编排与 `internal/controller` 的 reconcile 编排（DryRunning 态推进），均经 service 层进入。
- 我调谁：仅调用本上下文 `ports` 声明的引擎出站接口，运行期由 `internal/adapters/opa` 注入实现；GuardrailPolicy 的 spec 由编排层翻译为领域值对象传入。
- 禁止依赖谁：`k8s.io/*`、`sigs.k8s.io/*`、`github.com/open-policy-agent/*`、MCP SDK、`internal/adapters/*`、其他上下文 domain——depguard 的 context-purity 规则强制（见 `.agents/skills/aegis-ddd-layout/SKILL.md`）。
- 边界红线：放行判断永不下沉到 mcp-servers（设计文档 §4.1 职责边界）；求值结果只能由编排层写回 CR status。

## 8. 测试策略与红队用例

- hardDeny 矩阵单测：§5.2 四条清单逐项命中（删 namespace、读 secret 明文、动 kube-system、改 clusterrole）全部 deny 且留痕——落地方案 T2.3 完成判据，即 M2 三连演示③的底层。
- fail-closed 三态单测：装载失败、求值超时、引擎不可用分别断言 deny + 告警，绝无放行分支。
- Rego 策略样例测试：hostPath、特权容器、命名空间白名单、资源配额上下限各至少一组通过/拒绝对。
- 同库测试：`spec.source=human` 与 `=agent` 的同一违规 manifest 得到同一 decision。
- 红队用例（M2 检查点）：越权（诱导 R3 操作变体）必须被 hardDeny 或 Rego 拦截；构造"绕过样例"manifest（字段别名、嵌套位置）验证违例清单的 `subject` 定位准确；绕过成功样本回写策略库与 `docs/threat-model.md`。
- 集成测试：适配器对真实 OPA（钉死版本）跑 bundle 装载与求值；覆盖率 ≥80%（L2 安全包）。
