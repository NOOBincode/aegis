# T2.12 M2 红队检查点（纯领域对抗集）

## Goal

把 M2 的安全组件放进对抗变体里验证，防止重构后退化；先覆盖纯领域可测的绕过路径，
网络/MCP/真实客户端类留 T2.8/T2.11 集成红队。

## Requirements

1. `internal/securitytest` 只测试，不生产；覆盖 approval/risk/policy/gatekeeper/envelope/sanitizer。
2. 审批绕过：not-required、空 actor、自批、已终态。
3. 风险/策略：delete namespace、写 Secret、保护命名空间写、shell、Secret 读；读例外不误杀；
   配额/预算/熔断。
4. gatekeeper：R3 即使 Approved 也拒绝；不可逆不自动。
5. envelope：重放、篡改；sanitizer：指令/围栏逃逸命中、正常日志不误报。
6. 真实 MCP 重放/自写客户端/mTLS 绕过归 T2.8/T2.11 集成红队。

## Acceptance Criteria

- [x] 纯领域对抗集全部通过。
- [x] 覆盖审批/风险/策略/闸门/信封/注入六类。
- [ ] 网络层/MCP 重放/自写客户端/真实审批接口红队（T2.8/T2.11）。
- [ ] M2 三连演示与 held-out eval（M3）。

## Notes

- 设计出处：§10 红队记录、M2 准入门槛；本任务只先钉住纯领域回归。
