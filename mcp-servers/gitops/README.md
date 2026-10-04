# gitops — open_change_pr：R2 变更的唯一通道

> 所属层：L1 工具层 ｜ 里程碑：M2（落地方案 T2.10） ｜ 设计文档出处：§4.1、§4.2.3（ADR-001） ｜ 关联不变量：I1、I5 ｜ 关联失败模式：F3、F8

## 1. 设计初衷

ADR-001 的决策：R1 直写集群（低延迟、交互闭环），R2 强制走 GitOps PR（评审与回滚天然自带），R3 硬拒。本 server 就是 R2 通道的执行末端——Agent 对 GitOps 管资源的全部变更意图，最终形态是**一个 PR 链接，而不是一次集群变更**（设计文档 §4.1 gitops 类定义）。这个设计同时化解两个失败模式：F3（与 ArgoCD 双写冲突——Agent 不直写，就没有双写）；F8 的部分成因（R2 人审流于形式——PR 描述强制携带诊断链与影响面，评审者有上下文可判，不是橡皮图章）。

代价与接受（ADR-001 原文）：R2 交互断裂；换来审计与回滚的确定性。

## 2. 职责与任务清单

**职责**：把闸门放行后的变更意图（manifest_patch + description）落到白名单仓库的一个新分支与 PR 上，返回 PR 链接。**不直接触达集群**——本 server 的配置中不存在任何集群凭证，物理上不可能产生集群变更（F3 的技术兜底）。

**任务清单**：

| 任务 | 交付 | 出处 |
| --- | --- | --- |
| T2.10 | open_change_pr：产出 PR 链接而非集群变更；ArgoCD 管资源一律转此通道 | 落地方案 W17 |
| T2.9 | authority-map 判定 ArgoCD 所有权 → 路由到本通道（路由在 L2，本层是被路由方） | 落地方案 W16 |
| T2.4 | 审批附诊断链 + 影响面报告（PR 描述模板的字段来源） | 落地方案 W12 |

## 3. 技术选型与开源包

- GitOps 引擎：ArgoCD（设计文档 §3.3 选型：生态最大、drift 检测现成；Flux 为等价放弃项）——本 server 不直接调 ArgoCD API，PR 合入后由 ArgoCD 既有 sync 流程接管；
- 出站访问：git 托管平台 API（PR/branch 创建），经 `internal/adapters` 对应端口封装；具体平台与 SDK 版本以 `deploy/versions.md` 钉死为准；
- 共享框架 `internal/mcpserver`；repo 白名单配置由 L2 闸门配置层维护（与目标集群注册表同层，设计文档 §4.2）。

## 4. 具体设计（不写代码）

### 4.1 工具清单

| # | 工具 | 入参语义 | 强制约束 | 返回结构 |
| --- | --- | --- | --- | --- |
| 1 | open_change_pr | repo（目标仓库，必填）、manifest_patch（变更内容，结构化 patch 描述）、description（变更说明，必填）、cr_ref（关联的 ChangeRequest ID，必填） | repo 必须在白名单内；manifest_patch 字节上限（建议初值 64KB，超限要求拆分）；分支名从 CR 派生（幂等锚） | `pr_url`、`branch`、`commit_ref`、`cr_ref` 回执；`no_cluster_mutation: true` 恒真标注 |

入参与 k8s-write 的对照：k8s-write 只接受 `cr_id`，本工具接受结构化参数，因为 R2 通道的产物是给人评审的变更文本——但 `cr_ref` 必填把每次 PR 绑定到已过人审的 ChangeRequest（`status.state` 已过 `AwaitingApproval` 且审批通过），自由 PR 不成立。

### 4.2 repo 白名单

1. 白名单由 L2 闸门配置层维护，本工具每次调用先校验 repo ∈ 白名单，不在即拒绝（E-REPO）；
2. 白名单之外不存在任何"临时加仓库"的便捷路径——加仓库走配置变更评审，与 GuardrailPolicy 变更同级留痕；
3. 本期白名单 = aegis 目标集群对应的 GitOps 配置仓库（单一仓库为常态），多仓库是 M4 之后的事（N6 边界）。

### 4.3 PR 描述模板（强制结构，缺段拒绝创建）

PR 描述固定五段，字段值由 description 入参与关联 CR 共同固化：

1. **诊断链**：`sessionRef`、DecisionRecord `auditRef`、关键工具调用摘要（从 CR `spec.sessionRef` 与 audit 记录取，不凭 Agent 自述）；
2. **影响面**：CR `estBlastRadius` 原值（pods/nodes/namespaces 三元组）+ 关联对象清单；
3. **变更内容**：manifest_patch 的摘要说明（patch 本身附在 PR 文件变更里）；
4. **期望处置级别**：CR `operations[].riskLevel`（终判，非 LLM 自评）；
5. **回滚方式**：git revert + ArgoCD sync 的标准路径说明（I5 的 GitOps 形态：回滚= revert 合入，与直写路径的 rollbacker 逆操作互为补充）。

### 4.4 元数据五字段

| 工具 | risk_hint | idempotent | reversible | est_blast_radius | timeout_ms（建议初值） |
| --- | --- | --- | --- | --- | --- |
| open_change_pr | R2（通道级恒 R2，ADR-001） | true（分支名从 CR 派生，重复调用折叠到同一分支/PR） | true（git revert 天然可逆） | 由关联 CR 的 estBlastRadius 决定，超限拒绝 | 30000 |

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | fail-closed / 降级 | 对应 |
| --- | --- | --- | --- | --- |
| repo 不在白名单（E-REPO） | 白名单核对 | 拒绝 + 配置变更引导 | fail-closed | I1、F3 |
| 关联 CR 状态非法 | cr_ref 状态复核（须过人审） | 拒绝创建 PR（无审批无 PR） | fail-closed | I1 |
| manifest_patch 超限（E-LIMIT） | 字节计数 | 拒绝 + 拆分引导 | fail-closed | I7 精神 |
| PR 描述缺段 | 模板五段校验 | 拒绝创建（模板强制） | fail-closed | F8 缓解 |
| git 平台 4xx/5xx（E-UPSTREAM） | 状态码 | 返回 upstream_error + 已创建中间产物的清理说明；分支已建则复用（幂等） | fail-closed，不产生半状态幻觉 | — |
| patch 与目标分支冲突 | 平台冲突语义 | 返回 conflict + 建议 rebase 窗口，不自动 force 覆盖 | fail-closed（覆盖他人变更是数据破坏） | — |

**失效姿态**：本工具所有失败都是 fail-closed——失败结局是"没有 PR"，绝不退化为"直连集群改一下"。集群凭证在本 server 配置中物理缺席，这是对 F3 的最终兜底（即使代码被注入，没有凭证可偷）。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

- **日志字段**：L1 统一字段集 + `repo`、`branch`、`cr_ref`、`pr_url`、`patch_bytes`、`template_sections_ok`、`whitelist_hit`。
- **指标**：`aegis_tool_calls_total{server="gitops", tool, result}`；`aegis_gitops_pr_total{result}`；`aegis_gitops_pr_duration_seconds`；`aegis_gitops_repo_denied_total`（白名单拒绝计数，异常抬升=探测信号）。
- **Trace span**：`l1.gitops.open_change_pr`，子 span `.validate` / `.render`（模板渲染）/ `.upstream`（git 平台 API）。
- **审计留痕点**：PR 链接与 cr_ref 的绑定关系落 DecisionRecord `spec.toolCalls` 与 CR `status`；PR 描述五段即评审包本身（F8：审批通过率/耗时监控的数据源之一在 L2，本层保证每次审批有完整材料）。

## 7. 依赖方向与模块边界

- **谁调我**：M2 起为 L2 gatekeeper MCP 代理转发——路由规则（authority-map 判定 ArgoCD 管资源 → R2 通道）在 L2，Agent 只提出意图。
- **我调谁**：`internal/mcpserver` 框架、`internal/adapters` 的 git 平台端口、（M1 形态）`internal/sanitize` 出口包裹。
- **禁止依赖谁**：禁止持有任何集群出站端口/凭证（与 k8s-read、k8s-write 的适配器隔离）；禁止 import L2 判定组件（路由在闸门）；禁止绕过 cr_ref 建"裸 PR"（协议层必填，无自由入口）。

## 8. 测试策略与红队用例

- **单测**：白名单拒绝；模板五段缺段拒绝；patch 超限；幂等折叠（同 cr_ref 二次调用返回同一 PR）。
- **集成测试**：对 ArgoCD 管理的 Deployment 发起变更 → 产出 PR 链接而非集群变更（T2.10 完成判据）；PR 合入后 ArgoCD sync 生效的端到端验证在 kind 环境跑通。
- **红队用例**：
  1. F3 防线：尝试让本工具直接改集群（协议注入/参数变形），验证无集群凭证、无出站端口，物理不可达；
  2. 白名单绕过：伪造 repo 参数指向外部仓库，验证拒绝 + `aegis_gitops_repo_denied_total` 计数；
  3. 无审 PR：构造未过人审的 cr_ref，验证拒绝；
  4. 注入 PR 描述：description 夹带指令文本，验证出口包裹与留痕（I4）；
  5. 重放：复制 cr_ref 重发，验证幂等折叠到同一 PR（F15 同族）。
