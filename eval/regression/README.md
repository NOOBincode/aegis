# eval/regression — CI 回归门禁（I8）

> 所属层：L4 + CI 门禁 ｜ 里程碑：M3（落地方案 T3.4）｜ 设计文档出处：§4.6 回归门禁、§2 I8、§9 LLM 成本行 ｜ 关联不变量：I8 ｜ 关联失败模式：F10

## 1. 设计初衷

- I8 原文："任何 prompt/模型/工具链变更，不过 eval 回归门禁不得发布"（§2）；§4.6："prompt/模型/工具链任何变更触发全量场景回归，指标回退即阻断发布"。
- M3 验收③ 就是本门禁的验收：改一处 prompt 导致指标回退，CI 阻断（§7 M3）。
- 门禁的双重属性：对 Agent 变更它是**刹车**（防裸奔），对迭代效率它是**信道**（中小模型 + 预算上限让回归跑得动，§9）。

## 2. 职责与任务清单

1. 触发判定：PR 变更面与门禁触发清单的匹配（path 过滤，§4）。
2. 组织全量回归：调用 `eval/README.md` runner 跑 20 场景（held-out 按月度节奏，见 `eval/scenarios/README.md` §4）。
3. 基线对比与回退判定：消费 result.json 的 comparison，按阻断规则输出 verdict。
4. 成本治理：中小模型通道 + 会话预算上限 + 月度预算告警（§9；aegis-ci 规范）。
5. 豁免流程：白名单标注、理由、审批、基准重定的规则化（§4）。
6. 与 `.github/workflows/` 的对接约定（§7 对接节）。

## 3. 技术选型与开源包

- **CI**：GitHub Actions（aegis-ci 规范：workflow `eval-regression.yml`，M3 起必过；`runs-on: ubuntu-latest`；每步显式 `shell: bash`；同 PR `concurrency` 分组 + `cancel-in-progress: true`；第三方 action 钉 major tag 同行注释确切版本）。
- **集群**：kind 唯一（aegis-ci 红线：CI 禁访问真实集群/生产凭据）；环境脚本与 `deploy/kind/` 共用同一份。
- **模型通道**：回归专用中小模型（OpenAI 兼容抽象，公网或私域 vLLM 以预算优先）；模型与组件版本以 `deploy/versions.md` 钉死为准。
- **Secrets**：LLM API key 走 GitHub Secrets（`permissions:` 最小化，默认 contents: read）。

## 4. 具体设计（不写代码）

**触发条件（path 过滤清单）**：
- **主触发**：`agent/`（prompt/模型配置/上下文工程变更）、`mcp-servers/`（工具链变更）、`eval/` 内 runner/grader 变更。
- **连带触发**：`policies/`（策略变更改变 Agent 可执行集合与写路径行为面）、`crds/`、`internal/risk` 等闸门规则变更——表面是确定性层，但其变更改变 Agent 行为边界，纳入主触发。
- **兜底**：每里程碑红队检查点强制一次全量回归（防 path 过滤被绕过，§8）；触发清单本身的变更须评审（diff 天然可见）。

**全量 vs 增量回归策略（成本权衡）**：
- **现状（20 场景）**：一律全量。20 场景 × 中小模型的成本在 §9 预算框架内（会话预算上限 + 月度预算告警）；全量换来口径稳定与实现简单，无需增量复杂度。
- **增量预留**：场景数显著膨胀或单场景成本上升后启用——按类别裁剪（如 prompt 微调先跑资源类+配置类冒烟，全量夜间跑）；启用条件与裁剪规则以本文件修订记录为准，**不预实现**（避免无验证环境的设计，附录 C 纪律）。

**回归执行流程**（workflow job 语义，编号步骤）：
1. 检出 PR 与目标分支求 diff，按触发清单判定；未命中且非兜底 run → job 跳过（结论 neutral，不是 pass）。
2. 环境准备：执行 `deploy/kind/` 共用脚本（kind + Chaos Mesh + 演示负载）。
3. 全量回归：以钉死模型通道与当前 prompt 版本运行 runner，产出 result.json（runner 契约见 `eval/README.md` §4）。
4. 基线对比：读取基线 ref（基线登记存于 git 内）计算 delta。
5. 判定：按阻断规则输出 pass/fail；指标明细（基线值/本次值/delta）写 job summary（PR 人读）。
6. 归档：run 目录上传 artifact；run_id 写入 PR 描述——落地方案 R-3："任何 prompt/模型/工具链变更的 PR 必须附回归报告链接"。

**指标回退阻断规则**：
- 任一一等指标（诊断准确率/归因正确率/误修复率/MTTR）较基线回退即 fail。容忍带初始为 0（§4.6"指标回退即阻断"字面执行）；抖动处理 = 同 PR 重跑确认，两次结论一致才定案，仍抖动按 fail 处理——宁可错杀：F10 环境下宽容忍带等于给指标虚高开后门。
- 误修复率方向单独说明：误动作 >0 即回退——该指标期望恒 0，任何上升都是行为级问题（F11）。
- **基线更新规则**：仅当全指标 ≥ 当前基线的 run 可经 PR 升格为新基线；基线变更 PR 须附 run_id 与对比报告。
- **白名单豁免流程**：PR 标注豁免标签 + 理由（如"纯文档变更误触发"）+ 维护者批准；豁免记录随 run 归档。**任何指标回退不得豁免**——豁免只适用于"不该触发而触发"的情形。

## 5. 错误处理与失效语义

| 错误类别 | 检测手段 | 处置策略 | 姿态与理由 |
| --- | --- | --- | --- |
| 基础设施失败（kind/Chaos Mesh 不绿） | runner 阶段探针（`eval/README.md` §5） | job 重试一次；仍失败标 infrastructure_failure | fail-closed：门禁只有 pass/fail 两态，infra 失败按 fail 并要求人工重跑——不给"环境坏了就当过"的出口 |
| LLM 预算耗尽 | 会话预算计数 + 月度预算告警（§9） | 当次 run 中止，job fail | fail-closed；预算治理是 §9 缓解措施的执行点 |
| 基线缺失（登记被清空/首次运行） | 步骤 4 校验 | 阻断并告警；首次运行例外——建立基线不阻断 | fail-closed：无基线则回退判定不可做，禁止"无基线即放行" |
| 报告缺失/schema 不符/评分器异常 | result.json schema 校验 | job fail | fail-closed：防半份报告冒充通过（`eval/README.md` §5 同口径） |
| 重跑抖动 | 两次重跑结论比对 | 见 §4 阻断规则 | 宁可错杀（F10 环境下的方向性选择） |

## 6. 可观测性（日志 / 指标 / Trace / 审计）

**日志/输出字段**：workflow job 名与步骤名固定（prepare/run/compare/verdict）；结构化输出 `eval.gate_trigger`（命中的触发清单项）、`eval.gate_baseline_ref`、`eval.gate_delta`（per 指标：baseline/current/delta）、`eval.gate_verdict`、`eval.gate_exemption`（豁免记录，如有）。

**指标**（CI 侧以日志与 artifact 承载）：`eval_gate_total{verdict="pass|fail|infra_failure"}`；`eval_gate_duration_minutes`；`eval_llm_budget_consumed_tokens{run_id}`（§9 月度预算告警的数据源）。

**审计留痕点**：每次门禁 run 的 PR 号、commit、run_id、基线 ref、verdict、豁免记录归档（artifact + PR 描述链接）；对应 R-3 的报告链接纪律。月度预算告警的消费方是开发者（§9），数据源在本门禁的 token 统计。

## 7. 依赖方向与模块边界

- **谁调我**：GitHub Actions 事件（push/PR 的 path 过滤）；维护者手动触发（workflow_dispatch——兜底全量、基线重定、门禁自身演练）。
- **我调谁**：`eval/README.md` runner（唯一执行入口，门禁不内嵌评测逻辑）；`deploy/kind/` 环境脚本（共用同一份，aegis-ci 跨平台规则）；`deploy/versions.md` 钉死版本；Go 构建链（工具链变更时先 build 再回归）。
- **禁止依赖谁**：禁止真实集群/生产凭据（aegis-ci 红线）；禁止绕过 runner 手工拼报告（防"门禁自证"）；禁止在 workflow 内联评测业务逻辑——判定逻辑全在 eval/ 世界内，workflow 只做编排（CI 是门禁不是实现）。
- **与五铁律的关系**：本目录是 Python 世界与 Go 世界之间的编排层，只产生调用不产生 import（落地方案附录 A 的 eval/ 定位）。

## 8. 测试策略与红队用例

- **门禁自测**（T3.4 完成判据 = M3 验收③）：故意改坏一处 prompt 提 PR，必须 CI 红；恢复后 CI 绿。
- **误触发演练**：纯文档 PR 不触发 runner（neutral）；`policies/` 与 `crds/` 变更必须触发（§4 连带触发验证）。
- 红队用例：
  1. **绕过攻防**：尝试把 prompt 变更藏进不被 path 过滤命中的文件——缓解 = 触发清单评审 + 里程碑强制全量兜底；过滤清单的 diff 纳入 PR 评审视线。
  2. **豁免滥用攻防**：构造"指标回退但贴豁免标签"的 PR，验证流程拒绝（豁免不适用指标回退，§4）。
  3. **半份报告攻防**：模拟 runner 中途预算耗尽，验证 job fail 且无基线更新（联动 `eval/README.md` §8.4）。
  4. **基线污染攻防**：尝试用指标全降的 run 升格基线，验证基线更新规则拒绝（§4）。
  5. **artifact 重放攻防**：同名 run_id 重复上传的冲突处理——以首个为准并告警，防报告替换。
