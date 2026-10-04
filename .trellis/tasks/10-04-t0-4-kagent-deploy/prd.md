# T0.4 kagent 部署验证 + fork 边界

## Goal

在 kind 中部署钉死版本的 kagent，并把 fork 边界决策落盘。

## Requirements

- kagent 钉 release tag（禁止跟踪 main），版本写入 `deploy/versions.md`
- 部署清单 / 安装脚本（helm，复用 T0.2 环境）
- ADR-002：仅配置层二开、不改 controller 内核 + 降级触发条件（上游剧变或 fork 维护 >20h/月 → 自研薄运行时）

## Acceptance Criteria

- [x] `docs/adr/ADR-002` 落盘（本会话，转录设计文档附录 A）
- [ ] kind 中能声明一个空 Agent 并被其 controller 接管（**依赖 T0.2 集群**）
- [ ] kagent 版本钉死入 `deploy/versions.md`

## Status / Evidence

**部分完成。** ADR-002 已落盘。实机部署验证需 docker + kind 集群（T0.2）。

## Notes

- 叙事风险（红队 R8）：差异化叙事锁定 L2+L4 自研，ADR-002 保留自研薄运行时备选。
