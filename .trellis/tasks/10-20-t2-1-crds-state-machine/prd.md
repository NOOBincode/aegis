# T2.1 ChangeRequest/GuardrailPolicy CRD + 状态机

## Goal

把闸门契约从文档落成 K8s 对象：`ChangeRequest` 承载一次写操作的全生命周期，
`GuardrailPolicy` 承载硬拒/配额/审批/熔断参数；controller 只做薄 reconcile，
状态迁移合法性由纯领域状态机决定。M2 第一条主线，后续 T2.3/T2.5/T2.6 都消费这两个契约。

## Requirements

1. `api/v1alpha1/changerequest_types.go`、`guardrailpolicy_types.go`：字段全量对齐设计文档 §5.1/§5.2；
   ChangeRequest 至少含 `idempotencyKey/target/operation/riskHint/approval/dryRunResult/inverseOperation/rollback/status`。
2. `crds/aegis.dev_changerequests.yaml`、`crds/aegis.dev_guardrailpolicies.yaml`：M2 起走
   controller-gen 生成（禁止长期手写）；本任务允许先落 manifest 但必须补生成链/CI diff 校验。
3. `internal/controller`：reconcile 薄层（读 CR → 调状态机/应用服务 → 写 status），
   不在 reconcile 里写业务 if-else；finalizer 保证清理不悬挂。
4. 状态机：Pending→DryRunning→AwaitingApproval→Executing→Verifying→Committed/RolledBack/Aborted/Rejected；
   非法迁移拒绝并留 reason；幂等键派生 CR 名，重复提交返回同一 CR。
5. 在途恢复：controller 重启后按集群实际状态续作/回滚/中止（I10/F14），不重复执行。
6. 测试：状态机纯单测覆盖迁移表/非法迁移/幂等；envtest 覆盖 reconcile/finalizer/重启恢复。

## Acceptance Criteria

- [x] 两个 CRD apply 成功；sample ChangeRequest/GuardrailPolicy 通过 schema；字段与 §5.1/§5.2 对账。
- [x] 状态迁移表单测全绿；非法迁移不落 status（表驱动 + abort/终态/非法状态用例）。
- [ ] 同一 idempotencyKey 重复提交只生成一个 CR、不触发第二次执行（`CRNameFromKey` 纯函数与单测已落；缺 API/controller 的端到端）。
- [x] controller-lite reconcile 已落：finalizer/初始化/在途仅留“核对实际状态”条件/终态删除清理；fake dynamic client 单测通过。
- [x] `make lint` 逐 module 0 issues；状态机 + reconciler 单测通过；controller-lite 进程可运行；controller-runtime/envtest 未接入。
- [ ] CI 增加 CRD generate-diff 门禁（M2 内完成；当前手写 manifest 已在 README 标注过渡）。

## Evidence

- `api/v1alpha1/changerequest_types.go` / `guardrailpolicy_types.go`：字段覆盖 §5.1/§5.2；
  `crds/aegis.dev_{changerequests,guardrailpolicies}.yaml` 已 apply，sample 通过 schema。
- `internal/controller/state.go`：状态迁移表、Abort、终态、RecoveryAction、`CRNameFromKey`；
  `state_test.go` 覆盖迁移表/非法迁移/恢复动作/幂等派生。
- 2026-10-19：CRD apply 成功；sample CR/GP apply 成功；lint 0 issues。
- 缺口（T2.1 未完成）：controller-runtime manager/leader election + envtest；当前 reconcile-lite 用
  client-go dynamic 实现并已单测 finalizer/在途/终态删除；CRD 仍是手写过渡（controller-gen 生成链待补）。

## Notes

- 设计出处：§4.2.5、§5.1/§5.2、ADR-003；依赖 M1 的 DecisionRecord/审计。
- 风险：controller-gen 未在仓库内固化；若本任务手写 manifest，必须在 PR 描述标注并建 T2.1-followup 补生成链。

- 2026-10-20：controller 接入 `Executor`；`--enable-writes` 才启用 ScaleExecutor（默认 fail-closed 只读）。实机 E2E：CR Pending + Deployment replicas=1 → controller 执行/验证 → replicas=2、CR Committed；R2 未审批不执行单测覆盖。

- 2026-10-20 全链路实测：gatekeeper MCP `request_change`（scale +1，R1 allow）→ CR Pending → controller（`--enable-writes`）→ ScaleExecutor → Deployment replicas 1→2 → CR `Committed`；随后清理。

- 2026-10-20 R2 审批 E2E：request_change +2 → CR AwaitingApproval（确认未执行）→ CLI approve（bob）→ status Approved → controller 执行 → replicas 3、CR Committed。证明审批门禁无绕过；修复 CR 创建时 status 丢失与 CLI `--cr`。
