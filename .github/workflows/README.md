# .github/workflows — CI 门禁设计说明

> 所属层：控制面（工程基座）｜ 里程碑：T0.3 建骨架（ci.yml 始终必过），kind-smoke.yml M2 起必过，eval-regression.yml M3 起必过 ｜ 设计文档出处：§3.3（CI 门禁选型）、§7（里程碑验收）｜ 关联不变量：I8（eval 先行）｜ 关联失败模式：F10（eval 过拟合）、F14（失效语义的可验证性）

## 1. 设计初衷

aegis 是安全敏感项目（AI 操作 K8s 的闸门），CI 是门禁而非形式（`.agents/skills/aegis-ci/SKILL.md` 开宗明义）。对单人 + AI 辅助的项目，CI 承担三份职责：

1. **第二双眼睛**：闸门代码 AI 产出必须 100% 逐行人审（落地方案纪律 R-2），机器门禁兜不住人审的惰性，但至少拦住"没跑测试就推"的低级漏。
2. **不变量的机器化**：I1–I10 里能机检的部分（依赖方向 depguard、eval 回归 I8、fail-closed 冒烟）全部落成 workflow，不靠自觉。
3. **唯一裁决环境**：CI（ubuntu-latest）是唯一裁决环境，Windows Git Bash 与 WSL2 只是开发入口（落地方案附录 B-3），"本地过了 CI 没过"一律以 CI 为准修本地。

新增 workflow 前必须先回答一个问题：它守的是哪条不变量 / 失败模式（设计文档 §2 / §6）？答不上来就不加——这是本目录的准入门槛，防止 CI 膨胀成形式主义。

## 2. 职责与任务清单

**Workflow 清单与门禁起点**（照录 `.agents/skills/aegis-ci/SKILL.md` 清单表）：

| Workflow | 触发 | 内容 | 门禁起点 |
| --- | --- | --- | --- |
| `ci.yml` | push / PR | lint（golangci-lint）→ unit test（`-race` + goleak）→ build（全部 cmd 与 mcp-servers） | 始终必过 |
| `kind-smoke.yml` | PR | kind 起集群 → apply `crds/` → controller 状态机冒烟 →（M2 后）三连演示脚本 | M2 起必过 |
| `eval-regression.yml` | prompt / 模型 / 工具链变更 | `eval/` 全量场景回归，指标回退即阻断（I8） | M3 起必过 |

**任务清单**：

1. T0.3：CI 骨架四个 job 级别的空架子全绿（落地方案 T0.3 完成判据："空架子 PR 触发全绿"）。
2. M1 期间：kind-smoke 先以"起集群 + apply crds + 状态机冒烟"跑通，为 M2 升级门禁打底。
3. M2 起：三连演示脚本接入 kind-smoke，成为合并硬门禁。
4. M3 起：eval-regression 生效，任何 prompt / 模型 / 工具链变更必须附回归报告（纪律 R-3）。
5. 全周期：每次新增 workflow 过第 1 节的准入问题；每次升级门禁口径同步更新本文件与 PR 模板。

## 3. 技术选型与开源包

- **平台**：GitHub Actions（设计文档 §3.3：个人项目零成本）+ kind（唯一被允许接触的集群）。
- **版本钉死**：Go 版本用 setup-go 的 `go-version-file` 从 `go.work` / `go.mod` 读；kind / kubectl / helm / Chaos Mesh / ArgoCD / Prometheus / OTel collector / Langfuse 等组件版本以 `deploy/versions.md` 为单一事实源，改版本 = 单独 PR（aegis-ci skill 编写规则）。
- **本文件不写具体版本号**：所有组件版本一律"以 `deploy/versions.md` 钉死为准"；action 版本钉法见第 4 节编写规则第 4 条。

## 4. 具体设计（不写 workflow yaml）

**workflow 一：`ci.yml`**（始终必过）

1. lint job：跑根 `Makefile` 的 lint 目标；`.golangci.yml` 是唯一事实源，本地 `make lint` 与 CI 跑同一配置（含 depguard 架构规则）。
2. unit test job：表驱动单测 + `-race` 默认开启 + 并发代码 goleak；领域层纯单测不起集群。
3. build job：全部 `cmd/` 与 `mcp-servers/` 产物可构建。
4. 三个 job 串行门禁语义：前序红则后续不跑，PR 不可合并。

**workflow 二：`kind-smoke.yml`**（M2 起必过）

1. 前置条件：PR 触发；kind 起单集群（安装脚本与 WSL2 开发环境共用同一份，不写第二份）。
2. apply `crds/` 下 ChangeRequest / GuardrailPolicy / DecisionRecord / SchedulingHint 全套。
3. controller 状态机冒烟：手写 CR yaml 走完整状态机（落地方案 T2.1 完成判据的 CI 化），杀进程重启后 reconcile 正确重入（I10）。
4. M2 起追加三连演示脚本：①scale → dry-run → 审批 → 执行 → 一键回滚；②恶意日志注入不越权且告警；③删 namespace 被硬拒（设计文档 §7 M2 验收逐字）。
5. 全程禁止访问任何真实集群 / 生产凭据，kind 是唯一集群（编写规则第 7 条）。

**workflow 三：`eval-regression.yml`**（M3 起必过）

1. 触发口径：prompt / 模型 / 工具链变更（纪律 R-3：M1/M2 期间先把场景格式定下来，避免 M3 返工）。
2. 执行 `eval/` 全量场景回归（20 场景，含 ≥5 个控制器干扰类）。
3. 指标口径：诊断准确率、归因正确率、误修复率、MTTR 四项并列（设计文档 §4.6），指标回退即阻断发布（I8）。
4. 成本控制：回归用中小模型 + 会话预算上限（GuardrailPolicy `sessionBudget`）；LLM 调用必须带月度预算告警（设计文档 §9 LLM 成本行）。
5. 回归报告作为运行产物归档，PR 描述附报告链接（纪律 R-3 的"回归报告链接"即此）。

**编写规则**（照录 `.agents/skills/aegis-ci/SKILL.md` 编写规则节，逐条为硬性要求）：

1. 每个 shell 步骤显式 `shell: bash`；`runs-on` 固定 `ubuntu-latest`。
2. `permissions:` 最小化（默认 `contents: read`，按需逐项加）。
3. 同一 PR 配 `concurrency` 分组 + `cancel-in-progress: true`。
4. 第三方 action 钉 major tag 并在同行注释写确切版本；官方 actions（checkout / setup-go）可跟 major。
5. Go 版本用 setup-go 的 `go-version-file` 从 `go.work` / `go.mod` 读；Python 及其余组件版本以 `deploy/versions.md` 为单一事实源。
6. 禁止在 CI 访问任何真实集群 / 生产凭据；kind 是唯一集群。
7. LLM 调用（eval 回归）必须带月度预算告警与单次会话预算上限。

**PR 模板双必填说明**（缺一不合并，aegis-ci skill 分支 / 提交 / PR 节）：

1. **不变量检查单**：设计文档 §2 的 I1–I10 逐项对照，标 N/A 或说明（纪律 R-4：违反任一项的 PR 不合入）。
2. **AI 产出声明**：AI 生成占比 + 人审人；L2 安全包（`internal/risk`、`policy`、`approval`、`rollback`、`breaker`、`authority`、`sanitize`、`audit`、`session`、`controller`）要求 100% 逐行人审（纪律 R-2）。

## 5. 错误处理与失效语义

CI 自身的失败语义按红线口径执行：

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| lint / 单测 / build 红 | ci.yml 对应 job 失败 | 阻断合并；L2 安全包覆盖率 <80% 需在 PR 说明（aegis-go-quality） | fail-closed |
| 状态机冒烟失败 | kind-smoke.yml 红 | M2 起阻断合并；杀进程 fail-closed 场景不通过 = I10 失守 | fail-closed：冒烟不过即闸门失效语义不可信 |
| 三连演示任一不过 | kind-smoke.yml 演示步骤红 | 视为 M2 验收口径被违反，回查代码而非放宽演示 | fail-closed（对应设计文档 §7 逐字口径） |
| eval 指标回退 | eval-regression.yml 比对基线 | 阻断发布（I8 / F10 防线） | fail-closed：宁可不发布，不带病迭代 |
| 门禁超时（kind 起不来 / 下载卡死） | job 级超时 | 重跑；连续失败查 `deploy/versions.md` 与网络代理配置 | 超时降级为"本地过了先合"是一票否决的违规 |
| GitHub Actions 平台故障 | 平台状态页 | 延期合并或本地复跑相同 Makefile 目标留证，不得绕过门禁合入 | fail-closed：CI 是门禁，门禁缺席时默认不放行 |
| eval 预算告警 | 月度预算计数 | 当月中止全量回归，降级为影响面子集并记录 | 降级的是回归范围，不是门禁强度 |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

CI 是被管对象也是审计源，口径如下：

1. **job / step 命名固定**：lint / unit-test / build / kind-up / crd-apply / state-machine-smoke / demo-triple / eval-regression，运行历史即审计日志，不删 run 记录。
2. **eval 报告归档**：回归报告作为 workflow 产物保留（文件名 `eval-report`），PR 描述链接到对应 run；报告中四项指标与 `aegis_eval_metric{metric=...}` 命名对齐（见 `docs/threat-model.md` 第 7 节）。
3. **kind-smoke 留痕**：三连演示每连的通过与否写入 run summary，失败步骤保留 kind 集群事件摘要不清理。
4. **预算留痕**：eval job 的 LLM 消耗计数随 run 归档，作为月度预算告警的对账依据。
5. **门禁变更即审计事件**：workflow 文件本身的修改单独走 PR（与版本钉死同纪律），run 历史可追溯门禁口径何时变严 / 变松。

## 7. 依赖方向与模块边界

- **谁调我**：PR / push 事件触发；`Makefile` 与 `scripts/*.sh` 是我执行的手段；PR 模板双必填是我消费的人工输入。
- **我调谁**：只调用仓库内 Makefile 目标与脚本（lint / test / build / kind 环境脚本），不内联第二条安装路径；eval 调用 `eval/` 的回归入口，不反向 import Go 内部包（落地方案附录 A）。
- **禁止依赖谁**：禁止访问真实集群 / 生产凭据（kind 唯一）；禁止 PowerShell / BAT 进入关键路径（附录 B-1）；禁止在 workflow 里写死 `deploy/versions.md` 已有的版本号（改版本 = 单独 PR）；禁止新增答不出"守哪条不变量 / 失败模式"的 workflow。

## 8. 测试策略与红队用例

CI 自证用例（纳入对应里程碑检查点）：

1. T0.1 配套：构造一个 domain 文件 import client-go 的违例，验证 depguard 在 ci.yml lint job 拦下（落地方案 T0.1 完成判据）。
2. T0.3：空架子 PR 触发 ci.yml 全绿（完成判据原文）。
3. T3.4：故意改坏一处 prompt，验证 eval-regression.yml 红（M3 验收③的 CI 化，设计文档 §7）。
4. I10 冒烟：kind-smoke 中杀 gatekeeper 进程，验证写路径阻塞 + 告警、读路径降级且标注（aegis-security-review I10 验收方法）。
5. R2 演示：构造目标被第三方修改场景，回滚拒绝并升级人工 + 附漂移 diff（T2.5 完成判据的脚本化）。
6. 红队检查点 #2 的五个专项（审批绕过 / 日志注入 / 越权 / 重放 / 预算绕过）中可脚本化的项，逐步纳入 kind-smoke，使 M2 之后的每一次 PR 都重放当年红队结论。
