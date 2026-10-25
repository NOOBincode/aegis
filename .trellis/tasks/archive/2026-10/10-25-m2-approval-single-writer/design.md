# M2 审批单写者设计

## 决策

- 采用独立 ApprovalDecision CRD，保留审批与执行进程边界；controller 是唯一 ChangeRequest status writer。
- 只新增 CRD，无 ChangeRequest 数据迁移。保持 controller replicas=1。
- journal、Unknown、SSA 不在本任务；envtest 不作为本轮通过项。

## 数据流

```text
CLI → gatekeeper-approval → approval.Decide → 创建 ApprovalDecision
                                                ↓ watch
                                   controller 读取并核对 CR UID
                                                ↓
                         唯一写入 status.approval / state → 执行或拒绝
```

Submit 创建 CR 时不写 status；controller 根据 `spec.approval.required` 初始化 Pending 或 AwaitingApproval。

## 决定契约

- spec：changeRequestRef(namespace/name/uid)、approver、decision、channel、decidedAt、evidenceHash、note。
- 名称：`ad-` + CR UID SHA256 前 16 hex，防止并发双决定。
- evidenceHash：绑定 CR UID/spec/note；note 脱敏限长。
- `AlreadyExists` 只在 ref/approver/decision/evidenceHash 完全一致时幂等成功，否则 409。
- label `aegis.dev/change-request` 用于 watch/list；ownerReference 用于随 CR 垃圾回收；CRD spec 不可变。

## controller

- 初始化 status.approval，并按 R1/R2 选择初始 state。
- 仅对 AwaitingApproval 消费决定；核对 UID，终态审批不覆盖。
- Manager 同时 watch CR 与决定，决定事件按标签映射为 `namespace/crName` 入队，两个 informer 都完成 cache sync。

## RBAC

- gatekeeper：ChangeRequest get/list/watch/create + ApprovalDecision get/list/watch/create；移除 `changerequests/status`。
- controller：ChangeRequest/status 既有权限 + ApprovalDecision get/list/watch。

## 验证

- 动态 fake 覆盖决定创建、幂等/冲突、R1/R2 初始化、Approved/Denied、UID 不匹配、watch 入队。
- 原 HMAC、自批、证书与执行链测试全部重跑；再跑全仓 lint/test-unit/build。
