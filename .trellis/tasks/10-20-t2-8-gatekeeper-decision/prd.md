# T2.8 gatekeeper 决策核心

## Goal

把 risk/rollback/policy/approval 串成唯一裁决点（纯领域）：同一请求只能得到
allow/requires-approval/reject，R3 即使有审批也拒绝；执行/代理/网络在 cmd 层。

## Requirements

1. `internal/gatekeeper.Evaluate(Input) Decision`：risk → 逆操作 → policy → approval。
2. R3/`hardDeny` 优先拒绝，不接受审批覆盖；R2/不可逆必须 Approved 才放行；
   R1 仅在逆操作/dry-run/无 owner/无注入且 policy 允许时可 AutoExecutable。
3. Reasons 可审计，不含 Secret/原日志；输入缺省/未知 fail-closed。
4. 组合单测覆盖 R1/R2/R3/不可逆/超配额/审批 pending→approved。
5. MCP 代理、mTLS、审批认证写、真实执行留 T2.8 后半/T2.11，本任务先领域闭环。

## Acceptance Criteria

- [x] R1 有逆操作+dry-run+policy 允许 → allow + AutoExecutable。
- [x] R2/不可逆 → RequiresApproval；Approved 前 Allow=false。
- [x] R3/保护命名空间 → Reject+HardDeny，即使 approval approved。
- [x] policy 配额/预算/熔断 reject；未知输入 fail-closed。
- [x] 组合单测通过；lint 0。
- [ ] MCP 代理/mTLS/执行器接入（后续）。

## Notes

- 设计出处：§4.2.2 gatekeeper、§4.2.1、I1/I5；M2 红队检查点 #2 后续覆盖。
