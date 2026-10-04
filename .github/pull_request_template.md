<!--
  aegis PR 模板 —— 双必填，缺一不合并。
  事实源：.agents/skills/aegis-ci/SKILL.md（分支/提交/PR 节）、.github/workflows/README.md §4。
  1. 不变量检查单：设计文档 §2 的 I1–I10 逐项对照，标 N/A 或说明（纪律 R-4：违反任一项不合入）。
  2. AI 产出声明：AI 生成占比 + 人审人；L2 安全包要求 100% 逐行人审（纪律 R-2）。
  提交信息约定：<type>(<scope>): <summary>，type ∈ feat / fix / docs / refactor / test / chore。
-->

## 变更说明

<!-- 做了什么、为什么。关联落地方案任务号（T 编号）与 issue；形态级变更须附 docs/adr/ 链接。 -->

关联任务 / ADR / issue：

## ① 不变量检查单（必填，逐项标 N/A 或说明）

| 不变量 | 内容 | 相关? | 说明（N/A 或如何满足） |
| --- | --- | --- | --- |
| I1 | 确定性闸门：写操作放行/拒绝由代码与策略决定，不由 LLM 输出决定 | | |
| I2 | LLM 不进热路径：调度/熔断/配额均为确定性逻辑 | | |
| I3 | 工具即权限边界：窄接口、强 schema 校验、显式标注只读/写与幂等 | | |
| I4 | 读路径也是攻击面：集群返回数据标记不可信并包裹隔离，衍生动作自动升级审批 | | |
| I5 | 一切写操作可回滚：强制逆操作 + dry-run 验证，无法生成逆操作升 R2 | | |
| I6 | 决策可回放：输入快照/推理链/工具调用/审批记录/执行结果全量留痕 | | |
| I7 | 爆炸半径配额：变更影响面（Pod/节点/命名空间数）硬上限，超限拒绝 | | |
| I8 | eval 先行：prompt/模型/工具链变更不过 eval 回归门禁不得发布 | | |
| I9 | 先归因后行动：状态变化先经确定性归因，Agent 不与控制器争字段所有权 | | |
| I10 | 失效姿态自明：写路径 fail-closed，在途变更靠 CR 状态机 + finalizer 恢复 | | |

## ② AI 产出声明（必填）

- AI 生成占比（约 %）：
- 人审人：
- 是否触及 L2 安全包（`internal/risk` / `policy` / `approval` / `rollback` / `breaker` / `authority` / `sanitize` / `audit` / `session` / `controller`）：是 / 否
  - 若是，确认已 100% 逐行人审（纪律 R-2）：是 / 否

## 自证与测试

<!-- 如何验证。涉及写工具 / 攻击面变化，须先登记 docs/threat-model.md 第 4、5 节再合入。 -->

- [ ] `make lint` 绿（含 depguard 依赖方向机检）
- [ ] `make test` 绿（`-race`）
- [ ] `make build` 绿
- [ ] 依赖方向未违规（五铁律，见 `.agents/skills/aegis-ddd-layout/SKILL.md`）
