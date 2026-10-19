# T2.5 逆操作生成与互逆校验

## Goal

把 I5“写操作可回滚”落成确定性生成器：能证明可逆的操作生成逆操作，其余标记
不可逆并强制 R2；执行路径（T2.8）不得对不可逆操作自动放行。

## Requirements

1. `internal/rollback` 纯 stdlib；`Generate(Operation) Result`。
2. scale 必须提供 `from_replicas`/`replicas_from` 才能生成回退；缺目标/缺原值不可逆。
3. cordon 逆 uncordon；restart/delete/patch/apply/未知工具不可逆（fail-closed）。
4. `Validate(forward,inverse)` 校验目标/工具/必填字段的互逆性。
5. 单测覆盖 scale/cordon/不可逆/目标不匹配；lint 0。

## Acceptance Criteria

- [x] `Generate` 对 scale/cordon 生成合法逆操作；对不可逆操作返回 Reversible=false。
- [x] `Validate` 拒绝工具/目标不匹配。
- [x] 单测全绿；lint 0。
- [ ] 与 dry-run 执行/回滚路径集成（T2.8/T2.5 后续）。

## Notes

- 设计出处：§4.2.2 rollbacker、I5；无逆操作自动 R2。
