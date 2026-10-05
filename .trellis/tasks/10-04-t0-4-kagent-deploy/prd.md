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

## Progress (2026-10-05)

kagent 0.10.3 经 OCI 部署完成：kagent-crds + kagent 两 chart，controller/ui/tools/kmcp/postgresql 全 Running。声明空 Agent `aegis-empty-probe`，controller `Accepted=True / Reconciled`（**被接管判据达成**；Ready=False 因占位 LLM key，非 T0.4 范围）。版本已钉 versions.env。附注：kagent 自带 `memories` CRD + postgres 存储（SessionRetentionDays/VectorEnabled），与记忆选型研究强相关。kagent 已接入 env-up.sh 默认组件序列（Phase 0 出口标准）。
