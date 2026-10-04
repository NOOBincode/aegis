# cmd/aegis-cli — 用户交互 CLI

> 所属层：工具层（用户入口，控制面客户端）｜ 里程碑：M1（replay）/ M2（approve、deny、breaker reset、status）/ M3（eval run）｜ 设计文档出处：§4.2.1、§5.1–§5.3、§10 红队 R1 ｜ 关联不变量：I6 ｜ 关联失败模式：F7、F8

## 1. 设计初衷

aegis-cli 是人操作 aegis 的入口：审批、回放、状态查看、熔断复位、eval 触发。它的第一设计原则来自红队 R1 整改项（设计文档 §10）：**审批状态机若在 CLI 客户端实现，可被 `--yes` 类参数或自写客户端绕过**。因此本 CLI 自始至终只是**视图与提交器**——它渲染服务端状态（读 apiserver 的 CR status）、向服务端提交意图（写 spec），但不含任何判定逻辑；判定的唯一居所是 gatekeeper 同步链路与 controller 状态机（`cmd/gatekeeper/README.md`、`cmd/controller/README.md`）。第二原则：**人机双通道输出**——默认人读视图，`-o json` 机读，两者同一数据源（CR status），保证 CI 脚本与人工看到的事实一致。第三原则：写类子命令零绕过设计（见 §4 禁止参数条款），这是红队检查点 #2 的审计对象。

## 2. 职责与任务清单

**子命令规划**（对照落地方案任务编号）：

| 子命令 | 职责 | 关键参数语义 | 服务端落点 | 任务来源 |
| --- | --- | --- | --- | --- |
| `approve <cr>` | 提交审批通过意图（R2 人审通道之一，§4.2.1） | cr 名；`--note` 审批意见（机读为字段 `note`） | 写 `spec.approval`（approver 取自本地 kubeconfig 身份，channel=cli）；状态机校验合法性 | T2.4 |
| `deny <cr>` | 提交审批拒绝意图 | cr 名；`--note` 拒绝理由（机读为字段 `note`） | 写 `spec.approval`（approver 取自本地身份，channel=cli）；状态机校验合法性，迁移结果 Rejected | T2.4 |
| `status <cr>` | 视图：状态机位置、verification、rollback 信息、abort 状态 | `-o json` | 只读 status，零写 | M2 配套 |
| `get <kind>` | 列表/详情视图（changerequest / guardrailpolicy / decisionrecord） | 名称可选；label selector、分页参数语义与 kubectl 对齐 | 只读 | M2 配套 |
| `replay <decision-record>` | 回放完整决策链（M1 验收口径：`aegis-cli replay` 回放完整决策链） | record 名；可选步骤区间 | 读 DecisionRecord（§5.3 全量字段）及其 `clusterSnapshotRef`/`llmTraceRef` 引用 | T1.8 |
| `eval run` | 触发 eval 回归或单场景运行，报告落盘 | `--scenario`（可多次）；`--report` 机读报告路径 | M3 执行入口；评分逻辑在 `eval/graders/`，CLI 不内嵌评分；CI 门禁仍以 `eval-regression.yml` 为准 | T3.4 配套 |
| `breaker reset` | 熔断人工复位（F7：熔断后无人复位的对冲） | `--reason` **必填** | 向服务端提交复位意图（通道与目标对象以 T2.6 实现为准，语义固定为显式留痕的人工动作） | T2.6 |
| `version` | 版本与构建信息 | — | — | 工程配套 |

范围控制：新子命令准入口径——凡涉及写动作的（如未来新增 `abort`），每个过一遍红队（`.agents/skills/aegis-security-review/SKILL.md` 新写工具准入清单的 CLI 版），威胁模型同步更新（`docs/threat-model.md`）。

**职责黑名单**：不做任何放行/分级/回滚判定；不缓存服务端状态做"本地判定"；不实现审批状态机的任何一环；不直接调 L1 工具层或集群写接口。

## 3. 技术选型与开源包

| 项 | 选型 | 理由 | 放弃项及原因 |
| --- | --- | --- | --- |
| 命令框架 | **spf13/cobra**（底层 pflag，与 daemon 语法一致，见 `cmd/README.md` §3） | 子命令树 + 稳定 flag 语义 + shell completion + 与 kubectl 同构的运维手感；`deny`/`breaker reset` 这类"带强约束参数"的命令靠 cobra 的 flag 校验机制落地 | 标准库 `flag`：无子命令树，自研路由是负资产 |
| 人读输出 | 标准库 `text/tabwriter` 对齐表格 | 零依赖；CI 日志可读（不依赖终端特性、不输出 ANSI 控制码） | 富文本表格库：非必要依赖 |
| 机读输出 | 标准库 `encoding/json`，`-o json` | 字段名承诺与 `api/v1alpha1` 的 json tag 对齐（机器可稳定解析） | 自造文本格式：机读契约不可漂移 |
| 集群凭证 | client-go 的 kubeconfig 装载规则（版本以 `deploy/versions.md` 为准） | 与 gatekeeper/controller 同一凭证体系；用户身份天然来自 kubeconfig，审批人字段可审计 | CLI 自带凭证体系：双体系 = 审计裂缝 |
| 配置 | env + flag 双层（同 `cmd/README.md` §3），禁 viper | 配置面极小（kubeconfig 路径、上下文、输出格式、apiserver 地址覆盖） | viper：重型依赖 |
| 观测接入 | **不初始化 OTel exporter**（短进程） | 避免每次调用建联 collector 的开销；trace 连续性经 DecisionRecord `llmTraceRef` 实现（§5.3） | 每次命令带 OTLP：得不偿失 |

## 4. 具体设计（不写代码）

**审批语义**（approve/deny 的核心契约）：

1. CLI 只被授权写 **spec**：`spec.approval.approver`（取自当前 kubeconfig 用户）、`spec.approval.decidedAt`（CLI 本地时间，服务端以收到时刻复核）、`spec.approval.channel=cli`、可选 note。
2. status 由 controller 独占（K8s status subresource 权限隔离），迁移合法性校验在服务端：CR 须处于 AwaitingApproval、级别确需审批、未超 `rollback.deadline`、非目标冷却期、非熔断态。
3. 校验失败 = CLI 收到服务端明确拒绝理由，原样呈现，**不回退为本地猜测**。
4. 自写客户端绕过 CLI 直改 status：无效（状态机纠正，见 `cmd/controller/README.md` §5）——CLI 在此不是安全边界，只是合规通道。

**输出契约**：

- 人读：状态机位置用状态名原样呈现（Pending/DryRunning/AwaitingApproval/Executing/Verifying/Committed/RolledBack/Aborted/Rejected）；审批视图必附影响面摘要（`estBlastRadius`）与回滚截止时间（`rollback.deadline`）。
- 机读：`-o json` 输出为 CR 对象（或 CR 列表）的规范化编码，字段名对齐 `api/v1alpha1`；退出码与输出分离——脚本永远先查退出码再解析输出。
- `replay` 输出五要素（I6 在视图的呈现）：输入快照引用（`clusterSnapshotRef`）、工具调用序列、审批记录、执行结果、`sanitizationEvents`（注入命中回放）。

**退出码约定**（在 `cmd/README.md` 三进程统一码 0/1/2 之上扩展业务码）：

| 退出码 | 含义 |
| --- | --- |
| 0 | 成功（含 `deny` 成功提交、查询到 Rejected 状态——状态事实不是 CLI 错误） |
| 1 | 未预期内部错误（含 JSON 编码失败） |
| 2 | 用法 / 参数错误（cobra 默认语义，未知子命令、缺必填 flag） |
| 3 | 集群连接 / 认证失败（apiserver 不可达、凭证无效） |
| 4 | 资源未找到（CR / DecisionRecord 不存在） |
| 5 | 服务端规则拒绝 / 状态冲突（对终态 CR 提交审批、状态机校验失败、RBAC 不足） |

**禁止参数条款**（设计说明，非实现）：本 CLI **不存在** `--yes`、`--force`、`--skip-approval`、`--insecure-skip-*` 类参数，也不提供隐藏 flag；flag 清单是 PR 评审必查项与红队检查点 #2 审计项（自写客户端 / `--yes` 类参数绕过）。理由：CLI 没有任何"需要被绕过"的本地判定，绕过型参数在此没有合法用途，存在的唯一效果是给审批链路开一个社会工程口子（F8 审批疲劳的放大器）。`breaker reset` 用必填 `--reason` 代替交互式 y/N——留痕优先于操作便利。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| apiserver 不可达 / 超时 | client 调用错误 | 退出码 3 + 重试提示（不重试写动作，防重复提交焦虑） | **fail-closed**：CLI 拿不到权威状态就报错退出，不打印猜测；审批类写操作幂等性由服务端 idempotencyKey 兜底（F15），CLI 层不重复提交 |
| 凭证无效 / RBAC 不足（403） | apiserver 状态码 | 退出码 5，原样呈现服务端 reason | fail-closed；不引导用户"换高权限凭证" |
| 资源未找到（404） | apiserver 状态码 | 退出码 4 + 相似资源建议（仅 get 类） | 只读路径可引导，写路径直接终止 |
| 服务端规则拒绝（状态机校验失败） | 服务端 reason 字段 | 退出码 5，完整呈现 reason（含哪条规则：deadline / 冷却期 / 熔断态） | 判定永远归服务端；CLI 的呈现义务是**不截断**拒绝理由 |
| 输出编码失败 | 本地编码错误 | 退出码 1，不落半成品文件 | 机读契约宁可不写不可写坏（脚本安全） |
| `eval run` 指标回退 | 报告内指标对比 | 退出码非 0（具体码值以 T3.4 报告 schema 定），供 CI 门禁使用 | I8 eval 先行的执行端钩子 |

错误消息约定（aegis-go-quality）：全小写、无结尾标点、不带栈_trace_给用户；debug 级本地日志另记。危险命令保护：`deny` 与 `breaker reset` 要求显式目标名（不接受模糊匹配），`breaker reset` 的 `--reason` 为空即拒绝执行（退出码 2）。

## 6. 可观测性（日志 / 指标 / Trace / 审计）

CLI 是短进程，不设指标端点、不接 OTel exporter（§3）；其可观测性义务在**审计与本地 debug**：

- **审计留痕点**（全部经服务端落库，不在 CLI 本地）：① 审批提交——`spec.approval` 写入即入 CR 与 DecisionRecord 关联审计（approver / decidedAt / channel=cli / note）；② `breaker reset`——reason、操作者（kubeconfig 身份）、时间，随复位动作留痕（F7 要求"显式复位且留痕"）；③ `replay` 读取事件——审计侧可发现谁在何时回放了哪条决策链；④ CLI 不产生任何绕过审计的写路径。
- **本地 debug 日志字段**（`--v` 分级，slog）：`command`、`cr_name`（或目标对象名）、`kube_user`、`output_mode`（table/json）、`duration_ms`、`exit_code`、`api_latency_ms`。**不记录**工具全量参数与凭证材料，防敏感信息落盘。
- **trace 关联**：不产 span；`replay` 视图呈现 `llmTraceRef`（Langfuse 引用），人工/trace 系统的对账入口在 DecisionRecord（§5.3）。

## 7. 依赖方向与模块边界

- **谁调我**：人类用户（交互）；CI 脚本（`-o json` + 退出码，如 `kind-smoke.yml` 与 `eval-regression.yml` 的编排步骤）；eval 流水线。
- **我调谁**：apiserver（CR 读、spec 写）；`api/v1alpha1` 类型（机读编码契约）；`internal/adapters/k8s` 的客户端构造（只读装载，复用而非重造）；`eval run` 经 Makefile 目标转调 `eval/` 的 Python runner（进程外，控制面禁内嵌非 Go 运行时，aegis-go-quality 依赖纪律）。
- **禁止依赖谁**：禁止 import `internal/` 各上下文的 service / domain 业务实现（CLI 无业务可下沉，视图逻辑就地内聚于 cmd 属允许的装配残余——呈现格式化这类纯视图代码没有更合适的归属）；禁止 import gatekeeper / controller 进程的任何包（一切经 apiserver 契约）；禁止直接触达 L1 工具层与目标集群写接口；禁止 import 评分逻辑（`eval/` 是独立 Python 世界，落地方案附录 A）。
- **被依赖关系**：无（main 包）。

## 8. 测试策略与红队用例

1. **单测（表驱动）**：输出渲染（人读列对齐、`-o json` golden 文件，字段名对齐 `api/v1alpha1`）；退出码映射全表；flag 校验（`--reason` 必填等）。
2. **kind 集成**：`approve` 走完整链路——提交 → controller 校验 → 状态迁移可见于 `status`（T2.4）；`replay` 回放 M1 诊断会话完整决策链（T1.8 完成判据）；`breaker reset` 复位后熔断态解除且留痕可查（T2.6/F7）。
3. **红队检查点 #2 映射（M2 准入门槛）**：
   - `--yes` 类参数不存在性：flag 清单全量审计（`--help` 输出快照入库比对）；
   - 自写客户端绕过：配合服务端用例——CLI 证明自身无特权通道（服务端判伪用例见 `cmd/controller/README.md` §8）；
   - 审批疲劳口子（F8）：验证 `approve` 必须逐 CR 显式指定 + 呈现影响面摘要，无批量放行参数。
4. **权限最小化用例**：CLI kubeconfig 仅具备 spec 写 + 读权限时全部子命令可用；获得 status 写权限的凭证被服务端拒绝（subresource 隔离验证）。
5. **CI 钩子**：eval 回归回退时 `aegis-cli eval run` 非零退出，作为 I8 门禁的执行端验证手段（T3.4 配套）。
