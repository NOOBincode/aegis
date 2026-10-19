# T2.2 确定性 risk-classifier（R0–R3）

## Goal

`internal/risk` 落 R0–R3 判定表：资源类型×动词×作用域 + 安全证据修饰，输出级别/硬拒/原因/
是否可自动执行。LLM 的 risk_hint 只作参考，最终级别取高者；R3 无审批入口。

## Requirements

1. 纯 stdlib、无 k8s/OPA/MCP import（depguard）。
2. R3 代码级硬拒：namespace 删除/修改、保护命名空间写、写 Secret、改 RBAC、
   shell/exec/hostPath/privileged、Secret 明文读取；读操作不受保护命名空间限制。
3. R1 白名单：scale ±1、单 Pod restart、单节点 cordon/uncordon；
   其他写/未知工具 fail-closed R2。
4. 上下文修饰只升不降：无逆操作、dry-run 未过、owner 非空、I4 注入命中、
   配额/影响面超限 → 至少 R2；读操作跳过这些修饰。
5. `AutoExecutable` 仅在 R1 + 逆操作/dry-run 通过 + 无 owner + 无注入时为 true。
6. 表驱动单测覆盖上述边界；`make lint` 0。

## Acceptance Criteria

- [x] 读工具/动词 R0；保护命名空间读放行、写 R3。
- [x] scale ±1 可自动、步长超限 R2；无逆操作/dry-run 未过/owner/注入升 R2。
- [x] R3 硬拒用例全过；未知工具/动词 fail-closed R2。
- [x] `make lint` 0 issues；`go test ./internal/risk/...` 通过。
- [ ] 接入 gatekeeper 的调用点（T2.8）与 Policy 配额输入（T2.3）——跨任务，后续验收。

## Notes

- 设计出处：§4.2.2、§4.2.2 表格；风险级别最终取 max(确定性, LLM hint)。
- 误判方向：宁可 R2 人审，不把未知写降到 R1；R3 不允许策略放宽。
