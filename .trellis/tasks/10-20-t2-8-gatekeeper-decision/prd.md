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
- [x] `Service.Apply` 执行/验证/回滚编排通过 fake executor 单测；未审批不执行。
- [x] `gatekeeper.Service.Apply` 执行闭环：只有 Allow 才调 Executor；执行/验证失败逆序回滚；executor 未接线 fail-closed；fake executor 单测通过。
- [x] 真实 `ScaleExecutor`（k8s apps client）：dry-run → merge patch → Verify replicas → Rollback；fake clientset 单测通过。
- [ ] MCP 代理/mTLS/其他写工具 adapter（后续）。

## Notes

- 设计出处：§4.2.2 gatekeeper、§4.2.1、I1/I5；M2 红队检查点 #2 后续覆盖。

- 2026-10-20：`ChangeRequestService.Submit` 已落：幂等键派生 CR、R3 拒绝不落库、R2 落 AwaitingApproval、R1 落 Pending；fake dynamic 单测通过。

- 2026-10-20：gatekeeper `request_change` MCP 工具已落（cmd/gatekeeper/mcp.go）：Agent 只提交意图/幂等键，服务判定后落 ChangeRequest，不直接写集群；提交服务 fake dynamic 单测通过。

- 2026-10-20：gatekeeper MCP 支持 `ServeHTTPS` + mTLS 客户端证书；`request_change` 可经 HTTPS/mTLS 暴露。

- 2026-10-20：gatekeeper 打包为 `aegis/gatekeeper:0.1.0`，以 kagent MCPServer `kagent/gatekeeper` 部署（MCP-only；:8086）；实测 kagent Service/port-forward 可 `initialize`、`tools/list=request_change`，并成功创建 CR。审批端口在 MCP-only 部署中不启用（无 secret）；审批仍用本地/独立 secret 进程。

- 2026-10-21：in-cluster 全链路：kagent/gatekeeper MCPServer request_change（R2，bob）→ AwaitingApproval → 集群内 gatekeeper-approval Deployment（Secret volume，alice）→ Approved → 本地 controller（--enable-writes）执行 → Deployment 1→3、CR Committed。证明集群内审批与执行串通，无旁路。

- 2026-10-21：`deploy/agent/operator.yaml` 新增 `aegis-operator` Agent，唯一工具为 gatekeeper MCPServer 的 `request_change`；无直接写工具/集群凭证。Agent CR Accepted=True；因 `aegis-llm` Secret 未配置，Pod 未 Ready（预期）。
