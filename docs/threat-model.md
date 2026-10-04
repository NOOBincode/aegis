# 威胁模型 — aegis 攻击面、缓解映射与红队档案

> 所属层：跨层安全档案（L2 闸门为缓解重心）｜ 里程碑：M1 建骨架、M2 前补齐正文（落地方案 T0.6），每个红队检查点后更新 ｜ 设计文档出处：§2（I1–I10）、§4.2、§6（F1–F15）、§10（红队记录 R1–R15）｜ 关联不变量：I1–I10 全部 ｜ 关联失败模式：F1–F15 全部

## 1. 设计初衷与维护纪律

本文件是 aegis 的"怕什么、防什么、查出过什么"底账。差异化叙事钉死在 L2 安全闸门与 L4 eval（设计文档 §1.5），而闸门是安全组件——自研安全组件出现逻辑漏洞即核心价值崩塌（设计文档 §9），因此威胁模型不是合规装饰，是设计迭代的输入：每次红队检查点的发现必须回写这里，每次新增写工具必须先登记攻击面（`.agents/skills/aegis-security-review/SKILL.md` 写工具准入第 5 条）。

维护纪律三条（细则见第 8 节）：

1. 每次红队检查点（M1/M2/M3 末尾固定动作，落地方案纪律 R-7）后更新本文件，检查点不更新 = 检查点未过。
2. 新增写工具、新增组件、新增外部依赖时，先补攻击面清单与缓解映射表，再合入。
3. 全部记录追加制：历史行不删不改，状态变化（如"已整改"→"复发"）另起新行。

## 2. 资产清单

攻击者真正想要的东西（失守后果按最重口径计）：

| # | 资产 | 内容 | 失守后果 | 守卫 |
| --- | --- | --- | --- | --- |
| A1 | 集群写权限 | gatekeeper 持有的目标集群写凭证（目标集群注册表，设计文档 §4.2） | 攻击者获得绕过闸门的直写通道，I1–I3 全部门禁形同虚设 | 闸门 fail-closed（I10）、闸门自身 SA 最小权限 |
| A2 | 审批权 | approval-svc 的服务端审批状态机与 `aegis-cli approve/deny` 凭证 | R2 人审可被伪造，红队 R1 的发现复发 | 审批状态机在服务端 controller（R1 整改），CLI 仅为视图 |
| A3 | 会话与决策数据 | DecisionRecord（输入快照、工具调用序列、LLM 调用链引用）、会话上下文与预算计数 | 决策链可篡改或断档，I6 回放失真，审计叙事作废 | DecisionRecord 落盘不可变存储（设计文档 §5.3）、retentionDays |
| A4 | LLM 通道 | OpenAI 兼容 API 凭证、私域 vLLM 端点、会话 token 预算 | 成本失控；提示词投毒的新入口；私域通道失守波及工作场景 | 双通道抽象、会话预算上限（GuardrailPolicy `sessionBudget`） |
| A5 | 策略库 | `policies/` Rego 包与 GuardrailPolicy CRD | 策略层被绕过（R3 仍有代码级 hardDeny 兜底，见 §5.2 注释） | hardDeny 代码级强制（I1）、策略变更走 PR 人审 |
| A6 | 沙箱凭证 | 沙箱内 R0 只读 kubeconfig、诊断镜像 | 逃逸后影响面外溢（F9） | gVisor/Kata 隔离 + 网络策略禁直连 apiserver 写端口 |

## 3. 攻击者画像

| 画像 | 能力假设 | 典型手法 | 对应材料 |
| --- | --- | --- | --- |
| 租户注入者 | 能在集群内写日志 / 事件 / 注解（租户应用即其阵地） | 在 Pod 日志写入指令型文本、角色扮演文本，等待 Agent 读取后"服从" | I4、F2、红队 R3 |
| 供应链攻击者 | 能污染依赖、镜像或用户提交的诊断脚本 | 投毒开源依赖（故依赖仅 MIT/Apache-2.0/BSD 且须陈述理由）、在排查脚本里藏带外行为 | F9、依赖纪律（aegis-go-quality） |
| 误用者（内部人） | 合法持有 CLI / 会话，但图省事或图快 | 加 `--yes` 类参数绕过审批、自写客户端直接改 CR status、审批疲劳后闭眼点通过 | 红队 R1、F8 |
| LLM 幻觉（非人攻击者，等效威胁） | 无主观恶意但不可预测 | 产出危险写操作、把正常控制器行为误判为故障而"修复"、诊断循环不收敛 | F1、F11、F4 |

## 4. 攻击面清单

编号 AS-NN，登记制（新增写工具 / 组件时先登记后合入）：

| # | 攻击面 | 入口 | 利用前提 |
| --- | --- | --- | --- |
| AS-01 | 读路径注入 | 集群返回数据（日志/事件/描述/指标文本）进入 LLM 上下文 | Agent 读取了被污染对象 |
| AS-02 | 审批绕过 | CLI 参数、自写客户端直连 apiserver 改 `status` | 审批逻辑曾落在客户端（已整改为服务端状态机） |
| AS-03 | 重放 / 重投 | 复制 MCP 信封重发；调用方网络重试 | 无幂等键或信封序号可绕过 |
| AS-04 | 沙箱逃逸 | 沙箱内执行恶意诊断脚本 | 沙箱隔离失效（低概率高影响，F9） |
| AS-05 | 预算耗尽 / 绕过 | 会话工具调用数、token 数、apiserver QPS | 预算计数存在绕过路径 |
| AS-06 | 写路径越权 | 诱导 Agent 拼出 R3 操作的变体、LLM 幻觉危险操作 | 分级查表或 hardDeny 有漏洞 |
| AS-07 | 与控制器双写冲突 | 直写已被 HPA/VPA/ArgoCD 所有的字段 | 所有权检查缺失 |
| AS-08 | 闸门自身失效被利用 | 闸门崩溃、与 apiserver 分区、webhook 超时 | 失效期间出现"静默放行"或悬挂变更被二次执行 |
| AS-09 | 回滚欺骗 | 回滚时目标已被第三方修改（状态漂移） | 回滚前不复验漂移即执行 |
| AS-10 | 供应链 | 恶意依赖 / 镜像 / 用户脚本 | 依赖准入与镜像钉死失效 |

## 5. 现有缓解映射表（攻击面 → 不变量 → 组件 → 失败模式）

| 攻击面 | 不变量 | 缓解组件 / 机制 | 失败模式编号 | 残余风险与姿态 |
| --- | --- | --- | --- | --- |
| AS-01 | I4 | log-sanitizer：`<untrusted_cluster_data>` 包裹 + 注入模式检测（指令型关键词、角色扮演命中），命中后该数据衍生动作自动升 R2 | F2 | 模式库不可能完备，故升 R2 兜底而非仅告警 |
| AS-02 | I1、I6 | approval-svc 服务端状态机；自写客户端伪造审批的单测证明（T2.4 完成判据） | F8（疲劳侧） | 状态机流转全程 DecisionRecord 留痕 |
| AS-03 | I10 | idempotencyKey 派生 CR 名（重试折叠）+ MCP 通道 mTLS / 签名信封 / 会话内序号 | F15 | 序号乱序拒绝；重复提交返回同一 CR |
| AS-04 | I3 | 隔离委托 gVisor（默认）/ Kata（强隔离）；沙箱内 kubeconfig 硬限 R0 只读；网络策略禁直连 apiserver 写端口 | F9 | 逃逸影响封顶只读面（设计文档 §4.4） |
| AS-05 | I2、I7 | 会话双预算（GuardrailPolicy `sessionBudget`）+ apiserver QPS 限流（`rateLimit`）+ 结果集硬上限 | F4、F5 | 耗尽即暂停并输出当前结论与置信度 |
| AS-06 | I1、I7 | risk-classifier 确定性查表（LLM 初判仅参考、终判取高）+ opa-eval + hardDeny 代码级清单（R3 无审批入口） | F1 | 分级规则迭代须经 eval 验证（I8），不静默放宽 |
| AS-07 | I9 | authority-map 字段级所有权图谱（managedFields / HPA/VPA/KEDA / ArgoCD / operator）；attributor 归因先行 | F3、F11、F13 | 有主字段直写拒绝，意图翻译为 owner API 操作建议 |
| AS-08 | I10 | 闸门无状态化 + leader election 单写者 + CR 状态机在途恢复 + fail-closed 写路径 + Executing 超时巡检 | F14 | 闸门不可用 ≠ 集群可用性受损；绝不"挂了先放行" |
| AS-09 | I5 | rollbacker：逆操作强制 dry-run 验证 + 回滚前漂移复验 + `rollback.deadline` 超时转人工 | F6 | 逆操作无法生成即自动升 R2 |
| AS-10 | I3 | 依赖纪律（理由 + license 陈述）、版本钉死（`deploy/versions.md`）、写工具逐人过红队 | F9 | 闸门代码 AI 产出 100% 逐行人审（R-2） |

## 6. 红队评审记录（首轮：设计文档 §10 定稿前自审）

首轮记录为设计文档 §10 的全量转录，作为本表第 1 批历史行；后续每轮检查点追加新批次，行格式不变：编号 / 严重度 / 视角 / 发现 / 整改落点。

| 编号 | 严重度 | 视角 | 发现 | 整改落点 |
| --- | --- | --- | --- | --- |
| R1 | P0 | 安全 | 审批流若只在 CLI 客户端实现，可被 `--yes` 类参数或自写客户端绕过 | 审批状态机移到服务端 controller，CLI 仅为视图；回写设计文档 §4.2.1/§5.1 `status.state` |
| R2 | P0 | 可靠 | 逆操作生成成功≠可执行，目标状态漂移会使回滚失败，造成假安全感 | 逆操作强制 dry-run 验证+回滚前复验漂移；无法验证自动升级 R2；回写 §4.2.2、§5.1 `inverseVerified`、§6 F6 |
| R3 | P0 | 安全 | 初稿只防"写操作幻觉"，漏了读路径注入——日志即攻击面 | 新增不变量 I4、log-sanitizer 组件、失败模式 F2、M2 注入演示验收 |
| R4 | P1 | 成本 | 诊断循环可能无限调工具，token 与 apiserver 双重失控 | GuardrailPolicy 增加 `sessionBudget`；失败模式 F4 |
| R5 | P1 | 可靠 | 未处理与 HPA/GitOps 的双写冲突，Agent 与控制器会互相打架振荡 | 执行前所有权检查，冲突转 PR 通道；回写 §4.2.3、§6 F3 |
| R6 | P1 | 工程 | 文档写下 I5"一切写可回滚"但初版 schema 无中止/超时字段——说一套做一套 | §5.1 补齐 `timeout`、`abort`、`rollback.deadline` 字段 |
| R7 | P1 | 工程 | M4 调度闭环是个人项目最易失控的扩展点 | 写死过门条件（对照实验改善 ≥10%，否则砍）；回写 §4.5、§7 |
| R8 | P2 | 叙事 | "基于 kagent 二开"在简历上可能被读为"搭积木" | 差异化叙事锁定 L2+L4 自研；ADR-002 记录自研薄运行时备选；回写 §9 末行 |
| R9 | P2 | 时间 | eval 场景标注工作量被低估（AI 提速有限的体力活） | §8.1 单独列账 60–80h，并安排碎片时间承接 |
| R10 | P2 | 安全 | 沙箱内若持有可写凭证，逃逸即破防 | 沙箱内 kubeconfig 硬限定 R0 只读+网络策略；回写 §4.4、§6 F9 |
| R11 | P2 | 范围 | "不做多租户/重 RBAC"被误读为"无需任何权限设计" | 边界澄清回写 §1.4 N3/N6；权限设计维持 I3/§4.4 现状 |
| R12 | P1 | 可靠 | 集群是多控制器并发系统，初版把"被干扰"当异常而非常态环境，归因缺失必导致误修复与对抗振荡 | 新增 I9、authority-map/attributor、§4.2.4 共存写规则、F11–F13、eval 归因/误修复指标 |
| R13 | P2 | 稳定 | Agent 自身成为控制回路一员后，动作频率无约束会放大系统振荡（自我激励回路） | 目标级冷却期 + 验证窗口≥控制器稳定窗口；回写 §4.2.4 |
| R14 | P1 | 可靠 | 初版有熔断器但无闸门自身进程级失效设计：崩溃时在途变更悬挂、恢复语义未定义、重试可能双执行 | 新增 I10 + §4.2.5 + idempotencyKey + F14/F15 |
| R15 | P2 | 范围 | 千级集群 HA/worker 解耦冲动撞 N3/N6，且无可验证环境——设计出来无法过"能演示"里程碑纪律 | 留档设计文档附录 C 并写死立篇触发条件，不进主线 |

## 7. 检测信号与告警映射（观测口径）

威胁模型的每条缓解必须能被观测验证，信号口径如下（与运行时代档共用同一组命名）：

| 信号 | 类型 | 命名 | 说明 |
| --- | --- | --- | --- |
| 注入命中 | 计数指标 | `aegis_sanitizer_hit_total{pattern_type="instruction|roleplay"}` | 日志字段 `sanitizer_hit=1` 伴随告警；衍生动作升 R2 记录进 DecisionRecord `sanitizationEvents` |
| 审批异常 | 计数指标 | `aegis_approval_total{decision="approved|denied"}` + 审计字段 `approval_decision` | 客户端伪造尝试在日志留 `approval_forge_attempt=1`（T2.4 单测场景） |
| 重复提交 | 计数指标 | `aegis_idempotent_dedup_total` | 重试折叠次数；序号乱序拒绝记 `replay_rejected=1` |
| 熔断态 | 仪表 | `aegis_circuit_breaker_tripped`（0/1）+ 日志 `breaker_state` | 触发即全局降级只读 + 告警；人工复位留痕（F7） |
| 在途悬挂 | 计数指标 | `aegis_stale_cr_total` | Executing/Verifying 超时未推进即告警（F14） |
| 预算耗尽 | 计数指标 | `aegis_budget_exhausted_total{budget_type="tool_calls|llm_tokens|read_qps"}` | 会话暂停并输出当前结论与置信度（F4/F5） |
| 写路径失败 | 计数指标 | `aegis_cr_total{result="committed|rolled_back|aborted|rejected"}` | R1 失败率 >30% 触发熔断（GuardrailPolicy `circuitBreaker`） |
| 归因覆盖 | 评估指标 | `aegis_eval_metric{metric="diagnosis_accuracy|attribution_accuracy|false_repair_rate|mttr"}` | M3 起 eval-regression.yml 门禁口径（I8） |

Trace span 命名贯穿"会话 → 闸门 → 工具 → 集群"：闸门侧固定为 `gatekeeper.risk.classify`、`gatekeeper.opa.eval`、`gatekeeper.approval.wait`、`gatekeeper.rollback.execute`、`gatekeeper.sanitize.wrap`、`gatekeeper.authority.check`、`gatekeeper.attributor.attribute`、`gatekeeper.breaker.check`；全链关键字段：`cr_name`、`session_ref`、`idempotency_key`、`risk_level`、`policy_decision`、`approval_decision`、`degraded_mode`。审计留痕点：每次会话落一份 DecisionRecord（设计文档 §5.3），回滚与熔断复位必须各产生一条留痕。

## 8. 变更与复评纪律

1. **检查点后更新**：红队检查点 #1（M1 末，读路径注入初测）、#2（M2 末，项目准入门槛：审批绕过 / 日志注入 / 越权 / 重放 / 预算绕过全量过）、#3（M3 末，held-out 轮换 + 干扰并发零误修复）后 48h 内追加新批次记录，含"发现 / 严重度 / 整改落点 / 验证方式"。
2. **新增攻击面登记**：新写工具按 `.agents/skills/aegis-security-review/SKILL.md` 准入清单第 5 条同步本文件第 4、5 节。
3. **追加制**：历史行不删不改；同一条目复发另起新行并关联旧编号。
4. **版本同步**：攻击面结构变化（新增 AS 编号）需对照设计文档 §6 失败模式表核对，发现设计文档缺失对应 F 编号的，先补设计文档再登记。
5. **门槛声明**：M2 检查点未全量过且本文件未更新，按设计文档 §9 口径——项目不进入 M3。
