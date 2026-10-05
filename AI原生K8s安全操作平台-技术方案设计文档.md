# aegis：AI 原生 K8s 安全操作与调度平台 技术方案与设计文档

| 项 | 内容 |
| --- | --- |
| 项目代号 | `aegis`（暂定，宙斯盾——取"安全闸门"意象，随项目定盘可改） |
| 文档版本 | v1.5（§10 批次 #2 红队回写 R16–R29：审批身份、L2 部署级强制力、verifier 契约、留痕不可变机制、注入瘫痪面、多操作事务语义、策略防篡改等；v1.4：§3.2 目录结构落地为轻型 DDD 分层 + 跨平台约定；v1.3：闸门自身失效语义补齐 + 规模化 HA 方向留档，R14–R15） |
| 日期 | 2026-09-22 |
| 对标/参考 | AgentCube（volcano-sh，沙箱/调度形态参照）、kagent（CNCF，Agent 运行时形态参照）、kubectl-ai / K8sGPT（交互形态参照）、agent-sandbox（kubernetes-sigs，沙箱底座现成件） |
| 技术形态 | Go 控制面（CRD + controller + 策略闸门）+ Python/Go Agent 运行时（基于现成件二开）+ MCP 工具层 |
| 文档状态 | 待评审 → 评审通过后进入 M1 |

> **重要前提校正（先于一切设计）**：AgentCube（[volcano-sh/agentcube](https://github.com/volcano-sh/agentcube)）是 Volcano 子项目，目前处于 Proposal 与早期设计阶段，它解决的问题是"**K8s 如何服务好 AI Agent 工作负载**"（沙箱编排、预热池、会话粘性、空闲回收）——即 *AI on K8s*。本项目要解决的问题是镜像方向："**AI 如何安全稳定地操作 K8s**"——即 *AI operates K8s*。两者合起来才构成"AI 原生云资源管理调度平台"的完整图景。本文档以 *AI operates K8s* 为主线，沙箱与调度层（*AI on K8s*）直接复用 AgentCube/agent-sandbox 的设计与现成件，不自研。

---

## 1. 产品定位与范围

### 1.1 一句话定位

一个架在 K8s 之上的 **AI 操作安全层**：让 LLM Agent 能诊断、能修复、能参与调度决策，但每一次写操作都必须经过确定性的风险分级、策略校验、审批与可回滚封装——**AI 的变更和人类变更过同一套门禁，且比人类变更多一层熔断**。

### 1.2 目标（含可衡量的验收口径）

| # | 目标 | 验收口径 |
| --- | --- | --- |
| G1 | 只读诊断能力 | 自建故障场景库（≥20 场景）上根因诊断准确率 ≥80%，报告可回放 |
| G2 | 写操作全链路安全 | 任意写操作必经：风险分级 → dry-run → 策略校验 → （按级别）审批 → 执行 → 验证 → 可回滚；红队注入测试零越权 |
| G3 | 自治边界可控 | R1 级操作可全自主；R2 必须人审；R3 硬禁止；连续失败自动熔断降级只读 |
| G4 | 决策可审计 | 每次会话产出完整决策链记录（输入快照、工具调用、审批、执行结果），支持事后回放 |
| G5 | 调度参与不进热路径 | LLM 只产出调度策略/hint，由确定性调度器执行；有对照实验证明收益，否则砍 |

### 1.3 功能范围（MVP → 完整版分期）

| 分期 | 范围 | 对应里程碑 |
| --- | --- | --- |
| MVP | 只读诊断 copilot（窄工具集 + 诊断 Agent + 决策日志） | M1 |
| v1 | 安全闸门（风险分级、OPA 策略、审批流、逆操作回滚、熔断）+ 人审写操作 | M2 |
| v2 | eval harness（故障场景库 + 自动评分 + 回归门禁）+ R1 级有限自治 | M3 |
| v3（可选，决策门控制） | 调度 hint 闭环（LLM → SchedulingHint CRD → Volcano/调度器） | M4 |

### 1.4 明确的非目标（Non-Goals）

| # | 非目标 | 理由 |
| --- | --- | --- |
| N1 | **不自研沙箱运行时与沙箱编排** | [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)（SIG Apps，已发 v1.0.2，Sandbox/SandboxTemplate/SandboxClaim/SandboxWarmPool 一套 CRD 齐备，隔离委托 gVisor/Kata）直接可用；AgentCube 亦构建于其上。自研=重复造轮子且安全责任巨大 |
| N2 | **不做 LLM 逐 Pod 调度（不进调度热路径）** | 调度是毫秒级高频决策，LLM 是秒级低频推理，架构上不自洽。LLM 只产策略 |
| N3 | **不做商业化外壳**：多租户管理、Web 控制台、计费、HA 控制面、面向组织的大型 RBAC 体系 | 团队级工程量（见 §8 拆账）。注意边界——"端到端闭环的单点服务"正是内核 M1–M3，不做的是外壳而非闭环；Agent 自身的最小权限设计（I3/§4.4）不属于本豁免项，是安全叙事核心 |
| N4 | **不替代 GitOps** | ArgoCD/Flux 管的资源，Agent 只提 PR 不直写（见 ADR-001） |
| N5 | **不做通用聊天 Agent / 不接入 IM** | 护城河在闸门与 eval，不在交互皮 |
| N6 | **本期不实现多集群**（联邦调度、跨集群写操作） | HCS 场景需要，但先把单集群闸门做对；**预留接缝不实现**：CRD 带 `clusterRef` 字段、闸门配置层为目标集群注册表（§4.2/§5.1），避免多集群期重写契约；实现升格时另立姊妹篇 |

### 1.5 生态观察与定位选择（含冷静剂）

**生态事实（2026-09 核实，均为一手来源）：**

- **AgentCube**（volcano-sh）：Proposal 阶段，方向是 AI Agent 工作负载的 K8s 原生编排（CodeInterpreter CRD、预热池、会话绑定沙箱、PicoD 替代 SSH、JWT 安全链）。**与本项目互补而非竞品**。
- **kagent**（[kagent-dev/kagent](https://github.com/kagent-dev/kagent)，CNCF Sandbox）：Agent 声明为 CRD（system prompt + 工具白名单 + ModelConfig），MCP 工具抽象为 ToolServer CRD（自带 K8s/Istio/Helm/Argo/Prometheus/Grafana/Cilium 工具集），引擎基于 Google ADK，四组件（Controller/Backend/CLI/UI），OTel 追踪。**是本项目 Agent 运行时的首选二开底座**。
- **kubectl-ai**（Google）/ **K8sGPT**（CNCF）：前者是 CLI copilot（变更前确认模式），后者是只读扫描+解释，其自动修复 Mutation 至今 alpha、rollback 标注 TODO。**说明"安全写操作"是业界公认未解决的空白——这正是本项目切口**。
- **agent-sandbox**：见 N1，沙箱底座现成件。

**冷静剂（必须写下来的风险）：**

1. **"有 AI 了个人能不能做这种平台？"——诚实回答：全平台不行，收敛版可以。** AI 辅助把 CRD/controller/MCP server 的样板代码成本压低 3–5 倍，但威胁模型设计、eval 场景库构建、红队注入测试是判断力密集型工作，AI 提速有限，且**安全组件本身不允许 vibe code**（AI 写的闸门代码必须逐行人审，否则等于没有闸门）。工程量拆账见 §8。
2. **赛道拥挤的是"诊断 copilot"，空白的是"闸门与 eval"。** 若本项目最终做成"又一个 k8s agent"，简历叙事价值归零；差异化必须钉死在 L2 安全闸门与 L4 评估层。
3. **混合云/私域是真实差异化场景**：开源工具默认假设可调用公网 LLM API；私域模型接入、等保审计、离线环境恰恰无人服务好——与本职工作（HCS）场景咬合，但要注意 N6 的时间盒，避免与工作互相挤占。

---

## 2. 设计原则与架构不变量

每个功能开发完成后对照此表检查；违反任一不变量的 PR 不得合入。

| # | 不变量 | 出处/理由 |
| --- | --- | --- |
| I1 | **确定性闸门**：一切写操作的放行/拒绝由代码与策略（OPA/Rego）决定，不由 LLM 输出或 prompt 决定 | LLM 幻觉不可预测；prompt 护栏可被注入绕过 |
| I2 | **LLM 不进热路径**：调度、熔断、配额判断均为确定性逻辑 | 延迟与可用性不自洽（见 N2） |
| I3 | **工具即权限边界**：不提供"任意 kubectl"类万能工具；每个工具窄接口、强 schema 校验、显式标注只读/写与幂等性 | 万能工具=把 RBAC 形同虚设 |
| I4 | **读路径也是攻击面**：所有集群返回数据（日志/事件/注解/描述）进入上下文前标记为不可信数据并包裹隔离；由数据"建议"出的动作自动升级审批级别 | 日志 prompt injection 是租户可触发的真实攻击向量 |
| I5 | **一切写操作可回滚**：每个写操作强制生成逆操作并 dry-run 验证；无法生成逆操作的自动升级为 R2（人审） | 假安全感比没有回滚更危险 |
| I6 | **决策可回放**：输入快照、推理链、工具调用、审批记录、执行结果全量留痕 | 审计刚需；同样症状不同动作必须可解释 |
| I7 | **爆炸半径配额**：单次变更影响面（Pod 数/节点数/命名空间数）有硬上限，超限拒绝 | 幻觉的破坏力必须有天花板 |
| I8 | **eval 先行**：任何 prompt/模型/工具链变更，不过 eval 回归门禁不得发布 | 无 eval 的 Agent 迭代=裸奔 |
| I9 | **先归因后行动**：任何状态变化先经确定性归因（所有权图谱+事件时间线）；能归因到健康控制器正常行为的变化不进入诊断、不触达写路径；Agent 永不与控制器争夺其字段所有权 | K8s 默认即多控制器并发系统（HPA/VPA/autoscaler/descheduler/operator/GitOps），"被干扰"是默认环境而非异常；局部正确的组件可组合出全局振荡 |
| I10 | **失效姿态自明**：闸门不可用 = Agent 不可用 ≠ 集群不可用；写路径 fail-closed，读路径可降级只读直连；一切在途变更靠 CR 状态机 + finalizer 恢复，进程内存不持有不可恢复状态 | 安全组件的故障不能静默放行，也不能拖垮被管对象；平台是外挂控制环而非集群依赖 |

---

## 3. 总体架构

### 3.1 进程/模块拓扑

```javascript
                        ┌────────────────────────────────────────────┐
                        │  L4 评估与观测层                            │
                        │  eval-harness │ otel-collector │ replay    │
                        └──────────────▲─────────────────────────────┘
                                       │ 决策链 trace / eval 结果
┌──────────┐   会话    ┌───────────────┴─────────────┐
│  用户/CLI │ ◀──────▶ │  L3 Agent 运行时              │
└──────────┘          │  （kagent 二开：Agent CRD +     │
                      │   ADK 引擎 + 上下文工程）       │
                      └───────────────┬─────────────┘
                                      │ 工具调用（MCP）
                      ┌───────────────▼─────────────┐
                      │  L2 安全闸门 ★核心自研★       │
                      │  risk-classifier │ opa-eval  │
                      │  approval-svc    │ rollbacker│
                      │  circuit-breaker │ quota     │
                      │  log-sanitizer（注入防御）    │
                      │  authority-map（所有权图谱）  │
                      │  attributor（变更归因）       │
                      └───────────────┬─────────────┘
                                      │ 放行后的窄调用
                      ┌───────────────▼─────────────┐
                      │  L1 工具层（MCP server 群）   │
                      │  k8s-read │ k8s-write │ prom │
                      │  logs │ events │ gitops-pr   │
                      └───────────────┬─────────────┘
                                      │
        ┌─────────────────────────────┼──────────────────────────┐
        │                             │                          │
┌───────▼────────┐         ┌──────────▼───────┐        ┌─────────▼────────┐
│ L0-a 目标集群    │         │ L0-b 沙箱执行层    │        │ L0-c 调度层        │
│ K8s + workload │         │ agent-sandbox     │        │ Volcano/调度器     │
│                │         │ (Sandbox CRD +    │        │ ◀─ SchedulingHint  │
│                │         │  WarmPool + gVisor│        │     CRD（M4 可选）  │
│                │         │  /Kata RuntimeCls)│        │                  │
└────────────────┘         └──────────────────┘        └──────────────────┘
```

数据流要点：Agent 的一切工具调用**强制途经 L2 闸门**（MCP 代理模式，运行时无法绕过）；不可信代码/诊断脚本在 L0-b 沙箱内执行；调度产物是 CRD 而非直接调度动作。

**部署级强制（R17）**："运行时无法绕过"必须由网络拓扑背书而非口头约定——L1 工具 server 只接受来自 gatekeeper 的 mTLS 客户端连接（证书仅签发 gatekeeper），NetworkPolicy 拒绝一切其他来源；工具不得注册进 kagent 的直连 ToolServer 清单。任一失守即 I3/I4 与 F4/F5 空心化，列入 M2 红队检查项。

### 3.2 模块清单与目录结构

Monorepo（Go workspace + Python 子项目）：

```text
aegis/
├── cmd/                     # 装配入口：仅 main + flag + 依赖注入，禁止业务逻辑
│   ├── gatekeeper/          # L2 闸门主服务（Go）
│   ├── controller/          # CRD controller（ChangeRequest/GuardrailPolicy/SchedulingHint）
│   └── aegis-cli/           # 用户交互 CLI（审批、回放、eval 触发）
├── api/
│   └── v1alpha1/            # CRD Go 类型定义（K8s 契约层，kubebuilder 惯例）
├── internal/                # 限界上下文（轻型 DDD：每上下文 = domain + ports + service）
│   ├── risk/                # 风险分级（R0–R3 判定表）
│   ├── policy/              # 策略求值端口（OPA 为实现细节）
│   ├── approval/            # 审批流状态机
│   ├── rollback/            # 逆操作生成与验证规则
│   ├── breaker/             # 熔断器 + 配额
│   ├── authority/           # 所有权图谱 + 变更归因（控制器共存，I9）
│   ├── sanitize/            # 不可信数据消毒/隔离包裹
│   ├── audit/               # 决策链留痕与回放
│   ├── session/             # 会话与预算（§5.2 sessionBudget 执行点）
│   ├── adapters/            # 适配器：k8s(client-go) / opa / mcp / redis / langfuse
│   ├── controller/          # reconcile 层：CRD ↔ 应用服务（保持薄）
│   └── mcpserver/           # MCP server 共享框架（注册、元数据强校验、限流中间件）
├── mcp-servers/             # L1 工具层（每个子目录一个进程）
│   ├── k8s-read/            # 只读工具集（Go）
│   ├── k8s-write/           # 写工具集（仅接受 ChangeRequest 引用，见 §4.1）
│   ├── prom/ logs/ events/  # 观测数据工具集
│   ├── gitops/              # PR 生成工具（R2 通道）
│   └── runbook/             # runbook 检索（LanceDB，§4.1 工具清单）
├── policies/                # Rego 策略包（§5.2 opaPolicyBundle 指向）
├── agent/                   # L3 运行时（kagent fork/配置层 + 自研 prompt/上下文工程）
├── eval/                    # L4 eval harness（Python，独立模块不依赖 Go 内部包）
│   ├── scenarios/           # 故障场景库（chaos mesh manifest + 期望根因标注）
│   ├── graders/             # 评分器（诊断准确率/归因正确率/误修复率/MTTR）
│   └── regression/          # 回归门禁（CI 任务）
├── deploy/                  # helm chart + kind 本地环境 + versions.md（版本钉死表）
├── docs/
│   ├── adr/                 # 架构决策记录
│   └── threat-model.md      # 威胁模型（红队维护）
└── crds/                    # ChangeRequest / GuardrailPolicy / SchedulingHint / DecisionRecord
```

**分层规则（轻型 DDD，CI 用 depguard 强制，速查见 `.agents/skills/aegis-ddd-layout`）：**

1. 依赖方向单向：`cmd → controller/mcpserver/mcp-servers → internal/<上下文>/service → domain`；`internal/adapters/` 实现各上下文 `ports.go` 声明的出站接口。
2. 领域层纯净：各上下文领域文件禁止 import `k8s.io/*`、`sigs.k8s.io/*`、OPA/MCP 等框架包——分级规则、状态机、归因判定与 K8s 客户端解耦，可纯单测。
3. controller（reconcile）保持薄：读 CR → 调应用服务 → 写回 status，不内联业务规则。
4. 跨上下文不直接 import 对方 domain，经 service 协作；共享仅限值对象级小工具。
5. 默认 `internal/`；`pkg/` 仅当确有外部复用价值才升格，不留空目录。

**跨平台约定**：自动化一律 Makefile + `scripts/*.sh`（bash），CI（ubuntu-latest）为裁决环境；开发入口为 WSL2 Ubuntu（Windows 侧 Git Bash 仅兜底）。

### 3.3 技术选型（每项给选型理由与放弃项）

| 层 | 选型 | 理由 | 放弃项及原因 |
| --- | --- | --- | --- |
| 控制面语言 | **Go** | CRD/controller 生态唯一正解；OPA、client-go 原生 | Rust：controller-runtime 生态不成熟，练手价值≠主航道 |
| Agent 运行时 | **kagent 二开**（Agent CRD + ADK 引擎 + ToolServer 抽象） | 声明式 Agent、工具白名单、OTel 追踪全现成；CNCF 项目可写进叙事 | 自研薄运行时：练手价值高但与本项目主线（闸门）抢时间，列入 ADR-002 备选 |
| 工具协议 | **MCP** | 已成为 Agent↔工具事实标准；kagent/生态直接兼容 | 私有 RPC：生态自杀 |
| 策略引擎 | **OPA（Rego）** | K8s admission 事实标准，与 Kyverno 策略可复用 | 自研 DSL：无生态 |
| 沙箱底座 | **agent-sandbox v1.x**（Sandbox/SandboxWarmPool CRD + gVisor RuntimeClass） | SIG Apps 官方项目，AgentCube 同路线；隔离委托成熟运行时 | 自研沙箱/Firecracker 直管：安全责任与工程量均不可承受 |
| 沙箱内隔离 | gVisor（默认）/ Kata（强隔离场景） | RuntimeClass 切换即可，agent-sandbox 原生支持 | — |
| GitOps | **ArgoCD**（R2 通道） | 生态最大，drift 检测现成 | Flux：能力等价，择一即可 |
| 会话/状态存储 | **Redis/Valkey** | AgentCube 同路线；会话粘性、TTL 天然匹配 | etcd：不适合高频会话读写 |
| 观测 | **OpenTelemetry + Langfuse（自托管）** | OTel 标准留痕；Langfuse 做 LLM 调用链回放成本低 | 纯自建回放：重复造轮子 |
| Runbook/知识库 | 向量库（LanceDB 嵌入式）+ 结构化 runbook YAML | 嵌入式零运维，适合个人项目；知识检索给诊断 Agent 用 | 外部向量服务：MVP 阶段过度设计 |
| LLM 接入 | OpenAI 兼容 API 抽象层（公网模型 + 私域 vLLM 双通道） | HCS 场景咬合点：私域模型接入是差异化 | 绑定单一厂商 SDK |
| 调度器（M4） | **Volcano** | AgentCube 同属 Volcano 社区，batch/hint 机制现成；与本职工作生态一致 | 自研调度插件：超出个人范围 |
| CI 门禁 | GitHub Actions + kind | 个人项目零成本 | 自建 CI：无必要 |

---

## 4. 核心子系统设计

### 4.1 L1 工具层：窄接口 MCP 工具集

**职责边界**：工具层只做"参数校验 + 执行 + 结果结构化返回"，不含任何智能判断；一切放行判断在 L2。

**设计要点：**

1. **工具三分类，写工具不直接接受自由参数**：

| 类别 | 示例 | 约束 |
| --- | --- | --- |
| read | `get_pods(ns, label_selector, limit)` / `get_pod_logs(ns, name, tail_lines)` / `get_events(ns, since)` / `get_metrics(query, range)` | 强制分页与上限（单次 ≤500 行日志/≤200 对象）；结果过 sanitizer |
| write | `scale_deployment(cr_ref)` / `restart_pod(cr_ref)` / `cordon_node(cr_ref)` | **只接受 ChangeRequest ID**——必须先在 L2 建立 CR 并经闸门放行，工具层无法被 Agent 直接触达写能力 |
| gitops | `open_change_pr(repo, manifest_patch, description)` | R2 通道，产出 PR 链接而非集群变更 |

2. **每个工具带元数据标注**（注册进 ToolServer 时强制填写）：`risk_hint`（R0–R3 初判）、`idempotent`、`reversible`、`est_blast_radius`、`timeout_ms`。闸门据此做分级复核。
3. **读限流**：每会话 token 预算 + apiserver QPS 预算双上限，耗尽自动暂停并告知用户（对应失败模式 F4）。
4. **MVP 工具清单（12–15 个）**：pods/deployments/nodes/events/logs/metrics 只读族 8 个 + write 族 3 个（scale/restart/cordon）+ gitops 1 个 + runbook 检索 1 个。**工具宁缺毋滥，每加一个写工具过一遍红队。**

### 4.2 L2 安全闸门（核心自研，差异化所在）

**职责边界**：Agent 与集群之间唯一的写路径；读路径的消毒与限流也在这里。闸门配置层维护**目标集群注册表**（kubeconfig 列表），本期注册表恒为单集群——多集群接缝（§5.1 `clusterRef`）已预留，实现不升格。

**4.2.1 风险分级（I1）**

| 级别 | 定义 | 处置流 |
| --- | --- | --- |
| R0 | 只读 | 限流内自主执行 |
| R1 | 可逆小写：scale ±1 步长内、单 Pod restart、单节点 cordon | dry-run → OPA → 逆操作生成+验证 → 自动执行 → 执行后验证 |
| R2 | 有损可控：滚动重启、大范围 scale、任何 GitOps 管资源 | dry-run → OPA → **人审**（CLI/Webhook 推送，附诊断链+影响面报告）→ ArgoCD PR 或直写 → 验证 |
| R3 | 不可逆/提权：删除 namespace、改 RBAC/secret 明文读取、动 kube-system、hostPath 挂载 | **硬编码拒绝**，策略层拦截，无审批入口，留痕告警 |

**4.2.2 组件契约**

- `risk-classifier`：输入=工具调用+参数+目标对象清单；输出=级别+影响面估算。判定规则为确定性代码（资源类型×动词×作用域查表），**LLM 初判仅作参考，最终级别取两者中更高者**。
- `opa-eval`：每个 ChangeRequest 的 manifest 过 Rego 策略（禁 hostPath、禁特权容器、命名空间白名单、资源配额上下限）。AI 变更与人类变更共用同一策略库。
- `approval-svc`（R16）：审批状态机在服务端 controller（R1），且**审批身份必须绑定**：approve/deny 只能经 gatekeeper 的认证接口提交（审批凭证 + 来源校验），status 的 approval 子资源由 gatekeeper 独占写（RBAC 收敛 + admission 兜底），kubectl 直改 `status.approval` 无效；否则"状态机在服务端"防得住伪 UI、防不住有权限者的直改，单人场景下 R2 人审退化为"自己批自己"。
- `rollbacker`：逆操作生成（如 scale 的逆操作=原 replicas；restart 的逆操作=无，标记"效果不可回滚"→自动升 R2）。逆操作生成后**必须 dry-run 验证通过**，失败则升级级别。
- `circuit-breaker`：滑动窗口内 R1 执行失败率 >30% 或单位时间变更数超阈值 → 全局降级只读，推送告警，人工复位。
- `log-sanitizer`（I4）：集群返回数据统一包裹 `<untrusted_cluster_data>` 分隔符，附带注入检测（指令型关键词、角色扮演模式命中→告警+该数据衍生的任何动作自动升 R2）。
- `authority-map`（I9）：确定性维护**字段级所有权图谱**。数据来源：`metadata.managedFields`（SSA 字段管理者）、HPA/VPA/KEDA ScaledObject 的目标引用、operator CRD 管辖范围、ArgoCD Application、descheduler/autoscaler 配置。任意对象的关键字段（replicas、resources、调度约束）都能回答"归谁管"，零 LLM 参与。
- `attributor`（I9）：Agent 感知到的任何状态变化先过归因流水线：所有权图谱 + 事件时间线（K8s Events、audit log、HPA status、VPA recommendation、autoscaler 扩缩记录）+ 指标佐证。**能归因到健康控制器正常行为的变更=非事件**，只留痕不诊断；解释不了的残差才进入 LLM。归因先行同时大幅压缩 token 消耗。
- `verifier`（R18）：执行后验证基于**确定性探针判据库**（pod ready、错误率阈值、指标回落方向），判据与阈值可评审、可版本化，验证窗口遵守 §4.2.4-3；LLM 评估结论仅作参考且**不得触发回滚**——回滚触发的判定与写放行同属确定性域（I1 边界向回滚方向延伸）；验证失败默认升人工而非自动回滚，防"误验证→错误回滚"（F18）。

**4.2.3 关键取舍（ADR-001）**：R1 直写集群（低延迟、交互闭环）；R2 强制走 GitOps PR（评审与回滚天然自带）。代价是 R2 交互断裂，接受。

**4.2.4 与控制器共存的写规则（I9）**

1. **不抢字段**：目标字段有控制器所有权时直写一律拒绝；意图必须翻译为对所有者自身 API 的操作（要更多副本→改 HPA `minReplicas` 或暂停 HPA，而非改 Deployment `replicas`；要调整资源→改 VPA 策略而非 requests）。
2. **乐观并发**：写操作携带 resourceVersion / SSA field-manager 冲突检测，检出即放弃并重新归因，不蛮写重试。
3. **验证窗口 ≥ 稳定窗口**：verifier 观察窗口不得短于相关控制器的稳定窗口（如 HPA `stabilizationWindowSeconds`），否则把正常收敛误判为失败并触发多余动作（F12）。
4. **Agent 自身也是被管理的回路一员**：对每个目标对象设冷却期（类比 HPA 稳定窗口），冷却期内不重复动作；Agent 的动作会改变其他控制器的输入，必须按控制回路的一员设计稳定性，而非系统外的旁观者。
5. **涌现失配检测**：维护跨控制器振荡模式库（VPA+HPA 同指标互搏、VPA 抬 requests→autoscaler 加节点的成本螺旋、descheduler 与 PDB 拉锯等），结合指标级周期性波动检测主动巡检，输出"控制器组合失配"报告——各自正确、全局错误的系统行为是重点诊断品类。

**4.2.5 闸门自身失效与恢复语义（I10）**

1. **无状态化**：gatekeeper 进程不持有不可恢复状态；分级、审批、执行进度、回滚窗口全落 CRD status。多副本 standby，leader election 保证单写者。
2. **fail-closed 写路径**：闸门不可用、策略引擎超时、与 apiserver 分区 → R1+ 写操作一律阻塞/拒绝并告警，绝不"闸门挂了先放行"。读路径允许降级为只读直连，明确标注降级态。
3. **在途恢复**：controller 重启后 reconcile 重入 `Executing`/`Verifying` 态的 ChangeRequest：核对集群实际状态 → 已生效补验证，未生效按策略续作/回滚/中止；finalizer 保证清理不悬挂。
4. **幂等与去重（通信任务校验的内核版）**：调用方提交必须携带 `idempotencyKey`，CR 名由其哈希派生——网络重试折叠为同一 CR，不产生第二次执行；MCP 通道 mTLS + 签名信封 + 会话内序号，防重放防串话。
5. **规模化接缝**：状态机外置 + 幂等 + 单写者选举正是未来 worker 队列化解耦的前置——多 worker 时把 CR 状态机接入共享 workqueue 即可，无需推翻本节。队列化本身见附录 C 留档，本期不实现。

### 4.3 L3 Agent 运行时（kagent 二开）

- Agent 定义复用 kagent Agent CRD：`systemPrompt` + `tools`（ToolServer 引用，白名单制）+ `modelConfigRef`。本项目新增字段仅为 `guardrailPolicyRef`（绑定 §5 的 GuardrailPolicy）。
- **上下文工程**：集群状态以快照摘要注入（拓扑、异常对象、近期事件各设 token 上限），禁止裸灌全量 YAML；长会话走 autocompact（摘要+关键决策留存）。
- **双通道模型接入**：公网 API 与私域 vLLM 端点同一抽象，HCS 场景用私域通道（差异化叙事素材）。
- **多 Agent 分工（v2 起）**：diagnoser（只读）/ executor（写，强闸门）/ verifier（执行后验证）三个 Agent 角色分离，权限面最小化。MVP 先单 Agent。

### 4.4 L0-b 沙箱执行层（agent-sandbox 现成件集成）

- 用途：执行诊断脚本、网络探测、不可信代码（用户提交的排查脚本、LLM 生成的验证脚本）。
- 用法：`SandboxTemplate` 预置诊断镜像（kubectl/curl/tcpdump/只读 kubeconfig）；`SandboxWarmPool` 预热 2–3 个实例消冷启动；Agent 通过 Python SDK（`pip install k8s-agent-sandbox`）+ sandbox-router 调用。
- 隔离：默认 gVisor RuntimeClass；沙箱内 kubeconfig 仅 R0 只读权限——**沙箱能看到的最多等于 Agent 能查到的，写操作不经过沙箱**。
- 空闲回收：TTL 到期自动回收（AgentCube 同款思路，agent-sandbox 控制器已支持 scheduled deletion/pause/resume）。

### 4.5 L0-c 调度闭环（M4 可选，决策门控制）

- Agent 诊断结论 → `SchedulingHint` CRD（如"该 Deployment 对节点 X 的磁盘 IO 敏感，建议反亲和"）→ 确定性 controller 将 hint 翻译为 Volcano 优先级/亲和性 patch。
- **LLM 永远不直接逐 Pod 调度**（I2/N2）。
- 过门条件（写死）：对照实验中 hint 组相比基线在目标指标（如 P99 延迟/碎片率）改善 ≥10%，否则砍掉本模块，调度章节降级为"附录：未采纳方向"。

### 4.6 L4 评估与观测层

- **eval harness**（Python，与 `inferchaos` 资产复用）：
- 场景库：每个场景 = Chaos Mesh manifest + 期望根因标注 + 期望处置级别（应 R1/R2/拒绝）。必含"控制器干扰类"：HPA/VPA/autoscaler/descheduler 正常动作与故障注入并发，考察归因而非仅诊断。
- 核心指标：诊断准确率（根因命中）、**归因正确率**（控制器正常行为不误判为故障）、**误修复率**（"该不动时不动"场景的误动作数，对抗 F11）、MTTR。归因正确率与误修复率为一等指标，与诊断准确率并列。
- 回归门禁：prompt/模型/工具链任何变更触发全量场景回归，指标回退即阻断发布（I8）。
- **与 inferchaos 的关系**：不合仓。inferchaos 保持独立产品定位，本项目的 eval-harness 以库/流水线方式复用其故障注入能力——两个资产互相成就，各自叙事独立。
- **观测**：OTel trace 贯穿 会话→闸门→工具→集群；Langfuse 回放 LLM 调用链；每次会话落一份 `DecisionRecord`（§5）。

---

## 5. 数据契约与 Schema

第一版即包含中止/恢复/超时/回滚字段——与 §2 不变量 I5/I6/I7 保持一致，不留"以后再加"。

### 5.1 ChangeRequest CRD（闸门核心对象）

```yaml
apiVersion: aegis.dev/v1alpha1
kind: ChangeRequest
metadata:
  name: cr-20260917-0001
spec:
  sessionRef: sess-abc123            # 关联决策会话（审计回溯）
  clusterRef: cluster-a               # 目标集群（接缝预留：本期恒为默认集群，字段先行避免多集群期重写契约）
  idempotencyKey: intent-hash-9f3c2   # I10：幂等键，CR 名由其哈希派生，重试折叠去重
  intent: "缓解 payments 服务 OOM 导致的重启风暴"
  source: agent                      # agent | human
  operations:
    - tool: scale_deployment
      args: {namespace: shop, name: payments, replicas: 4}
      riskLevel: R1                  # risk-classifier 终判（非 LLM 自评）
      authorityCheck:                # I9：字段所有权核查（无 owner 才可直写）
        fieldOwner: ""               # 如 hpa/payments；非空则必须走 owner API 或拒绝
        decision: clear              # clear | via-owner-api | rejected
      estBlastRadius: {pods: 4, nodes: 0, namespaces: 1}
      dryRunResult: passed           # passed | failed | skipped
      inverseOperation:              # I5：必填，生成失败→自动升级
        tool: scale_deployment
        args: {namespace: shop, name: payments, replicas: 2}
      inverseVerified: true          # 逆操作 dry-run 验证结果
      timeout: 120s                  # 单操作执行超时
  approval:
    required: false                  # R2 强制 true
    approver: ""
    decidedAt: ""
    channel: cli                     # cli | webhook
  guardrailPolicyRef: default        # 绑定的护栏策略
status:
  state: Committed                   # Pending→DryRunning→AwaitingApproval→Executing
                                     # →Verifying→Committed | RolledBack | PartiallyRolledBack | Aborted | Rejected
  abort:                             # 中止语义：可随时人工/熔断器置位
    requested: false
    reason: ""
  executedOps: 1
  verification:                      # 执行后验证（verifier）
    checks: [{name: "pod-ready", passed: true}]
    healthy: true
  rollback:                          # I5 回滚语义
    deadline: "2026-09-17T15:00:00Z" # 超过此时间视为需人工介入而非自动回滚
    attempted: false
    result: ""
  auditRef: audit-20260917-0001      # DecisionRecord 引用
```

**多操作语义（R22）**：`operations[]` 默认整体顺序执行；任一失败→已执行操作按逆序自动回滚，状态落 `PartiallyRolledBack` 并升人工；组合爆炸半径按各操作 `estBlastRadius` 累加核算后再过 I7 配额（两个 6-Pod 的 R1 操作叠加=12 Pod，超限即拒）。

**对象级互斥（R27）**：同一目标对象存在在途 CR 时，后续冲突 CR 拒绝并后置排队——目标级冷却期管"重复动作"，管不了并发变更。

**approval 写入约束（R16）**：`status.approval` 仅由 gatekeeper 认证接口写入（§4.2.2 approval-svc），其余写入路径由 admission 拒绝。

### 5.2 GuardrailPolicy CRD（护栏策略，可评审的 YAML）

```yaml
apiVersion: aegis.dev/v1alpha1
kind: GuardrailPolicy
metadata:
  name: default
spec:
  clusterSelector: {}                 # 接缝预留：策略适用的集群范围，本期为空=默认集群
  hardDeny:                          # R3 硬禁止清单（I1：代码级强制）
    - {verbs: ["delete"], resources: ["namespaces"]}
    - {resources: ["secrets"], note: "禁止读取明文值"}
    - {namespaces: ["kube-system", "gatekeeper-system"]}
    - {verbs: ["*"], resources: ["clusterroles", "clusterrolebindings"]}
  blastRadiusQuota:                  # I7
    maxPodsPerChange: 10
    maxNodesPerChange: 1
    maxNamespacesPerChange: 1
  rateLimit:
    maxChangesPerHour: 20
    maxReadQPS: 5
  sessionBudget:                     # R4 整改：成本上限
    maxToolCalls: 50
    maxLLMTokens: 200000
  circuitBreaker:                    # 熔断
    failureRateThreshold: 0.3
    window: 30m
    action: downgrade_to_readonly
  opaPolicyBundle: policies/default  # Rego 策略包路径
```

**运行态防篡改（R23）**：GuardrailPolicy 是闸门配置中枢（配额/限流/预算/熔断阈值），必须由 GitOps 管理——gatekeeper 启动载入时校验策略 CR 的 ArgoCD 来源注解与 Git 提交哈希，运行期仅接受 GitOps 同步通道的变更，`kubectl edit` 放宽不生效；R3 hardDeny 为代码级清单，不受本机制影响，保持 I1 强制。

### 5.3 DecisionRecord（审计对象，落盘不可变存储）

```yaml
apiVersion: aegis.dev/v1alpha1
kind: DecisionRecord
metadata:
  name: audit-20260917-0001
spec:
  sessionRef: sess-abc123
  clusterSnapshotRef: snap-xxx       # 输入状态快照（对象清单哈希）
  llmTraceRef: langfuse-trace-yyy    # LLM 调用链引用
  toolCalls: [...]                   # 全量工具调用序列
  sanitizationEvents: [...]          # 注入命中/消毒记录
  changeRequests: [cr-20260917-0001]
  outcome: committed
  retentionDays: 365                 # 审计保留期
```

**不可变的机制背书（R19）**：CRD 落 etcd 本身可被 edit，"不可变"须三件套落地——①spec 一次性写入后由 admission 拒绝一切 spec 变更，controller 独占 status；②会话结束即封存（finalizer 标记 `sealed`），封存后任何字段修改拒绝；③每日导出对象存储（WORM）+ 内容哈希链，`aegis-cli replay` 前校验哈希。缺此机制，I6 的审计叙事不成立。

### 5.4 SchedulingHint CRD（M4 可选，此处仅留契约位）

`spec.targetRef` + `spec.hints[]`（affinity/priority/profile 建议）+ `spec.evidence`（诊断结论引用）+ `status.adopted`（controller 采纳与否）。**hint 是建议而非指令**，调度器可拒绝。

---

## 6. 失败模式表

| # | 模式 | 触发条件 | 检测手段 | 处置策略 |
| --- | --- | --- | --- | --- |
| F1 | LLM 幻觉产出危险操作 | 任意写提议 | risk-classifier + OPA + R3 硬清单 | 拒绝+留痕；不依赖 prompt 护栏（I1） |
| F2 | 日志/事件 prompt 注入 | 租户在日志写入指令型文本 | sanitizer 模式命中；数据衍生动作自动升 R2 | 数据隔离包裹+升级审批+告警（I4） |
| F3 | 与 HPA/ArgoCD 双写冲突 | 目标资源有 controller 所有权 | 执行前 ownerReference/GitOps 注解检查 | 拒绝直写，转 R2 PR 通道或拒绝 |
| F4 | 诊断循环失控 | Agent 反复调工具不收敛 | 会话预算（工具调用数/token 数）耗尽 | 暂停会话，输出当前结论与置信度 |
| F5 | 读操作打爆 apiserver | 大范围 list/全量日志 | QPS 限流+结果集硬上限 | 429+分页引导 |
| F6 | 逆操作失效（状态已漂移） | 回滚时目标对象已被第三方修改 | 回滚前 dry-run 复验 | 升级人工，附漂移 diff |
| F7 | 熔断后无人复位 | 熔断器触发但告警被忽略 | 熔断状态指标+持续告警 | 保持只读，CLI 显式复位，留痕 |
| F8 | 审批疲劳 | R2 过多导致人审流于形式 | 审批通过率/耗时监控 | 反哺分级规则调优；eval 验证 R1 扩边界的安全性 |
| F9 | 沙箱逃逸（低概率高影响） | 恶意诊断脚本 | gVisor/Kata 隔离+沙箱内仅只读凭证 | 逃逸影响封顶在只读面；网络策略禁出站直连 apiserver 写端口 |
| F10 | eval 集过拟合 | 自己出题自己答，指标虚高 | held-out 场景集+真实事故复盘入库 | 定期轮换；对外宣称指标时标注集来源 |
| F11 | 控制器正常行为被误判为故障 | HPA/autoscaler/descheduler/operator 正常动作引发状态变化 | attributor 归因覆盖率监控；eval"该不动时不动"场景 | 归因通过=非事件；归因失败的写动作默认升 R2 |
| F12 | 验证窗口短于控制器稳定窗口 | 执行后立即验证，控制器尚未收敛 | verifier 窗口配置校验（≥相关稳定窗口） | 判定为"收敛中"而非失败，延长观察 |
| F13 | 多控制器涌现失配（各自正确、全局振荡） | VPA+HPA 同指标互搏；VPA 抬 requests→autoscaler 加节点的成本螺旋；descheduler 与 PDB 拉锯 | 振荡模式库 + 指标级周期性波动检测 | 主动巡检品类，输出"控制器组合失配"报告（差异化亮点） |
| F14 | 闸门崩溃/与 apiserver 分区，在途变更悬挂 | 宕机、leader 切换、网络分区、webhook 超时 | CR 状态机巡检：Executing 超时未推进即告警 | fail-closed；重启后 reconcile 重入核对实际状态，续作/回滚/中止（§4.2.5） |
| F15 | 重试导致同一意图执行两次 | 调用方网络重试、消息重投 | idempotencyKey 折叠 + 信封序号检测 | CR 名哈希去重，重复提交返回同一 CR；序号乱序拒绝 |
| F16 | 注入诱导瘫痪（过度保守） | 注入文本持续命中 sanitizer 或推高 LLM 初判级别，所有动作被升 R2/R3 | 升 R2 比例突增指标 + `sanitizer_hit` 风暴检测 | 注入者的目标可以是"什么都不许做"：命中风暴期临时收紧 R1 自动执行并告警，绝不反向放宽 I4（R20） |
| F17 | 多操作 CR 部分失败 | `operations[]` 第 N 个失败，前 N-1 个已生效 | 状态机 `executedOps` 与 operations 长度不符 | 已执行操作逆序回滚→`PartiallyRolledBack`→升人工（R22，§5.1） |
| F18 | 误验证触发错误回滚 | 验证判据/窗口不当，把收敛中判为失败 | verifier 判据库评审 + 观察窗口配置校验（类比 F12） | 验证失败默认升人工、不自动回滚；LLM 评估结论不得触发回滚（R18，§4.2.2） |

---

## 7. 里程碑计划

每个里程碑必须"能演示"，跑不出演示不进下一阶段。

| 里程碑 | 内容 | 验收标准（可演示的现象） | 依赖 |
| --- | --- | --- | --- |
| M1 只读 copilot | k8s-read/prom/logs 工具集 + 诊断 Agent（kagent 二开）+ DecisionRecord 留痕 | kind 集群注入 Pod OOMKilled，Agent 自主诊断输出根因报告（命中标注）；`aegis-cli replay` 回放完整决策链 | kagent 部署、OTel |
| M2 安全闸门 MVP | gatekeeper + ChangeRequest/GuardrailPolicy CRD + R0–R3 分级 + OPA + 审批 CLI + 逆操作生成验证 + sanitizer | 三连演示：①Agent 提议 scale→dry-run→审批→执行→一键回滚；②构造恶意日志注入，Agent 不越权且告警；③Agent 提议删 namespace 被硬拒 | M1 |
| M3 eval harness + 有限自治 | 20 场景库（含 ≥5 个控制器干扰场景）+ 评分器 + CI 回归门禁 + R1 全自主 + 熔断器 | ①eval 报告（准确率/归因正确率/误修复率/MTTR）；②连续注入失败场景，熔断自动降级只读并告警；③改一处 prompt 导致指标回退，CI 阻断；④HPA 正常伸缩与故障并发时 Agent 零误修复 | M2、inferchaos 故障注入能力 |
| M4 调度闭环（可选） | SchedulingHint CRD + hint controller + Volcano 对接 + 对照实验 | 对照实验报告：hint 组目标指标改善 ≥10%；不达标则砍模块并记录附录 | M3、决策门通过 |

---

## 8. 工程量拆账与实施时间安排

**先给诚实总数，再给分期裁剪**（区分"工具内核"与"平台级产品"两个量级）。

### 8.1 模块拆账（个人 + AI 辅助编码，按业余每周 12–16 小时可支配时间计）

| 模块 | 内容 | 估时 | 备注 |
| --- | --- | --- | --- |
| L1 工具层 | 12–15 个窄工具 + ToolServer 注册 + 限流 | 50–70h | MCP server 样板 AI 可大量代劳，但写工具的元数据标注需人逐一定义 |
| L2 闸门 | CRD×2 + controller + risk-classifier + OPA 集成 + 审批流 + rollbacker + breaker + sanitizer + authority-map/attributor + 失效恢复语义 | 115–150h | **安全组件，AI 产出必须逐行人审，提速打折**（约 2x 而非 5x）；v1.2–v1.3 因归因层与失效语义上调 |
| L3 运行时二开 | kagent fork 接入 + 双通道模型抽象 + 上下文工程 | 40–60h | 大部分是集成本 |
| L0-b 沙箱集成 | agent-sandbox 部署 + 诊断镜像 + SDK 封装 | 15–25h | 现成件，主要是环境调试 |
| L4 eval | 20 场景制作 + 评分器 + CI 门禁 | 60–80h | 场景标注是体力活，AI 提速有限；与 inferchaos 资产复用可省 ~20h |
| 文档/威胁模型/红队 | 本类文档维护 + 注入测试设计 | 30–40h | 不可省 |
| **内核合计（M1–M3）** |  | **约 310–425h ≈ 5–7 个月业余** | v1.3 起 |

| 平台级产品（Non-Goal，仅给量级感知） | 量级 |
| --- | --- |
| 多租户 + Web 控制台 + 审计合规报表 + HA 控制面 + 多集群 + 私域模型产品化 | 团队 6–12 人月起，个人不进入 |

### 8.2 时间编排原则

- 黄金时段（周末整块）给编码；工作日晚上碎片时间给 eval 场景标注、文档、红队——标注类工作适合碎片。
- 每里程碑结束留 1 周缓冲做红队整改，不压缩。
- **时间盒**：与工作项目（i18n 扫描存量治理收尾）冲突期，本项目降速不暂停——保持每周最低 8h，防止冷启动成本。

### 8.3 收敛决策记录（回答"个人能不能做"）

- **能做的**＝本文档 M1–M3：工具层 + 闸门 + eval 的竖切，5–7 个月业余可达，且是差异化最强部分。
- **不能做的**＝M4 之后的平台化与多集群，以及任何"再造一个 kagent/AgentCube"的企图。
- AI 辅助改变的是**样板成本**，不改变**判断成本**；本项目的护城河（威胁模型、eval 集、红队）恰好全是判断密集区——这对个人反而是好消息：大厂团队在这层没有规模优势。

---

## 9. 风险表

| 风险 | 概率 | 影响 | 缓解 |
| --- | --- | --- | --- |
| 范围蔓延（想加 UI/多集群/联邦） | 高 | 项目烂尾 | Non-Goals 表 + M4 决策门；新方向先对照 N1–N6，突破则开姊妹篇 |
| kagent 上游大改版导致 fork 维护成本 | 中 | M3 延期 | 二开限定在配置层与新增 CRD，不改其 controller 内核；必要时降级为"仅复用其 ToolServer 生态+自研薄运行时"（ADR-002 备选） |
| eval 场景库质量不足→指标不可信 | 中 | 能力证明失效 | F10 缓解；从公开事故复盘（postmortem 社区资源）补充真实场景 |
| 与工作项目时间互挤 | 中 | 双线拖延 | §8.2 时间盒；降速不暂停 |
| 安全闸门出现逻辑漏洞（自研安全组件的天然风险） | 中 | 核心价值崩塌 | R1 级安全代码全人审；威胁模型文档随版本维护；M2 红队注入测试为准入门槛 |
| LLM API 成本（eval 全量回归烧钱） | 低 | 预算压力 | 回归用中小模型+会话预算上限；私域 vLLM 通道摊薄 |
| "又一个 k8s agent"叙事同质化 | 中 | 简历价值打折 | 叙事钉死"AI 变更安全闸门+eval"，README 首屏即威胁模型与红队结果 |

---

## 10. 红队评审记录

v1.0 定稿前首轮自审，发现项已全部回写正文。

| 编号 | 严重度 | 视角 | 发现 | 整改落点 |
| --- | --- | --- | --- | --- |
| R1 | P0 | 安全 | 审批流若只在 CLI 客户端实现，可被 `--yes` 类参数或自写客户端绕过 | 审批状态机移到服务端 controller，CLI 仅为视图；已回写 §4.2.1/§5.1 `status.state` 状态机 |
| R2 | P0 | 可靠 | 逆操作生成成功≠可执行，目标状态漂移会使回滚失败，造成假安全感 | 逆操作强制 dry-run 验证+回滚前复验漂移；无法验证自动升级 R2；已回写 §4.2.2、§5.1 `inverseVerified`、§6 F6 |
| R3 | P0 | 安全 | 初稿只防"写操作幻觉"，漏了读路径注入——日志即攻击面 | 新增不变量 I4、sanitizer 组件、失败模式 F2、M2 注入演示验收 |
| R4 | P1 | 成本 | 诊断循环可能无限调工具，token 与 apiserver 双重失控 | GuardrailPolicy 增加 `sessionBudget`；失败模式 F4 |
| R5 | P1 | 可靠 | 未处理与 HPA/GitOps 的双写冲突，Agent 与控制器会互相打架振荡 | 执行前所有权检查，冲突转 PR 通道；已回写 §4.2.3、§6 F3 |
| R6 | P1 | 工程 | 文档写下 I5"一切写可回滚"但初版 schema 无中止/超时字段——说一套做一套 | §5.1 补齐 `timeout`、`abort`、`rollback.deadline` 字段 |
| R7 | P1 | 工程 | M4 调度闭环是个人项目最易失控的扩展点 | 写死过门条件（对照实验改善 ≥10%，否则砍）；已回写 §4.5、§7 |
| R8 | P2 | 叙事 | "基于 kagent 二开"在简历上可能被读为"搭积木" | 差异化叙事锁定 L2+L4 自研；ADR-002 记录自研薄运行时备选，保留叙事升级空间；已回写 §9 末行 |
| R9 | P2 | 时间 | eval 场景标注工作量被低估（AI 提速有限的体力活） | §8.1 单独列账 60–80h，并安排碎片时间承接 |
| R10 | P2 | 安全 | 沙箱内若持有可写凭证，逃逸即破防 | 沙箱内 kubeconfig 硬限定 R0 只读+网络策略；已回写 §4.4、§6 F9 |
| R11 | P2 | 范围 | "不做多租户/重 RBAC"被误读为"无需任何权限设计"——单点服务的 Agent 自身 SA 最小权限与沙箱只读凭证恰是安全叙事核心 | 边界澄清回写 §1.4 N3/N6；权限设计维持 I3/§4.4 现状 |
| R12 | P1 | 可靠 | 集群是多控制器并发系统（HPA/VPA/autoscaler/descheduler/operator/GitOps 各自正确地改状态），初版把"被干扰"当异常而非常态环境，归因缺失必导致误修复与对抗振荡 | 新增 I9、authority-map/attributor 组件、§4.2.4 共存写规则、F11–F13、eval 归因/误修复指标；已回写 §2/§3/§4.2/§4.6/§5.1/§6/§7 |
| R13 | P2 | 稳定 | Agent 自身成为控制回路一员后，动作频率无约束会放大系统振荡（自我激励回路） | 目标级冷却期 + 验证窗口≥控制器稳定窗口；已回写 §4.2.4 |
| R14 | P1 | 可靠 | 初版有熔断器但无闸门自身进程级失效设计：崩溃时在途变更悬挂、恢复语义未定义、重试可能双执行 | 新增 I10 + §4.2.5 + idempotencyKey + F14/F15；已回写 §2/§4.2/§5.1/§6 |
| R15 | P2 | 范围 | 千级集群 HA/worker 解耦冲动撞 N3/N6，且无可验证环境——设计出来无法过"能演示"里程碑纪律，即纸面架构 | 留档附录 C 并写死立篇触发条件，不进主线；主线仅保留可验证的失效语义 |

**批次 #2（2026-10-05，M2 前外部评审）**：发现 14 项已全部回写正文；威胁模型新增 AS-11～AS-14 的同步登记列入 M2 前动作。

| 编号 | 严重度 | 视角 | 发现 | 整改落点 |
| --- | --- | --- | --- | --- |
| R16 | P0 | 安全 | 审批状态机移服务端后，"谁有资格审批"无认证设计：CLI→gatekeeper 通道无身份校验，有 RBAC 权限者可 kubectl 直改 approval/status；单人场景下 R2 退化为"自己批自己"，人审边际价值归零 | 审批身份绑定 + approval 子资源独占写（RBAC 收敛 + admission 兜底）；已回写 §4.2.2、§5.1；威胁模型 AS-02 扩展登记 |
| R17 | P0 | 安全 | "一切工具调用强制途经 L2"只有一句话、无部署级保证：工具 server 若被 kagent 直连发现，I3/I4 与 F4/F5 全部空心化 | 部署级强制：mTLS 客户端证书仅签 gatekeeper + NetworkPolicy 拒绝其他来源 + 禁止直连注册；已回写 §3.1；列 M2 红队检查项 |
| R18 | P0 | 可靠 | verifier 是语义黑洞：谁验证、验证失败处置、LLM 能否触发回滚均未定义；M2 验收①依赖它，且 LLM 触发回滚会突破 I1 边界 | verifier 契约：确定性探针判据库，LLM 评估不得触发回滚，验证失败默认升人工；已回写 §4.2.2、§6 F18 |
| R19 | P1 | 安全 | §5.3 写"落盘不可变存储"但 CRD+etcd 可任意 edit，I6 审计完整性无机制背书 | 不可变三件套（spec 封存 + admission 拒改、finalizer `sealed`、WORM 导出 + 哈希链）；已回写 §5.3 |
| R20 | P1 | 安全 | 注入分析只覆盖"诱导越权"一面，漏"诱导瘫痪"：持续注入可让所有动作升 R2/R3，Agent 被远程钉死（可用性 DoS） | 新增 F16 + 升级率异常指标 + 命中风暴期收紧 R1；威胁模型新增 AS-12 |
| R21 | P1 | 安全 | R1 全自动与 sanitizer"模式库不可能完备"自相矛盾：检测残差 = 无审批直写 | 对冲规则：本轮会话存在注入嫌疑（LLM 初判升高或命中风暴）时禁用 R1 自动执行；M2 红队注入用例覆盖 |
| R22 | P1 | 可靠 | 多操作 CR 无事务语义：部分失败如何回滚、组合爆炸半径是否累加核算均未定义 | 多操作语义：逆序回滚 + `PartiallyRolledBack` + 累加核算；已回写 §5.1、§6 F17 |
| R23 | P1 | 安全 | GuardrailPolicy 运行态无防篡改：`kubectl edit` 可放宽配额/熔断/预算，PR 人审只管 git 流程 | 策略 CR 走 GitOps + 启动哈希校验 + 运行期仅接受同步通道变更；已回写 §5.2；威胁模型新增 AS-13 |
| R24 | P1 | 安全 | I4 只管注入不管脱敏：日志中的连接串/token/PII 原样进 LLM 上下文，走公网通道即数据出域——HCS 私域叙事的核心短板 | read 工具族增加敏感模式扫描与分级脱敏，私域通道讲成"数据不出域"；威胁模型新增 AS-14，M1 先做扫描 + 告警 |
| R25 | P1 | 工程 | I8 eval 门禁的裁决者未定义：grader 若用 LLM-as-judge，门禁可信度建立在未评估的评判者上 | grader 判定机制显式化：确定性匹配优先 + LLM 评判仅辅助 + 人工抽检比例写入口径文档（T3.3 交付物） |
| R26 | P2 | 工程 | `source: agent\|human` 定位模糊：同规则则字段无用，差异化则"同一套门禁"叙事有未声明例外 | 明确字段用途=审计维度，人类变更同规则同闸门；人类通道审批语义写清，双人审批规则留 M3 后评估 |
| R27 | P2 | 可靠 | 目标级冷却期管不了并发 CR：两个对同一对象的不同操作可同时进闸门 | 对象级互斥：在途 CR 目标冲突即拒绝后置；已回写 §5.1 |
| R28 | P2 | 可靠 | `rollback.deadline` 超时"转人工"无闭环：人工介入入口、未介入悬挂巡检、漂移 diff 呈现均未定义 | deadline 到期自动转 `Aborted` + 悬挂告警 + 漂移 diff 落 DecisionRecord；接入 F14 巡检口径 |
| R29 | P2 | 工程 | 16GB 内存底线无分配表：kind + Prometheus + ArgoCD + kagent + Langfuse + Chaos Mesh + gVisor 争内存，笔记本 OOM 是 Phase 0 可验证风险 | `deploy/versions.md` 增资源预算表；W2 出口自检实机压一遍（T0.2 收尾项） |

---

## 附录

### 附录 A：ADR 记录

#### ADR-001：R1 直写 vs R2 GitOps PR 的分界

- 背景：Agent 写操作走"直写集群"还是"GitOps PR"是形态级分歧。直写延迟低、交互闭环；PR 自带评审与回滚但交互断裂。
- 决策：R1 直写（逆操作+配额兜底），R2 强制 PR（ArgoCD 通道），R3 硬拒。
- 代价与接受：R2 用户体验断裂；换来审计与回滚的确定性，接受。
- 反方意见留档：若未来 M4 后做多集群，R1 也可能需统一转 PR 通道——届时重审本 ADR。

#### ADR-002：Agent 运行时 kagent 二开 vs 自研薄运行时

- 决策：M1–M3 基于 kagent（配置层二开，不改其 controller 内核），复用 Agent/ToolServer CRD 与 ADK 引擎。
- 备选触发条件：kagent 上游架构剧变或 fork 维护成本 >20h/月 → 降级为仅复用其 ToolServer/MCP 生态，自研薄运行时（约 +40h，叙事反而增强）。
- 本决策随每个里程碑复评。

### 附录 B：参考项目清单（2026-09-17 核实）

| 项目 | 角色 | 一手来源 |
| --- | --- | --- |
| volcano-sh/agentcube | 沙箱/调度形态参照（AI on K8s） | github.com/volcano-sh/agentcube（Proposal 阶段） |
| kagent-dev/kagent | Agent 运行时底座（二开对象） | github.com/kagent-dev/kagent |
| kubernetes-sigs/agent-sandbox | 沙箱底座（N1 现成件） | github.com/kubernetes-sigs/agent-sandbox |
| GoogleCloudPlatform/kubectl-ai | CLI copilot 交互参照 | github.com/GoogleCloudPlatform/kubectl-ai |
| k8sgpt-ai/k8sgpt | 只读诊断参照（自动修复仍 alpha） | github.com/k8sgpt-ai/k8sgpt |
| kubernetes-sigs/agent-sandbox 生态：gVisor/Kata | 沙箱隔离运行时 | 经 RuntimeClass 接入 |
| volcano-sh/volcano | M4 调度层现成件 | github.com/volcano-sh/volcano |
| inferchaos（自有项目） | eval 故障注入能力复用 | 独立仓库，不合仓 |

### 附录 C：未采纳方向留档

- LLM 逐 Pod 调度（违反 I2，架构不自洽）；
- 自研沙箱运行时（N1，安全责任与工程量不可承受）；
- 多集群联邦（N6，本期仅预留 clusterRef/注册表接缝不实现；待单集群闸门验证后另立姊妹篇）。
- **规模化 HA 与 worker 队列化解耦**（千级节点、多集群控制面 HA、跨 worker 任务校验总线）：本期不立篇。理由两条：①撞 N3/N6 非目标；②无千节点可验证环境，设计无法过"能演示"里程碑纪律，写出来即纸面架构。留档预备瓶颈清单（立篇时逐一回答）：informer watch fanout 与缓存分片、etcd 对象规模与分页、APIServer 优先级与公平性（APF）限流、admission webhook 超时与 failurePolicy、跨集群时钟漂移、CRD 版本灰度不一致、大对象撞 etcd 上限。**立篇触发条件（写死）**：获得 ≥3 集群或压测环境可验证，且 M3 已验收通过。本期主线仅实现可验证的失效语义（§4.2.5），其机制即未来队列化的前置接缝。