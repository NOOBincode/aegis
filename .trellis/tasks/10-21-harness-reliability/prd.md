# aegis-harness H1–H3

## Goal

把 `docs/harness-reliability.md` 的可靠性底座并入主线 M2.5–M3；当前只同步设计与测试 oracle，不阻塞 M2。

## Scope

1. H1：journal（六类事件、checksum/hash chain、fsync 失败 fail-closed）、fake 故障注入；
2. H2：`Unknown` 状态、preimage/desired/after、SSA（fieldManager=aegis, force=false）、envtest、kill-recovery 三分支；
3. H3：ApprovalDecision 单写者、controller actor/Stop/心跳、gatekeeper 审批停止写 status。

## Non-goals

- 万级集群 worker/共享会话存储；
- kagent session/checkpoint 管理；
- 替代 kagent 的通用 harness。

## Acceptance Criteria

- [x] journal 纯领域库：只追加、每行 fsync、checksum hash chain、Verify；单测通过（H1 部分）；
- [ ] journal 接入 controller 写路径 + fake 故障注入；恢复不读 journal；
- [ ] ChangeRequest `Unknown` 可进入/退出，绝不假成功；
- [ ] SSA + envtest 覆盖字段所有权冲突；L3 canonical bytes 一致；
- [ ] L4 kill controller 三阶段（dry-run 后/apply 后/verify 中）三分支各演示一次，零污染；
- [x] ApprovalDecision 落地后 controller 是唯一 status writer（2026-10-25，additive CRD + 单测 + RBAC）；
- [ ] 与 `aegis-test-matrix` L3/L4 门禁联动。

## Open Decisions

见 `docs/harness-reliability.md` §11：单写者迁移、journal 存储、CRD 迁移、envtest 可用性、多副本。
