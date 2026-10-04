# risk — 确定性风险分级器（risk-classifier）

> 所属层：L2 安全闸门（控制面） ｜ 里程碑：M2（落地方案 T2.2） ｜ 设计文档出处：§4.2.1/§4.2.2 ｜ 关联不变量：I1、I7 ｜ 关联失败模式：F1、F8

## 1. 设计初衷

风险分级是写操作进入闸门后的第一道判定（设计文档 §4.2.1）。设立本上下文的根本理由是 I1：放行/拒绝必须由确定性代码与策略决定，而不能由 LLM 输出或 prompt 决定——LLM 幻觉不可预测，prompt 护栏可被注入绕过。risk-classifier 以"资源类型×动词×作用域"三维查表把每个工具调用映射到 R0–R3 之一，并同步产出影响面估算 estBlastRadius，供配额核对（I7）、审批路由与逆操作要求使用。

本上下文是 F1（LLM 幻觉产出危险操作）的第一道拦截：无论 LLM 把"删除 namespace"描述得多么温和，查表结果恒为 R3，随后进入硬拒链路。它也是 F8（审批疲劳）的数据上游：分级口径直接决定多少操作被送入人审，过严或过松都会被审批指标反向校验。

## 2. 职责与任务清单

1. 接收分级输入：工具调用名、参数、目标对象清单（已由闸门编排层解析为结构化的 namespace/name 目标），以及调用方附带的 LLM 风险初判（如有）。
2. 维护并执行"资源类型×动词×作用域"三维判定表，输出 R0–R3 基础级别。
3. 执行合成规则：LLM 初判仅作参考，最终级别取查表结果与 LLM 初判中更高者（设计文档 §4.2.2）。
4. 按统一口径估算 estBlastRadius（pods/nodes/namespaces 三维度），供 GuardrailPolicy.blastRadiusQuota 的执行点（breaker，见 `internal/breaker/`，落地方案 T2.6）做硬上限核对。
5. 把终判级别与影响面写入 ChangeRequest 的 `operations[].riskLevel` 与 `operations[].estBlastRadius`（设计文档 §5.1）。
6. 记录"表未命中"事件，为判定表补全提供输入。
7. 消费审批疲劳指标（由 approval 上下文产出，见 `internal/approval/README.md`，F8），作为分级规则调优的输入；调优结论须经 eval 验证（M3，I8）。

## 3. 技术选型与开源包

- 判定表以 Go 数据结构内置于领域层，即"表即代码"：表的每次变更都是 PR diff，天然可评审（设计文档 §4.2.2"确定性代码查表"）。
- 领域层零外部依赖：不引入 k8s 客户端、OPA SDK、MCP SDK，不做网络调用；分级是纯函数式求值，可脱离集群单测。
- 工具元数据（`risk_hint` 等五字段，设计文档 §4.1）由 mcp-servers 在注册时声明，作为分级复核的辅助输入而非放行依据；元数据缺失的工具注册即失败（落地方案 T1.1）。
- 具体第三方库版本一律以 `deploy/versions.md` 钉死为准；本上下文的理想形态是仅依赖 Go 标准库。

## 4. 具体设计（不写代码）

### 4.1 R0–R3 定义与处置流对照（设计文档 §4.2.1）

| 级别 | 定义 | 处置流 |
| --- | --- | --- |
| R0 | 只读 | 限流内自主执行 |
| R1 | 可逆小写：scale ±1 步长内、单 Pod restart、单节点 cordon | dry-run → OPA → 逆操作生成+验证 → 自动执行 → 执行后验证 |
| R2 | 有损可控：滚动重启、大范围 scale、任何 GitOps 管资源 | dry-run → OPA → 人审（附诊断链+影响面报告）→ ArgoCD PR 或直写 → 验证 |
| R3 | 不可逆/提权：删除 namespace、改 RBAC/secret 明文读取、动 kube-system、hostPath 挂载 | 硬编码拒绝，策略层拦截，无审批入口，留痕告警 |

### 4.2 三维查表模型

- 资源类型维度：由工具与参数归一化得出（scale_deployment → deployments；restart_pod → pods；cordon_node → nodes）。
- 动词维度：读类与写类分开编码；同一资源在不同动词下级别不同。
- 作用域维度：单对象、单命名空间、集群级三档；涉及 kube-system 等系统命名空间的作用域直接命中 R3 条目。
- 每个表条目记录：三维键、级别、判定依据说明；条目只增不改语义，调整须 PR 评审（L2 包 AI 产出逐行人审，落地方案 R-2）。

### 4.3 合成规则：LLM 初判仅参考

- 调用方可附带 LLM 风险初判，本上下文将其视为不可信输入：只可能把终判调高，不可能把终判调低。
- 终判 = max（查表级别， LLM 初判级别）。两个方向都成立：LLM 判低、查表判高 → 取高（F1 防线）；LLM 判高、查表判低 → 同样取高（I4"由数据建议出的动作自动升级审批级别"的分级侧呼应）。
- LLM 初判缺失或不可解析时不阻塞分级：查表结果即终判——LLM 从来都是参考，不是必要条件（I1）。

### 4.4 estBlastRadius 估算口径

| 操作族 | pods | nodes | namespaces |
| --- | --- | --- | --- |
| 只读族（R0） | 0 | 0 | 0 |
| scale_deployment | 目标 replicas 数 | 0 | 目标命名空间数（§5.1 示例：shop → 1） |
| restart_pod | 1 | 0 | 1 |
| cordon_node | 0 | 1 | 0 |

- 多操作 CR 取各操作估算的并集计数；与工具元数据 `est_blast_radius` 声明值不一致时，以按上表计算的实测值为终值，并对差异记警告事件（元数据虚报是 I3 意义上的审查线索）。
- 估算所需的目标清单由编排层在分级前解析好传入（实际 list 查询经 ports 由 k8s adapter 完成）；解析不出清单的操作不进入自动路径，见 §5。

### 4.5 表数据来源：内置默认，策略只能加严

- 判定表唯一事实源是内置默认表（随代码版本演进，PR 可评审）。GuardrailPolicy 不注入分级表数据，本设计不为此新增 CRD 字段。
- "策略覆盖"的语义由设计文档已定义的两条通道承载，且只可能更严、不可能更松：
  1. `GuardrailPolicy.spec.hardDeny`（§5.2）：代码级强制的 R3 硬拒清单，命中即拒绝，先于 OPA；
  2. `GuardrailPolicy.spec.opaPolicyBundle` 指向的 Rego 策略包（§5.2）：可拒任何未通过校验的 manifest。
- 理由：允许策略把内置级别调低，等于让一纸 YAML 变更即可放行高危操作，违反 I1 的确定性闸门精神；加严方向无此风险。若未来确需更细粒度覆盖，先写 ADR（纪律 R-5）。

### 4.6 分级流程

1. 归一化输入：工具名+参数 →（资源类型，动词，作用域）三元组；目标清单 → pods/nodes/namespaces 计数。
2. 查内置表得基础级别；表未命中 → 按 R2 处置并记录未命中事件（见 §5）。
3. 合成 LLM 初判：终判 = max（基础级别， LLM 初判）。
4. 按 §4.4 口径计算 estBlastRadius。
5. 输出（级别， estBlastRadius， 表条目引用， 合成说明），交由编排层写入 CR 并继续闸门链路。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| 表未命中（未知资源/动词/作用域组合） | 查表返回空 | 按 R2 送审 + 记 `table_miss` 事件 + 指标 | fail-closed：未知组合不允许落低级别，符合 I10"安全组件故障不能静默放行"的同类精神 |
| LLM 初判缺失/不可解析 | 入参校验 | 不阻塞，查表结果即终判 | 非降级：LLM 本来就只是参考（I1） |
| 目标清单缺失或解析失败 | 入参校验 | 拒绝进入自动路径，该操作按 R2 送审或退回 | fail-closed：没有影响面数据的写操作无法做 I7 配额核对，不允许自动执行 |
| 元数据与实测影响面冲突 | §4.4 口径比对 | 以实测值为准，记警告事件 | 降级为警告：不改变放行结论，但留下 I3 审查线索 |
| 进程级故障（gatekeeper 崩溃、与 apiserver 分区） | CR 状态机巡检（F14） | 分级结果已落 CR，reconcile 重入续作 | 分级器自身无外部依赖、无内存态；其不可用 = 闸门不可用 = 写路径阻塞（I10） |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- 结构化日志（事件 `risk_classification`）：`cr_name`、`session_ref`、`tool`、`resource_kind`、`verb`、`scope`、`table_level`、`llm_level`、`final_level`、`est_pods`、`est_nodes`、`est_namespaces`、`table_entry_ref`、`decision_ms`、`policy_ref`、`table_miss`。
- 指标：
  - `aegis_risk_classification_total{final_level, tool, resource_kind}`：分级计数；
  - `aegis_risk_llm_escalation_total`：LLM 初判高于查表被采纳的次数（衡量初判通道价值）；
  - `aegis_risk_table_miss_total`：表未命中计数，驱动补表；
  - `aegis_risk_est_blast_radius_pods`（histogram）：影响面分布，配合 I7 配额观察。
- Trace：span `gatekeeper.risk.classify`；子 span `risk.table_lookup`、`risk.blast_radius_estimate`。
- 审计留痕点：终判级别与影响面写入 CR `operations[].riskLevel`/`estBlastRadius`（§5.1）；判定依据（表条目引用、合成说明）随工具调用序列进入 DecisionRecord（§5.3，I6）；F1 命中（判 R3）另触发"留痕告警"。

## 7. 依赖方向与模块边界

- 谁调我：`cmd/gatekeeper`（MCP 代理路径上的分级编排）与 `internal/controller` 的 reconcile 编排，均经本上下文 service 层进入；`internal/breaker` 消费本上下文产出的 estBlastRadius 做配额硬核对（跨上下文经 service 值对象协作，不 import 对方 domain）。
- 我调谁：谁都不调。本上下文无出站依赖；GuardrailPolicy 相关数据由编排层翻译为领域值对象传入。
- 禁止依赖谁：`k8s.io/*`、`sigs.k8s.io/*`、OPA SDK、MCP SDK、`internal/adapters/*`、其他上下文 domain——CI 以 depguard 强制（`.golangci.yml` 的 context-purity 规则，见 `.agents/skills/aegis-ddd-layout/SKILL.md`）。
- 边界红线：判定表禁止从 apiserver 或外部存储动态拉取——表只在代码评审中变更，保证确定性（I1）与纯单测可行性。

## 8. 测试策略与红队用例

- 表驱动单测：查表覆盖 MVP 全部 12–15 个工具（落地方案 T2.2 完成判据），每工具至少覆盖"正常参数/越界参数/系统命名空间"三组。
- 合成规则用例："LLM 判低、查表判高 → 取高"（T2.2 完成判据明示）；反向"LLM 判高、查表判低 → 取高"；LLM 缺失 → 查表即终判。
- estBlastRadius 口径用例：scale 到 4 副本 → pods=4/namespaces=1；cordon → nodes=1；只读 → 全 0；多操作 CR 并集计数。
- 表未命中用例：未知三元组必须落 R2 而非 R0/R1，且产生 `table_miss` 事件。
- 红队用例（M2 检查点）：诱导 R3 操作变体（参数注入、语义伪装）必须命中 R3 表条目；幻觉危险操作（F1，演示③的判级侧）恒判 R3；元数据 `est_blast_radius` 虚报被实测口径纠正并留警告。
- 覆盖率 ≥80%（L2 安全包，见 `.agents/skills/aegis-go-quality/SKILL.md`）。
