# 执行记录：M1 阻塞修复（review 1-4）

## 提交

- `98314f8` fix(m1): 解锁环境并交付 k8s-read/metrics-server/只读 RBAC（review 1-4）
- `357dbe9` chore(trellis): 修 M1 阻塞任务与 T1.3/T1.4/T1.8/T1.10 排期
- `d23ddc7` chore: record journal（M1 阻塞修复）

## 变更摘要

| 方向 | 文件 |
| --- | --- |
| 环境脚本 | `scripts/env-up.sh`（inotify 预检/版本严格校验/kagent-controller/Chaos replicaCount/健康摘要/kagent-tools+demo agents 关闭/metrics-server）、`scripts/lib/common.sh`（版本比对/fetch_chart+sha256/未就绪 Pod 检查） |
| MCP 交付 | `mcp-servers/k8s-read/Dockerfile`、`deploy/mcp-servers/k8s-read-rbac.yaml`、`k8s-read-mcpserver.yaml`、`scan-rbac.yaml`、`scripts/build-mcp-images.sh`、`scripts/mcp-up.sh`、`Makefile`（mcp-images/mcp-up/scan module） |
| 安全 | `deploy/security/kagent-mcp-networkpolicy.yaml`、`docs/threat-model.md`（AS-15）、env-up 默认关闭 kagent-tools；集群侧删除 `kagent-tools-cluster-admin-rolebinding` |
| scan 正确性 | `internal/adapters/k8s/scanprovider.go`（触顶盲点）、`internal/scan/engine.go`（1h recency）、`policy.go`（仅 Deployment）、`engine_recency_test.go`、`policy_kind_test.go`、`scanprovider_truncation_test.go` |
| I4 | `internal/sanitize/sanitize.go` + 测试；`internal/mcpserver/middleware.go` 接入并转义尖括号 |
| k8s-read 契约 | `reader/reader.go`（512KiB 日志上限/错误传播）、`tools*.go`（since/cursor/schema 错误分类）、README 对齐 |
| CI/文档 | 逐 module lint + 修 7 个 lint 问题；设计文档 I11/R30；versions.md/env；deploy/kind README |

## 验证

- `bash -n` 四个脚本通过；`env-up.sh --cluster-only` 在当前 128 inotify 宿主上退出码 2，提示 sysctl 修复命令。
- `make lint`（逐 module）12 个 module 0 issues。
- 根与全部 module `go test ./...` 通过（本机无 gcc，`make test` 明确提示 `-race` 需要 cgo；未伪造 race 结果）。
- kind 实机：`k8s-read` 0.2.0 `tools/list` 8 工具；`get_pods` 返回真实对象；`get_resource_metrics` 返回节点指标；`scan_cluster` 返回 findings；`kubectl auth can-i` 对 k8s-read/scan 的只读/无 secret 断言通过。
- kagent controller/Agent `aegis-diagnoser`/metrics-server Ready；kagent-tools ClusterRoleBinding 与 Deployment 均不存在；NetworkPolicy 已 apply（kindnet 不强制，脚本说明）。
- 环境恢复方式：先 scale 掉 demo agents/kagent-tools、Chaos 3→1 释放 inotify 实例，重启 kube-proxy/CoreDNS/kagent-controller；根治仍要求宿主抬 `fs.inotify.max_user_instances`（未在无 root 会话内执行）。

## 未做

- T1.3/T1.4/T1.8/T1.10 仅建任务与 PRD，未实现；M1 验收（OOMKilled Agent 报告 + replay）仍未跑。
- win-proxy/LLM 通道未改（用户确认旧产物，后续单独讨论）；Agent Ready 不代表模型可调用。
