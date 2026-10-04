# T0.5 观测基座

## Goal

`deploy/observability/`：OTel collector + Langfuse（自托管，先 docker-compose 从简，M2 后可迁集群内）。

## Requirements

- docker-compose 编排：OTel collector + Langfuse（含其依赖）
- OTel collector 配置：traces→Langfuse、metrics→Prometheus
- 版本钉死入 `deploy/versions.md`
- 与闸门遥测口径对齐（trace span 命名见 docs/threat-model.md §7）

## Acceptance Criteria

- [ ] docker-compose 一键起 OTel collector + Langfuse
- [ ] 一次手测 HTTP 调用的 trace 在 Langfuse 可见（**依赖 docker**）
- [ ] 版本钉死入 `deploy/versions.md`

## Blocker

**docker 不可用**。compose 文件可先落盘，"trace 可见"验证需 docker。

## Notes

- 先 docker-compose 从简；集群内迁移留到 M2 后。
