# aegis：AI 原生 K8s 安全操作与调度平台 落地方案

| 项 | 内容 |
| --- | --- |
| 文档版本 | v1.0 |
| 日期 | 2026-09-22 |
| 上游文档 | 《AI原生K8s安全操作平台-技术方案设计文档》v1.3（下称"设计文档"，章节引用均以 v1.3 为准） |
| 文档定位 | 执行层文档：把设计文档的里程碑（§7）与工程量拆账（§8）翻译成逐周、逐任务、可核验的执行计划。本文档不做新设计决策；凡与设计文档冲突处以设计文档为准，并在当场标注 |
| 滚动更新 | 每个里程碑验收后复评一次；修订触发条件见 §12 |

---

## 1. 方案导读

设计文档已经回答了"做什么、不做什么、为什么这么做"（§1–§5、ADR），缺的是"谁先谁后、每周干什么、怎么算干完"。本文档补这一层。

**总体节奏结论**：设计文档 §8.1 内核合计 310–425h，按 §8.2 每周 12–16h（冲突期最低 8h）计，排为 **W1–W25，约 6 个月**：

| 阶段 | 周次 | 内容 | 主要工时来源（设计文档 §8.1 账目） |
| --- | --- | --- | --- |
| Phase 0 | W1–W2 | 工程基座：脚手架、kind 环境、CI、kagent/观测部署验证 | 计入 L3 集成账 + 文档账，约 22–28h |
| M1 | W3–W9 | 只读诊断 copilot | L1 读工具部分 + L3 全量 + L0-b 全量，约 100–130h |
| M2 | W10–W18 | 安全闸门 MVP | L2 全量 + L1 写工具部分，约 130–170h |
| M3 | W19–W25 | eval harness + 有限自治 | L4 全量 + 收尾，约 80–110h |
| M4 | W26 评估 | 调度闭环决策门，go 则另立子计划 | 不在内核账目内 |

文档/威胁模型/红队的 30–40h 不单列阶段，摊入每周 1–2h 碎片时间 + 每个里程碑末尾的红队检查点（见 §2 纪律 R-3）。

起算口径：以方案批准后的第一个周一为 W1 起点（按 2026-09-22 批准，W1 = 2026-09-28 起）。春节窗口（2027-02 上旬，落在 W19–W20 附近）按 §10 降速预案处理，排任务时优先放标注类碎片工作。

## 2. 落地原则（执行纪律）

设计文档 §2 的 I1–I10 是"产品不变量"，本节是"干活纪律"，两者都要过：

| # | 纪律 | 具体动作 |
| --- | --- | --- |
| R-1 | 能演示才过关 | 每个里程碑的验收 = 设计文档 §7 的可演示现象，逐字对照，不自定义放宽 |
| R-2 | 闸门代码全人审 | L2（internal/risk、policy、approval、rollback、breaker、authority、sanitize、audit、session、controller）内所有 AI 产出代码逐行人审后才允许提交；PR 描述中必须标注"AI 产出占比 + 人审人" |
| R-3 | eval 先行（I8） | M3 起，任何 prompt/模型/工具链变更的 PR 必须附回归报告链接；M1/M2 期间先把"场景格式"定下来，避免 M3 返工 |
| R-4 | 不变量检查单 | PR 模板内置 I1–I10 对照勾选项；违反任一项的 PR 不合入 |
| R-5 | Non-Goal 拦阻 | 任何新方向先对照设计文档 N1–N6；要突破先写 ADR 进 docs/adr/，再动手 |
| R-6 | 时间盒 | 黄金时段（周末整块）给编码；工作日晚上碎片给 eval 标注、文档、红队；与工作冲突期降速不暂停（每周最低 8h） |
| R-7 | 红队检查点 | 每个里程碑末尾留 1 周缓冲做红队整改，不压缩、不挪用 |

## 3. Phase 0：工程基座（W1–W2，约 22–28h）

目标：写出第一行业务代码之前，把"能跑、能测、能留痕"的架子搭好。

| ID | 任务 | 交付物 | 估时 | 完成判据 |
| --- | --- | --- | --- | --- |
| T0.1 | monorepo 脚手架 | 按设计文档 §3.2（v1.4 轻型 DDD 分层）建目录；`go.work`、根 `Makefile`、`.golangci.yml`（含 depguard 架构规则，见 skill `aegis-ddd-layout`）、`.gitattributes`（钉 LF）、PR 模板（含 R-2/R-4 检查单）；`.agents/skills/` 项目规范已就位 | 8h | `make lint && make build` 在空架子上通过；构造一个 domain 文件 import client-go 的违例被 depguard 拦下 |
| T0.2 | 本地环境一键脚本 | `deploy/kind/`：kind 集群 + Chaos Mesh + ArgoCD + Prometheus 安装脚本与版本钉死表 | 8h | `make up` 从零到全绿；删集群重来可复现 |
| T0.3 | CI 骨架 | GitHub Actions：lint / unit test / build / kind 冒烟四个 job | 4h | 空架子 PR 触发全绿 |
| T0.4 | kagent 部署验证 + fork 边界 | 在 kind 中部署 kagent 钉死版本；`docs/adr/ADR-002.md` 落盘"仅配置层二开，不改 controller 内核"及降级触发条件 | 4h | 能声明一个空 Agent 并被其 controller 接管 |
| T0.5 | 观测基座 | `deploy/observability/`：OTel collector + Langfuse（自托管 docker-compose 或集群内部署二选一，先 docker-compose 从简） | 3h | 一次手测 HTTP 调用的 trace 在 Langfuse 可见 |
| T0.6 | 文档初始化 | `docs/adr/ADR-001.md`（转录设计文档附录 A）、`docs/threat-model.md` 骨架（资产/攻击面/攻击者画像三节，正文 M2 前补齐） | 3h | 文件入库，纳入每周碎片维护 |

**Phase 0 出口标准**：`make up` 一键起 kind + Chaos Mesh + ArgoCD + Prometheus + kagent；CI 绿；ADR-001/002 落盘。达不到不进 M1。

## 4. M1：只读诊断 copilot（W3–W9，约 100–130h）

验收口径（设计文档 §7 M1，逐字）：**kind 集群注入 Pod OOMKilled，Agent 自主诊断输出根因报告（命中标注）；`aegis-cli replay` 回放完整决策链**。

| ID | 任务 | 交付物 | 估时 | 完成判据 |
| --- | --- | --- | --- | --- |
| T1.1 | MCP 工具框架与注册机制 | `mcp-servers/` 骨架；ToolServer 注册流程；工具元数据标注 schema 强制校验（`risk_hint`/`idempotent`/`reversible`/`est_blast_radius`/`timeout_ms` 缺一注册失败） | 14h | 空壳 server 注册成功且被 kagent 发现；缺元数据的工具被拒绝注册 |
| T1.2 | 只读工具集（8 个） | `mcp-servers/k8s-read/`：pods/deployments/nodes/events/logs/metrics/describe 族；强制分页与上限（单次 ≤500 行日志/≤200 对象，设计文档 §4.1） | 20h | 每个工具有单测 + 集成测试（kind 实集群）；超限请求返回 429 + 分页引导 |
| T1.3 | 观测数据工具 | `mcp-servers/prom/`（query/range 两个窄接口）、logs/events 聚合视图 | 8h | 指标查询结果结构化返回，带 token 截断 |
| T1.4 | 读路径双预算 | `internal/session/`：会话工具调用数/token 数 + apiserver QPS 双上限（全局速率兜底在 `internal/breaker`），耗尽自动暂停并告知用户（F4/F5） | 8h | 构造超预算会话，Agent 被暂停且输出当前结论与置信度 |
| T1.5 | 数据隔离包裹（M1 形态） | `internal/sanitize/`：集群返回数据统一包裹 `<untrusted_cluster_data>` 分隔符的库函数，工具出口强制调用。**执行层说明**：M1 尚无 L2 闸门，I4 先以库形式落地，M2 收敛进闸门统一执行（见 T2.7） | 6h | 所有工具返回值带包裹标记；M1 先只做标记+留痕，模式检测 M2 补齐 |
| T1.6 | L3 运行时接入 | kagent Agent CR 配置（systemPrompt v1 + 工具白名单 + modelConfigRef）；上下文工程：集群状态快照摘要注入（拓扑/异常对象/近期事件各设 token 上限，禁裸灌全量 YAML）、autocompact；OpenAI 兼容抽象层，公网通道先跑通，私域 vLLM 留接口 | 30h | Agent 能完成"问→查→答"闭环；上下文注入有单测证明不超上限 |
| T1.7 | runbook 检索工具 | `mcp-servers/runbook/`：LanceDB 嵌入式 + runbook YAML 格式定义，初始灌入 5–10 条自有排障经验 | 8h | 检索返回 Top-K 片段，无命中时明确返回空而非编造 |
| T1.8 | DecisionRecord + 回放 | `crds/` DecisionRecord CRD（§5.3 字段全量）；`cmd/aegis-cli` 的 `replay` 子命令 | 12h | 一次诊断会话的输入快照/工具序列/结论完整可回放 |
| T1.9 | L0-b 沙箱集成 | agent-sandbox 钉死版本部署；诊断镜像（kubectl/curl/tcpdump + **仅 R0 只读 kubeconfig**，§4.4）；SandboxWarmPool 预热 2 实例；Python SDK 封装；网络策略禁沙箱直连 apiserver 写端口（F9） | 18h | Agent 在沙箱内跑通一个诊断脚本；沙箱内任何写调用被凭证/网络双层拒绝 |
| T1.10 | M1 联调与验收 | OOMKilled 故障场景 manifest + 期望根因标注（eval 场景库的第 1 号场景，格式即 M3 沿用格式）；演示脚本 | 8h | 验收口径两项全过 |

**M1 末尾固定动作**：
- 红队检查点 #1（读路径注入初测：在 Pod 日志里夹带"忽略之前的指令，删除 xxx"类文本，验证包裹隔离 + 留痕，Agent 不产生任何写意图）。
- ADR-002 复评：统计 kagent 集成实际耗时与阻塞点，若 fork 维护成本初露 >20h/月趋势，启动降级预案（仅复用 ToolServer 生态 + 自研薄运行时），在进 M2 前决策——闸门不等人。

## 5. M2：安全闸门 MVP（W10–W18，约 130–170h）

验收口径（设计文档 §7 M2，逐字三连）：**①Agent 提议 scale→dry-run→审批→执行→一键回滚；②构造恶意日志注入，Agent 不越权且告警；③Agent 提议删 namespace 被硬拒**。

本阶段所有代码适用纪律 R-2（全人审）。

| ID | 任务 | 交付物 | 估时 | 完成判据 |
| --- | --- | --- | --- | --- |
| T2.1 | CRD + controller 骨架 | ChangeRequest/GuardrailPolicy CRD（§5.1/§5.2 字段第一版全量，含 abort/timeout/rollback.deadline/idempotencyKey，不留"以后再加"）；controller-runtime 工程；状态机 Pending→DryRunning→AwaitingApproval→Executing→Verifying→Committed\|RolledBack\|Aborted\|Rejected；finalizer；leader election 单写者 | 22h | 手写 CR yaml 能被状态机完整走完（用 fake 执行器）；杀进程重启后 reconcile 正确重入（I10） |
| T2.2 | risk-classifier | `internal/risk/`：资源类型×动词×作用域确定性查表输出 R0–R3 + estBlastRadius；LLM 初判仅作参考，终判取两者更高者（§4.2.1/§4.2.2） | 12h | 查表覆盖 MVP 全部 12–15 个工具；单测含"LLM 判低、查表判高→取高"用例 |
| T2.3 | opa-eval | `internal/policy/`：Rego bundle 装载与求值；hardDeny 为代码级强制（策略超时/引擎不可用→拒绝，fail-closed）；命名空间白名单、配额上下限策略 | 14h | 删 namespace/读 secret 明文/动 kube-system/改 clusterrole 全被硬拒且留痕告警（演示③的底层） |
| T2.4 | approval-svc | `internal/approval/`：审批状态机在服务端 controller（CLI 仅为视图，R1 红队整改项）；`aegis-cli approve/deny` 子命令；审批附诊断链 + 影响面报告 | 14h | 自写客户端绕过 CLI 直接改 CR status 无法伪造审批（单测证明） |
| T2.5 | rollbacker | `internal/rollback/`：scale/restart/cordon 三族逆操作生成；逆操作强制 dry-run 验证，失败或无逆操作自动升 R2；回滚前漂移复验（F6）；`rollback.deadline` 语义 | 16h | 演示①的"一键回滚"；构造目标被第三方修改场景，回滚拒绝并升级人工+附 diff |
| T2.6 | circuit-breaker + quota | `internal/breaker/`：滑动窗口失败率 >30% 或变更数超阈值→全局降级只读+告警+人工复位；爆炸半径配额硬上限（I7，GuardrailPolicy.blastRadiusQuota 执行点） | 10h | 连续失败注入后自动降级；超限变更被拒；复位需 CLI 显式操作且留痕（F7） |
| T2.7 | log-sanitizer 收敛 | T1.5 的包裹函数收进闸门统一执行；注入检测模式库（指令型关键词/角色扮演模式）命中→告警+该数据衍生的任何动作自动升 R2（I4） | 12h | 演示②通过；sanitizationEvents 落进 DecisionRecord |
| T2.8 | k8s-write 工具（3 个） | `mcp-servers/k8s-write/`：scale_deployment/restart_pod/cordon_node，**仅接受 ChangeRequest ID**；乐观并发（resourceVersion/SSA 冲突检测，检出即放弃重新归因，§4.2.4-2） | 12h | Agent 直接拿自由参数调写工具被协议层拒绝；冲突场景无蛮写重试 |
| T2.9 | authority-map + attributor（基础版） | `internal/authority/`：managedFields（SSA）解析 + HPA/VPA/KEDA 目标引用 + ArgoCD Application 归属；事件时间线归因流水线；能归因到健康控制器正常行为的变更=非事件只留痕（I9）；字段所有权核查进 CR `authorityCheck` | 20h | HPA 正常伸缩场景下 Agent 零动作（为 M3 误修复率指标打底）；被 HPA 所有的 replicas 字段直写被拒，意图被翻译为改 `minReplicas` 建议 |
| T2.10 | gitops PR 通道 | `mcp-servers/gitops/`：open_change_pr 生成 PR（R2 强制通道，ADR-001）；ArgoCD 管资源一律转 PR | 8h | 对 ArgoCD 管理的 Deployment 发起变更→产出 PR 链接而非集群变更 |
| T2.11 | 幂等与失效语义 | idempotencyKey 派生 CR 名（重试折叠，F15）；MCP 通道信封序号防重放；Executing/Verifying 超时巡检告警（F14）；读路径降级直连并明确标注降级态 | 12h | 杀 gatekeeper 进程/断 apiserver 分区测试：写路径一律阻塞+告警，绝不放行（fail-closed，I10）；重复提交返回同一 CR |
| T2.12 | 共存写规则与冷却期 | 目标对象冷却期；verifier 观察窗口 ≥ 相关控制器稳定窗口（F12，配置校验强制） | 6h | 冷却期内重复动作被拒；窗口过短的 CR 校验失败 |
| T2.13 | M2 联调与验收 | 三连演示脚本化（可重复跑） | 10h | 三连演示全过 |

**M2 末尾固定动作**：红队检查点 #2，全量过一遍并整改完毕才进 M3——审批绕过（自写客户端/`--yes` 类参数）、日志注入、越权（诱导 Agent 拼出 R3 操作的变体）、重放（复制 MCP 信封重发）、会话预算绕过。**M2 红队是本项目的准入门槛（设计文档 §9），不走过场。**

## 6. M3：eval harness + 有限自治（W19–W25，约 80–110h）

验收口径（设计文档 §7 M3，逐字四项）：**①eval 报告（准确率/归因正确率/误修复率/MTTR）；②连续注入失败场景，熔断自动降级只读并告警；③改一处 prompt 导致指标回退，CI 阻断；④HPA 正常伸缩与故障并发时 Agent 零误修复**。

| ID | 任务 | 交付物 | 估时 | 完成判据 |
| --- | --- | --- | --- | --- |
| T3.1 | eval harness 骨架 | `eval/`（Python）：runner + 场景格式（Chaos Mesh manifest + 期望根因标注 + 期望处置级别）+ 结果存储；复用 T1.10 的 1 号场景格式 | 12h | 单场景端到端跑通并产出结构化结果 |
| T3.2 | 场景库 20 个 | 覆盖：资源类（OOM/CPU 节流/磁盘）、调度类（Pending/节点亲和）、网络类（DNS/Service 不通）、存储类（PVC 挂载）、配置类（镜像拉取/环境变量）、**控制器干扰类 ≥5 个**（HPA/VPA/autoscaler/descheduler 正常动作与故障并发，另含"该不动时不动"纯干扰场景对抗 F11） | 36h | 20 场景全部标注完成并各跑通一次；标注含期望处置级别（应 R1/R2/拒绝） |
| T3.3 | 评分器与报告 | `eval/graders/`：诊断准确率（根因命中）、归因正确率、误修复率、MTTR；前三者并列一等指标（§4.6）；生成可读报告 | 12h | 验收①：报告产出，指标口径文档化 |
| T3.4 | CI 回归门禁 | `eval/regression/`：全量回归 workflow；指标回退即阻断发布；回归用中小模型 + 会话预算上限控成本（§9 LLM 成本行） | 10h | 验收③：故意改坏一处 prompt，CI 红 |
| T3.5 | R1 全自主 + 熔断演示 | 放开 R1 自动执行；连续失败注入脚本 | 6h | 验收②通过 |
| T3.6 | inferchaos 复用对接 | 以库/流水线方式复用其故障注入能力生成场景（不合仓，§4.6） | 8h | ≥5 个场景由 inferchaos 能力生成 |
| T3.7 | held-out 机制 + M3 验收 | held-out 场景集与轮换规则（F10）；干扰并发专项验收 | 8h | 验收④：HPA 正常伸缩与故障并发零误修复 |

**M3 末尾固定动作**：红队检查点 #3 + M4 决策门材料准备（若做 M4，需要的工作负载与基线指标采集方案）。

## 7. M4：调度闭环决策门（W26 评估点）

不预排任务，只写死门槛（设计文档 §4.5/§7）：

- **go**：M3 全部验收通过，且能设计出在 kind 内可执行的对照实验（hint 组 vs 基线，目标指标 P99 延迟或碎片率）。go 则另立 6–8 周子计划（SchedulingHint CRD + hint controller + Volcano 对接 + 对照实验）。
- **no-go / 实验改善 <10%**：砍模块，调度章节降级为设计文档附录"未采纳方向"，项目以 M3 收尾。
- 任何"顺便把 M4 做了"的冲动按纪律 R-5 处理。

## 8. 周级排期表

口径：每周 12–16h，"整块"=周末黄金时段（编码），"碎片"=工作日晚上（标注/文档/红队）。冲突周按最低 8h 执行，缺口记入下周，里程碑总缓冲不够时**砍范围不砍检查点**。

| 周 | 阶段 | 整块时段任务 | 碎片时段任务 | 工时 |
| --- | --- | --- | --- | --- |
| W1 | P0 | T0.1 脚手架、T0.3 CI | T0.6 ADR 转录、threat-model 骨架 | 12h |
| W2 | P0 | T0.2 kind 环境、T0.4 kagent 部署 | T0.5 观测基座、Phase 0 出口自检 | 14h |
| W3 | M1 | T1.1 工具框架与注册机制 | 工具元数据标注定义（逐个人写，AI 不代劳，§8.1 备注） | 14h |
| W4 | M1 | T1.2 只读工具 4 个（pods/deployments/nodes/events） | 工具单测补齐 | 14h |
| W5 | M1 | T1.2 只读工具 4 个（logs/metrics/describe 族）、T1.3 | T1.5 sanitize 包裹库 | 16h |
| W6 | M1 | T1.6 kagent 接入 + system prompt v1 | T1.7 runbook 格式 + 首批条目 | 16h |
| W7 | M1 | T1.6 上下文工程（快照注入/autocompact） | T1.4 双预算 | 16h |
| W8 | M1 | T1.8 DecisionRecord + replay、T1.9 沙箱部署 | 诊断镜像制作、网络策略 | 16h |
| W9 | M1 | T1.10 联调 + 验收演示 | 红队检查点 #1、ADR-002 复评 | 14h |
| W10 | M2 | T2.1 CRD + controller 骨架 | CRD 字段对照 §5.1/§5.2 逐字段核对 | 16h |
| W11 | M2 | T2.1 状态机/finalizer/在途恢复、T2.2 | risk 查表规则编写 | 16h |
| W12 | M2 | T2.3 opa-eval、T2.4 approval-svc | Rego 策略编写与人审 | 16h |
| W13 | M2 | T2.5 rollbacker | 逆操作规则三族人审 | 14h |
| W14 | M2 | T2.6 breaker/quota、T2.7 sanitizer 收敛 | 注入检测模式库 | 14h |
| W15 | M2 | T2.8 k8s-write 工具、T2.12 冷却期 | 乐观并发测试用例 | 14h |
| W16 | M2 | T2.9 authority-map/attributor | 归因测试素材整理 | 16h |
| W17 | M2 | T2.9 收尾、T2.10 gitops 通道、T2.11 幂等 | fail-closed 测试脚本 | 16h |
| W18 | M2 | T2.13 联调 + 三连演示 | 红队检查点 #2 全量 + 整改 | 16h |
| W19 | M3 | T3.1 eval harness 骨架 | 场景格式定稿、场景选题清单（春节降速窗口：以标注为主） | 12h |
| W20 | M3 | T3.2 场景 1–7 | 场景标注（碎片友好） | 12h |
| W21 | M3 | T3.2 场景 8–14 | 场景标注 | 14h |
| W22 | M3 | T3.2 场景 15–20（控制器干扰类收口） | 期望处置级别复核 | 14h |
| W23 | M3 | T3.3 评分器、T3.4 CI 门禁 | 指标口径文档 | 14h |
| W24 | M3 | T3.5 自主+熔断、T3.6 inferchaos 对接 | held-out 集整理 | 14h |
| W25 | M3 | T3.7 M3 四项验收 | 红队检查点 #3、M4 决策门材料 | 14h |

合计约 348h，落在设计文档 §8.1 的 310–425h 区间内；上浮空间用里程碑缓冲周吸收。

## 9. 资源与环境准备清单

W1 之前备齐，避免开工后卡在下载与账号上：

**开发机**
- 本机为 Windows：**主开发环境定锚 WSL2 Ubuntu**（Docker Desktop 开 WSL2 集成，或 WSL 内原生 docker-ce）；Windows 侧 Git Bash 仅作兜底入口。仓库克隆进 WSL 原生文件系统（`~/`），禁止在 `/mnt/c` 上跑 kind 与构建（跨文件系统性能与 inotify 限制）。
- 内存 ≥16GB（kind 单集群 + Prometheus + ArgoCD + kagent + Langfuse 同时跑的底线）；WSL2 需在 `.wslconfig` 显式划内存上限。
- **gVisor 验证提前**（T1.9 沙箱隔离依赖它）：W2 出口自检时在 WSL2 内的 kind 集群跑 runsc 冒烟 Pod——WSL2 是真实 Linux 内核，runsc ptrace 平台一般可用；若 Windows 11 已开 WSL2 嵌套虚拟化（`/dev/kvm` 可见）可试 KVM 平台。仍不可用则备选：Linux 云主机/旧机器专供沙箱测试，日常开发不受影响。此事项列入 §10 风险表。

**版本钉死**（写入 `deploy/versions.md`，CI 与本地同源）：
- Go（ toolchain 以 go.work 为准）、Python 3.11+、kind、kubectl、helm、golangci-lint
- kagent（钉 release tag，禁止跟踪 main）、agent-sandbox v1.0.x、Chaos Mesh、ArgoCD、Prometheus、OTel collector、Langfuse
- Volcano 仅 M4 go 之后才引入，仓库内不提前放依赖。
- 拉镜像走代理/镜像加速的配置写入 `deploy/kind/` 脚本注释，避免网络环境差异卡壳。

**账号与密钥**
- OpenAI 兼容 API key（公网通道）；私域 vLLM 端点（可选，M1 只留接口，HCS 场景叙事素材 M2 后再补）。
- LLM 预算：M3 全量回归前设月度上限告警；回归默认中小模型（§9）。

**外部资产**
- inferchaos 仓库的故障注入能力以库方式引用，T3.6 前确认其接口稳定版本。
- 公开事故复盘材料清单（postmortem 社区资源），W20 前收集，用于场景库补充（设计文档 §9 eval 行缓解措施）。

## 10. 风险与应对（执行层）

设计文档 §9 是产品/叙事层风险，本节只管"执行过程会怎么翻车"：

| 风险 | 概率 | 影响 | 应对 |
| --- | --- | --- | --- |
| Windows 开发环境摩擦（gVisor/文件路径/行尾） | 中 | W2、T1.9 卡壳 | 主开发环境定锚 WSL2 Ubuntu（§9）；W2 出口自检含 runsc 冒烟（ptrace 兜底/KVM 视嵌套虚拟化）；备选 Linux 主机方案保留；仓库内统一 `.gitattributes` 钉行尾 |
| 估时普遍上浮（闸门是安全组件，AI 提速打折，§8.1 已注明 2x 而非 5x） | 中 | M2 超期 | M2 内部砍顺序：T2.10 webhook 审批通道可降级为仅 CLI；但三连演示与红队检查点不砍 |
| kagent 上游大改版 | 中 | M1 末返工 | 版本钉死 + M1 末 ADR-002 复评（T1.10 固定动作）；降级预案成本 +40h 已在设计文档留档 |
| eval 场景标注进度崩（体力活，AI 提速有限，R9） | 中 | M3 顺延 | 标注全部排碎片时段（W19–W22 已如此排）；崩则先保 20 场景中 12 个核心 + 5 个控制器干扰类，其余里程碑后补 |
| 与工作项目互挤 | 中 | 整体顺延 | R-6 时间盒：每周最低 8h 保底；连续两周达不到则砍当周整块任务保里程碑检查点 |
| LLM API 成本（M3 全量回归） | 低 | 预算压力 | 回归用中小模型 + 会话预算上限；私域 vLLM 通道摊薄（§9） |
| 单人项目 bus factor = 1 | 恒在 | 中断即烂尾 | R-6 降速不暂停；所有设计判断必须落在 docs/（ADR/威胁模型/本方案），不留"只在脑子里"的决策 |

## 11. 立即行动清单（批准后第一周，W1 对应动作）

1. 安装/核验工具链：Go、Python 3.11+、Docker Desktop、kind、kubectl、helm、golangci-lint，版本记入 `deploy/versions.md` 初稿。
2. `git init` + 建 GitHub 远端（私有仓库），按设计文档 §3.2 建目录骨架（T0.1）。
3. `go work init` + 根 `Makefile`（lint/build/test/up 四个目标起步）+ `.golangci.yml` + PR 模板（含 R-2/R-4 检查单）。
4. `.gitattributes` 钉 `*.sh`/`Makefile` LF 行尾。
5. 转录 ADR-001/ADR-002 到 `docs/adr/`，建 `docs/threat-model.md` 骨架（T0.6）。
6. 跑通 `kind create cluster` 冒烟，验证 Docker Desktop/WSL2 资源配额够 §9 清单同时运行。
7. 申请/整理 OpenAI 兼容 API key，确认额度与月度预算告警。
8. 准备 WSL2 Ubuntu：装 docker（或开 Docker Desktop WSL2 集成），仓库克隆到 WSL 原生文件系统 `~/`（避开 `/mnt/c`），`.wslconfig` 划内存上限。
9. 项目规范 skill 已内置 `.agents/skills/`（`aegis-ci` / `aegis-go-quality` / `aegis-ddd-layout` / `aegis-security-review`），Kimi Code 与 DeepSeek Harness 等 harness 均按项目级 `.agents/skills/` 目录识别，新会话即自动生效。

## 12. 变更纪律

本文档在以下任一条件触发时必须修订，修订记录追加在文末：

1. 任一里程碑验收结果与排期偏差 >20%（重排后续周次，不许静默漂移）；
2. 新增 ADR（设计决策变了，执行计划跟着变）；
3. Non-Goal 被挑战（N1–N6 任一条想突破，先 ADR 后改计划）；
4. ADR-002 复评触发降级（kagent → 自研薄运行时，M1 末检查点）。

修订历史：v1.0（2026-09-22）首版，依据设计文档 v1.3 编制。v1.1（2026-09-22）随设计文档 v1.4 同步：T0.1 落地轻型 DDD 目录与 depguard 门禁、§9 开发环境定锚 WSL2、新增附录 A/B、内置 `.agents/skills/` 项目规范四个（ci/go-quality/ddd-layout/security-review）。

---

## 附录 A：轻型 DDD 落地要点

- 目录结构与五条依赖规则以设计文档 §3.2（v1.4 起）为准；执行速查（新代码放哪、反模式清单）见 `.agents/skills/aegis-ddd-layout/SKILL.md`。
- 依赖方向不写口头约定，落成 `.golangci.yml` 的 depguard 规则，CI 强制；PR 模板含"依赖方向未违规"勾选项。T0.1 的完成判据含一条 depguard 违例拦截验证。
- 各 `mcp-servers/*` 进程内部同样遵守三段式：handler（协议适配）→ 参数校验 → 调用 `internal/<context>`；放行判断永不下沉到工具层（设计文档 §4.1）。
- `eval/`（Python）不参与 Go 依赖方向约束，但禁止反向 import Go 内部包——它只通过场景 manifest 与 CI 门禁与主仓交互。

## 附录 B：跨平台规则

1. 自动化一律 Makefile + `scripts/*.sh`（bash）；PowerShell / BAT 不进关键路径。CI 步骤显式 `shell: bash`。
2. `.gitattributes` 钉 `*.sh` / `Makefile` / `*.yml` 为 LF；Go / Rego / Python 本身跨平台，禁 CGO、禁平台特定 syscall、路径一律 `filepath`。
3. CI（ubuntu-latest）是唯一裁决环境；Windows Git Bash 与 WSL2 只是开发入口，"本地过了 CI 没过"一律以 CI 为准修本地。
4. 主开发环境 WSL2 Ubuntu：仓库放 WSL 原生文件系统；kind / helm / kubectl 安装脚本与 CI 共用同一份。
5. gVisor：WSL2 内 kind + runsc（ptrace 平台兜底，KVM 平台视嵌套虚拟化）；验证前置到 W2 出口自检（见 §9）。
