# T2.3 GuardrailPolicy 求值 + 服务端审批状态机

## Goal

把 GuardrailPolicy 从 YAML 契约落成确定性求值器，并把审批状态机放到服务端纯领域：
R3 硬拒/配额/速率/预算/熔断 + R2 强制人审；审批人身份必须绑定、自批拒绝、终态不可改。

## Requirements

1. `internal/policy`：纯 stdlib；输入策略 + 请求事实；输出 reject/requires-approval/
   force-readonly/reasons；保护命名空间与写 Secret 是代码级硬拒，不受策略放宽。
2. 配额（pods/nodes/namespaces）、每小时变更数、会话 tool/token 预算、失败率熔断
   全部在执行前求值；超限拒绝，不降级放行。
3. `internal/approval`：Pending→Approved/Denied；仅 Required 可审批；actor 为空/自批/
   已终态全部拒绝；审批人/时间由调用方注入，CLI 不存判定逻辑。
4. OPA/Rego 适配器在 `internal/adapters/opa`（M2 T2.3 后半）；本任务先纯领域闭环。
5. 表驱动 adversarial 单测：硬拒绕过、R2 自批、--yes 变体、配额/预算/熔断、未知级别 fail-closed。
6. `make lint` 0；`make test -race` 通过。

## Acceptance Criteria

- [x] policy 纯领域：hardDeny/保护命名空间/写 Secret/配额/速率/预算/熔断/R2 审批；未知级别人审。
- [x] approval 纯领域：Required/actor/自批/终态校验；approve/deny 单测通过。
- [x] `internal/policy`、`internal/approval` 单测全绿；lint 0。
- [x] OPA/Rego 适配器 `internal/adapters/opa` 已落：HTTP /v1/data、bool/{allow,reason}、超时/非 2xx fail-closed；httptest 覆盖。
- [ ] CLI approve/deny 无绕过（T2.4）。
- [ ] gatekeeper 执行路径接入（T2.8）。

## Notes

- 设计出处：§4.2.2（risk/approval/log-sanitizer/circuit-breaker）、§5.2；R16/R23。
- 关键红线：策略超时/错误必须拒绝写（fail-closed），不得“先放行再补审”。
