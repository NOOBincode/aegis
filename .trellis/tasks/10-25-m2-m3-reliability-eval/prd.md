# M2/M3 可靠性与 eval 骨架

## Goal

按顺序完成两个独立交付：M2 的 ApprovalDecision 单写者，以及 M3 的 eval runner 最小骨架。

## Requirements

- 子任务一：`10-25-m2-approval-single-writer`，只处理审批与 ChangeRequest status 写入边界。
- 子任务二：`10-25-m3-eval-runner-skeleton`，只处理 T3.1 runner、场景校验、确定性评分与归档。
- 不宣称完整 M2/M3 验收，不混入 Unknown/SSA、完整 GitOps、20 场景、CI 回归门禁。
- 保留用户其他未提交修改；不提交、不推送；真实 kind 故障只记环境阻断。
- L2 及邻近安全边界的 AI 代码必须标注逐行人审要求。

## Acceptance Criteria

- [x] M2 子任务验收通过，gatekeeper 不再写 ChangeRequest status，controller 消费 ApprovalDecision。
- [x] M3 子任务验收通过，Python 单测与 Go 标准门禁通过。
- [x] 文档明确未覆盖的 envtest/race/真实集群与后续 T2/T3 项。
