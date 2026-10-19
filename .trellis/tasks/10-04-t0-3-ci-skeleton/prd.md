# T0.3 CI 骨架

## Goal

GitHub Actions 门禁骨架，空架子 PR 触发全绿。CI 是门禁而非形式（aegis-ci）。

## Requirements

- `ci.yml`（始终必过）：lint → unit test（-race）→ build 三 job 串行
- `kind-smoke.yml`（M2 起必过）：kind 起集群 → apply crds → 状态机冒烟；骨架期先以"起集群 + 节点就绪"空跑
- 编写规则：bash / ubuntu-latest / 最小权限 / concurrency 分组 / action 钉版本 / Go 版本从 go.work 读

## Acceptance Criteria

- [x] `ci.yml` 三 job（lint / unit-test / build）落盘、串行、空架子全绿（YAML 校验通过，本地 make lint/build 绿）
- [x] `kind-smoke.yml` 落盘（**依赖 T0.2**：共用 scripts/ 安装脚本，禁止第二条安装路径）

## Status / Evidence

**已完成落盘（2026-10-19）。** `ci.yml`、`kind-smoke.yml` 均在仓库；kind-smoke 复用 `scripts/install-tools.sh` + `env-up.sh --cluster-only`，未写第二份安装路径。远端 Actions 实跑结果需在推送分支后查看（本地无法代替 GitHub runner）。

## Notes

- eval-regression.yml 属 M3，不在本任务范围。

## Progress (2026-10-05)

- `ci.yml`（lint→unit-test→build 串行）已在此前落盘。
- 本次补齐：`scripts/install-tools.sh`（kind/kubectl/helm 钉版本、幂等，本地/CI 共用，满足 aegis-ci B-4）；`env-up.sh` 新增 `--cluster-only` 模式；`.github/workflows/kind-smoke.yml`（PR 触发：install-tools → env-up --cluster-only → env-down --force 幂等清理）。
- 已验：bash -n、install-tools 幂等 skip、env-up --cluster-only 前置 fail-fast(exit 2)、kind-smoke.yml YAML 合法。
- **待 GitHub 远端**：「空架子 PR 触发全绿」需仓库推送远端后才能实跑验证（当前无 remote）。
