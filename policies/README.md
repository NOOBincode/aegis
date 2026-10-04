# policies — Rego 策略包总述（策略即代码资产）

> 所属层：L2 安全闸门（策略资产）｜ 里程碑：M2（落地方案 T2.3）｜ 设计文档出处：§4.2.2 opa-eval、§5.2 `opaPolicyBundle` ｜ 关联不变量：I1、I10 ｜ 关联失败模式：F1

## 1. 设计初衷

- **AI 变更与人类变更共用同一策略库**（设计文档 §4.2.2）：同一套 Rego 规则既约束 Agent 经 ChangeRequest 发起的变更，也约束人类发起的等价变更，杜绝"AI 专属宽松通道"。
- **策略即代码，可评审、可 diff、可回滚**：GuardrailPolicy CRD 的 `hardDeny` 与 `blastRadiusQuota` 是代码级强制，表达力有限；Rego 层承接可演进的护栏表达（禁 hostPath、禁特权容器、命名空间白名单、资源配额上下限，§4.2.2），两者互补（分工见 `policies/default/README.md` §4）。
- **确定性**：策略求值不含 LLM、不含外部调用，同输入恒同输出——I1（确定性闸门）在策略侧的支撑。

## 2. 职责与任务清单

1. 以 bundle（目录）为单位组织 Rego 策略；默认包 `policies/default` 由 GuardrailPolicy `default` 经 `spec.opaPolicyBundle` 绑定（§5.2 示例值为 `policies/default`）。
2. 为每条策略编写正向（应放行）与反向（应拒绝）测试用例，随策略同目录、同 PR 提交。
3. 维护策略的输入文档结构契约（opa-eval 喂入的 input 字段语义，见 §4），与 `internal/policy` 的求值端口保持一致。
4. 策略变更走单独 PR + 人审（落地方案 W12 安排"Rego 策略编写与人审"；策略属 L2 闸门资产，适用 R-2 人审纪律）。
5. 策略不得包含网络调用、时钟依赖、随机性——求值必须是纯函数，保证可测试与可回放的确定性。

## 3. 技术选型与开源包

- 策略引擎：OPA（Rego）。理由：K8s admission 事实标准，社区策略可复用；放弃项：自研 DSL（无生态，设计文档 §3.3）。
- 策略测试：OPA 内置测试运行器（`opa test` 语义），CI 对每个 bundle 强制执行（aegis-ci 规范的 lint/test 门禁组成部分）。
- OPA 加载与求值的代码侧实现在 `internal/adapters/opa` 适配层；OPA 及相关组件版本以 `deploy/versions.md` 钉死为准。
- 本目录是纯声明资产：不含 Go/Python 代码，不含构建脚本。

## 4. 具体设计（不写代码）

**bundle 组织**：每个 bundle 一个一级子目录（如 `policies/default/`）；目录内每条策略一个 Rego 文件 + 同名测试文件。bundle 名即 `opaPolicyBundle` 字段的指向值。新增 bundle = 新增目录 + GuardrailPolicy 绑定，不改代码。

**输入文档结构**（opa-eval 对每个 ChangeRequest 组装，字段语义契约）：

| 字段 | 语义 |
| --- | --- |
| `input.request.tool` | 目标工具名（如 `scale_deployment`），对应 §5.1 `spec.operations[].tool` |
| `input.request.args` | 工具参数（命名空间/对象名/副本数等），即 `spec.operations[].args` |
| `input.request.target` | 目标对象解析结果：kind、namespace、name |
| `input.request.est_blast_radius` | 影响面估算（pods/nodes/namespaces），来自 risk-classifier 输出（§4.2.2） |
| `input.request.manifest` | 变更后对象形态的规范化表示，供对象级规则（如 hostPath 检测）求值 |
| `input.context` | 只读上下文：绑定的 GuardrailPolicy 摘录（白名单、配额数值），由 opa-eval 注入 |

**输出结构**（opa-eval 消费的判定结果语义）：`allow`（布尔，缺省视为 false——无策略显式放行即拒绝，fail-closed）；`violations`（列表，每项含策略名、规则名、违例字段路径、人类可读消息、严重度）。

**违例消息格式**：固定五元组——策略标识（bundle/文件）、规则标识、字段路径（如 `spec.template.spec.volumes[0].hostPath`）、解释文本、建议处置（拒绝或升级 R2）。消息面向审批人与审计，不面向 LLM（I1：LLM 不参与放行判断）。

**策略测试纪律**：每条策略至少一正一反两个用例；正例覆盖边界内输入，反例覆盖边界外与字段缺失；测试与策略同 PR；CI 跑 `opa test` 全 bundle，失败即红。红队演练中构造的绕过样本（历史注入、越权变体）沉淀为永久用例，防策略后续修改引入绕过。

**默认立场**：所有策略 deny 默认；`allow` 必须显式得出。策略层永不输出"建议放行但需人工决定"的中间态——升级审批是 risk-classifier 与 rollbacker 的职责（§4.2.2），策略只做确定性判定。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| 策略编译/装载失败（语法错误、包名冲突） | opa-eval 启动装载期校验 + CI `opa test` | 该 bundle 所有求值拒绝；gatekeeper 告警（F14 变体：闸门组件失效） | fail-closed（I10）：策略层不可用则写路径整体阻塞，绝不降级为"跳过策略" |
| 求值超时 | opa-eval 对每次求值设超时（T2.3 明确 fail-closed） | 该 ChangeRequest 拒绝，记 `policy.eval_timeout` | fail-closed（I10）；超时降级为放行是一票否决的 bug（aegis-go-quality 错误处理节原文） |
| 输入文档缺字段/类型不符 | 求值前 schema 校验 | 拒绝并记 `policy.input_invalid`，指向具体字段 | fail-closed：字段缺失时放行等于策略被静默架空 |
| 部分规则求值异常 | OPA 求值错误面 | 整个请求判拒绝（任一规则异常即整体不通过） | fail-closed：避免"坏规则被好规则平均掉" |
| 策略测试未过 | CI `opa test` | 合并阻断 | fail-closed（流程级门禁） |

无降级路径：策略层没有"宽松模式"。读路径不经过策略层，策略失效不影响只读诊断（I10：读路径可降级只读直连）。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

**结构化日志字段**（gatekeeper 进程，JSON）：`policy.bundle`（bundle 路径）、`policy.tool`、`policy.cr_ref`、`policy.decision`（allow/deny/error）、`policy.violation_count`、`policy.rules_fired`（触发的规则标识列表）、`policy.eval_duration_ms`、`policy.error_class`（load_fail/eval_timeout/input_invalid）。

**指标**：`aegis_policy_eval_total{result="allow|deny|error"}`；`aegis_policy_eval_duration_seconds`（histogram）；`aegis_policy_bundle_load_failure`（gauge，0/1，供 F14 巡检告警消费）。

**Trace**：span `gatekeeper.policy.evaluate`，属性含 bundle、decision、violation_count；作为 ChangeRequest 处理 span 的子级（span 树约定见 `internal/policy/README.md`、`internal/controller/README.md`）。

**审计留痕点**：每次求值的输入文档哈希、输出判定、违例五元组全文落 ChangeRequest status 与 DecisionRecord（§5.3，I6）；审批视图可见违例详情（approval-svc，§4.2.1 R2 人审附件）。策略本身的变更历史由 git + PR 评审记录承载——策略即代码，审计即 code review 历史。

## 7. 依赖方向与模块边界

- **谁调我**：`internal/adapters/opa`（OPA 适配器，装载并求值本目录 bundle，实现 `internal/policy` 上下文的求值端口，DDD 铁律 1 的适配器方向）。
- **我调谁**：无。本目录是被装载的声明资产，不调用任何仓库代码。
- **禁止依赖谁**：禁止 import 任何 Go/Python 包；禁止网络/时钟/环境变量依赖（求值纯函数化）；禁止引用 `internal/` 内符号。bundle 与 GuardrailPolicy 的绑定关系不在本目录自描述——绑定权在 `spec.opaPolicyBundle`。
- **与五铁律的关系**：本目录不在 Go 依赖图内，但它是 `internal/policy` 端口在"数据侧"的等价实现：策略不感知 k8s client（铁律 2 领域纯净的策略侧镜像），客户端细节全部由 opa-eval 适配。

## 8. 测试策略与红队用例

- 每策略正反用例（§4），CI `opa test` 强制；新策略无测试不合入。
- 红队用例方向：
  1. **等价语义变体**：同一违例意图的不同 manifest 写法（hostPath 经 volume 直挂与经组合卷间接引入、initContainers 中的特权容器），验证策略匹配的是语义而非字符串形态（对应红队检查点 #2"越权：诱导 R3 操作变体"的策略侧）。
  2. **字段缺失与空值**：缺 `namespace`、`args` 为空，验证 fail-closed（§5）。
  3. **历史注入样本入库**：M2 红队的注入构造沉淀为永久反例用例，随策略演进持续回归。
- 人审：策略变更 PR 必须人工评审（R-2）；CI 绿灯不豁免人审——策略是 L2 闸门的一部分。
