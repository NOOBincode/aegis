# T0.6 文档初始化

## Goal

落盘 Phase 0 的档案层文档：ADR-001/002 + threat-model 骨架。

## Requirements

- `docs/adr/ADR-001.md`：转录设计文档附录 A（R1 直写 vs R2 GitOps PR 分界）
- `docs/adr/ADR-002.md`：转录设计文档附录 A（kagent 二开 vs 自研薄运行时）
- `docs/threat-model.md` 骨架：资产 / 攻击面 / 攻击者画像三节（正文 M2 前补齐）

## Acceptance Criteria

- [x] ADR-001 落盘（五段式 + 五元组头 + 复评记录）
- [x] ADR-002 落盘（含 fork 边界与降级触发条件）
- [x] threat-model.md 骨架（含资产/攻击面/攻击者画像，且已补齐缓解映射与红队记录）
- [x] 纳入每周碎片维护（docs/adr/README.md 复评纪律）

## Status / Evidence

**已完成（本会话，2026-10-04）。**

- `docs/adr/ADR-001-r1-direct-write-vs-r2-gitops-pr.md`
- `docs/adr/ADR-002-kagent-fork-vs-thin-runtime.md`
- `docs/threat-model.md`（骨架已就位，且超出骨架：含缓解映射表 §5、红队记录 §6、观测口径 §7）
