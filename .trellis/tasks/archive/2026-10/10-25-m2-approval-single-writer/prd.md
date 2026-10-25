# M2 审批单写者

## Goal

新增不可变 ApprovalDecision，使 controller 成为 ChangeRequest `status` 的唯一写入者，消除 gatekeeper 初始状态与审批双写窗口。

## Requirements

- ApprovalDecision 为 additive CRD，不修改 ChangeRequest schema，不做离线数据迁移。
- gatekeeper Submit 只写 ChangeRequest spec；审批服务只创建 ApprovalDecision，不得调用 `UpdateStatus`。
- 每个 CR 的决定名由 UID 派生，最多一份；只有相同 ref/approver/decision/evidenceHash 才可幂等重试。
- controller 初始化 R1/R2 状态，监听并消费决定；Approved 继续执行，Denied 拒绝，UID 不匹配忽略，已有审批不覆盖。
- RBAC 收回 gatekeeper 的 `changerequests/status` 写权限，controller 只读 ApprovalDecision。
- 保留 HMAC、mTLS actor、自批拒绝与现有执行链；不修改无关脏文件。
- 不混入 Unknown/SSA/envtest、完整 GitOps 或多副本改造。

## Acceptance Criteria

- [x] 新 CRD 与 Go 契约存在，包含 ref/approver/decision/channel/decidedAt/evidenceHash/note，spec 不可变。
- [x] gatekeeper 无 `UpdateStatus` 路径，RBAC 无 status 写权限。
- [x] controller 初始状态、审批消费、UID 校验和 watch 入队有单测覆盖。
- [x] 原审批安全用例保持；`make test-unit`、`make build`、`make lint` 通过。
- [x] 最终报告标注 L2/安全边界 AI 代码需逐行人审，以及 race/envtest/真实集群未覆盖项。
