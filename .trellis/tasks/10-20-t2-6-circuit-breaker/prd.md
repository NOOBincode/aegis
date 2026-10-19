# T2.6 熔断器

## Goal

M2 的运行时熔断：窗口内失败率超阈值 → Open（拒绝写）→ 冷却后 HalfOpen 单探针
→ 成功闭合/失败重开；人工 Reset 显式留痕（F7）。

## Requirements

1. `internal/breaker.Circuit`：Closed/Open/HalfOpen；失败率/最小请求数/窗口/冷却。
2. Open 期间一律拒绝；HalfOpen 同时只允许一个探针。
3. Reset 人工复位；窗口滑出后旧失败不继续触发。
4. 并发安全；纯 stdlib，时间注入便于确定性单测。
5. 与 policy 的 failureRate/熔断 action 衔接：闸门执行路径读 Circuit 状态；本任务先领域。

## Acceptance Criteria

- [x] 失败率达阈值且请求数达最小 → Open；冷却期拒绝。
- [x] 冷却后 HalfOpen 单探针；成功 Close、失败重开。
- [x] 窗口滑出/Reset/并发测试通过；lint 0。
- [ ] 与 gatekeeper 执行路径/告警动作接线（T2.8 后半）。

## Notes

- 设计出处：§4.2.2 circuit-breaker；失败率 >30% / 单位时间变更超限 → 降级只读。
