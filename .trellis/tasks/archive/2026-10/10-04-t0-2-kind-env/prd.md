# T0.2 本地环境一键脚本（kind）

## Goal

`make up` 一键起本地环境，从零到全绿、删集群重来可复现。这是 Phase 0 出口的钥匙，也是 T0.3 kind-smoke 与 T0.4 kagent 的前置。

## Requirements

- `scripts/env-up.sh` / `scripts/env-down.sh`：幂等可重入；env-down 逆序销毁
- `deploy/kind/`：kind 集群配置 + Chaos Mesh + ArgoCD + Prometheus 安装脚本
- 版本钉死：kind/kubectl/helm/Chaos Mesh/ArgoCD/Prometheus 版本写入 `deploy/versions.md`（W1 核验后逐行钉死）
- runsc 冒烟（落地方案 §9：W2 出口自检，ptrace 平台兜底 / KVM 视嵌套虚拟化）
- 安装脚本在 CI 与 WSL2 共用同一份（aegis-ci 红线，不写第二份）
- Makefile `up`/`down` 已预留接线，接通即可

## Acceptance Criteria

- [ ] `make up` 从零到全绿（kind + Chaos Mesh + ArgoCD + Prometheus）
- [ ] 删集群重来可复现
- [ ] runsc 冒烟 Pod 跑通（ptrace 兜底）
- [ ] `make down` 逆序清理、幂等

## Blocker

**docker 守护进程不可用**（kind 的后端）。本机有 `/dev/kvm`、kind v0.24.0、kubectl、helm，但无 docker/podman。脚本可先落盘，实机验证需 docker。

## Notes

- 先写脚本与清单（纯文件），验证标记为 pending-docker。
- 版本号一律以 `deploy/versions.md` 为单一事实源，不在脚本里写死第二份。

## Progress (2026-10-05)

已落盘并本地验证：`scripts/lib/common.sh`（骨架四要素）、`scripts/env-up.sh`、`scripts/env-down.sh`、`scripts/runsc-smoke.sh`、`deploy/kind/kind-config.yaml`、`deploy/versions.env`、`deploy/versions.md` 首轮钉死。
本地已验：bash -n 语法、env-down 幂等 noop、env-up 前置 fail-fast（docker 不在时 exit 2 + 结构化日志）、`make up` 正确接线。
**待 docker**:`make up` 从零到全绿 + runsc 冒烟（helm 仓库 prometheus/argo 当前网络不可达，可能需镜像源）。
