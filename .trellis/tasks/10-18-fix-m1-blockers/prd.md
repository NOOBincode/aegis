# 修 M1 阻塞项：环境/交付/安全/CI

## Goal

修复 review 确认的 M1 阻塞项，让"kagent + 自研 MCP 工具链"在 kind 上真实可用、安全姿态可解释、CI 门禁覆盖新增 module。M1 剩余功能任务（T1.3/T1.4/T1.8/T1.10）不在本任务内实现，只建立任务与排期，避免把"代码骨架/环境恢复"误报成 M1 验收完成。

## Requirements

1. **环境**：`scripts/env-up.sh` 前置检查宿主 inotify（<512 instances 直接拒绝并给 sysctl 命令）；严格校验 kind/kubectl/helm 版本；修正 kagent 就绪检查的 deployment 名（`kagent-controller`）与 Chaos `controllerManager.replicaCount`；健康摘要检查 kube-system/chaos/kagent 等全部 Pod；kagent 默认关闭 `kagent-tools`（cluster-admin + 写工具）与 demo agents。
2. **交付**：k8s-read 增加可复现 Dockerfile；镜像 tag 升 0.2.0；MCPServer 显式 `serviceAccountName`；新增只读 RBAC（pods/events/nodes/pods-log/deployments/metrics.k8s.io，无 secrets/写动词）；metrics-server 进版本表并由 env-up 安装；新增 `make mcp-images`/`make mcp-up`；集群内 `tools/list` 8 工具 + `get_pods`/`get_resource_metrics`/`scan_cluster` 实调通过。
3. **安全**：关闭/移除 `kagent-tools` cluster-admin 暴露；新增 kagent MCP NetworkPolicy（本 kind 实测强制：default 阻断、kagent 放行）；scan RBAC 去掉无用 `watch`；威胁模型登记 AS-15；`k8s-read` 日志加 512KiB 上限与读流错误处理。
4. **代码/CI**：scan 采集触顶写 `partial_reads`；事件佐证加 1h recency（SystemOOM/Unhealthy 陈旧事件不参与归因）；B 层规则只允许 Deployment（不支持 kind 加载期拒绝）；新增 `internal/sanitize` 包裹库并转义尖括号，防 I4 分隔符伪造；k8s-read `get_events since`/cursor/错误分类修正；`Makefile` 补 `mcp-servers/scan`、逐 module lint、`test-integration`；修复所有 module 的 lint 问题。
5. **文档/流程**：正式设计文档补 I11 与 R30（规则腐烂），scan spec 引用同步；k8s-read README 与实现对齐；版本表修正 kubectl skew、metrics-server、Chaos/kagent 安全默认；本任务 PRD/context 落盘；M1 剩余 T1.3/T1.4/T1.8/T1.10 建立任务与验收口径。

## Acceptance Criteria

- [ ] `bash -n scripts/env-up.sh scripts/mcp-up.sh scripts/build-mcp-images.sh scripts/lib/common.sh` 通过；`scripts/env-up.sh` 在低 inotify 宿主上以退出码 2 拒绝并打印修复命令。
- [ ] `make lint` 逐 module 0 issues；各 module `go test ./...` 通过（本地无 cgo 时 `make test` 给出明确提示）。
- [ ] `aegis/k8s-read:0.2.0` 在 kind 内 `tools/list` 出现 8 工具，`get_pods` 返回真实对象；`get_resource_metrics` 在 metrics-server 就绪后返回节点数值。
- [ ] `kubectl -n kagent get clusterrolebinding kagent-tools-cluster-admin-rolebinding` NotFound；kagent-tools deployment 不存在/0 副本。
- [ ] `kubectl auth can-i list pods` / `get secrets` / `get pods/log` 对 `kagent:k8s-read` 分别为 yes/no/yes；对 `kagent:scan` 为 yes/no/no。
- [ ] `scan_cluster` 经 MCP 调用返回 findings + `partial_reads` 结构；触顶/陈旧事件用例有单测。
- [x] `deploy/security/kagent-mcp-networkpolicy.yaml` 存在且本 kind 实测强制（default 阻断、kagent 放行；换 CNI 用 security-smoke 复测）。
- [ ] 正式设计文档有 I11、R30；scan spec 不再引用不存在的 R17（规则腐烂）。
- [ ] M1 剩余 T1.3/T1.4/T1.8/T1.10 有独立任务与 PRD/验收口径；本任务不宣称 M1 验收通过。

## Notes

- 本轮不改 win-proxy/LLM 通道（用户确认是上阶段旧产物，后续单独讨论）；Agent `aegis-diagnoser` 已 Ready，但 LLM 端点未验证。
- 本任务的人类可复核证据：`mcp-servers/k8s-read` 集群内 `tools/list`、`get_pods`、`get_resource_metrics` 响应；`kubectl auth can-i` 输出；`make lint` 逐 module 输出。
