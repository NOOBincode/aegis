# T1.13 scan server kagent 集成（部署+RBAC+发现+工具调用端到端）

## Goal

把 scan server 部署进 kind 并注册为 kagent `MCPServer` CR（含只读 RBAC），让 kagent 发现 `scan_cluster`；在**工具调用层**端到端验证（经 kagent/MCP 对真实集群注入 OOMKilled 命中根因）。这是 T1.6（L3 运行时接入）的 scan 侧落地，打通 Agent→Tier-0 规则层的运行时发现与调用。

## Requirements

**范围**
- scan server 容器镜像：`mcp-servers/scan/Dockerfile`（多阶段构建，CGO_ENABLED=0 静态）+ 版本钉死（对齐 deploy/versions.md）+ kind load 注入。
- RBAC：ServiceAccount `scan`（kagent ns）+ ClusterRole（只读 list/watch/get pods、deployments、nodes、events）+ ClusterRoleBinding。**最小权限（I3）**：只读、不涉 secret 明文/写。
- `deploy/mcp-servers/scan-mcpserver.yaml`：MCPServer CR（`serviceAccountName: scan`、port 8081、httpTransport /mcp），参照 k8s-read 模式。
- 发现验证：`mcpserver.kagent.dev/scan` status True，kagent 能 tools/list 出 scan_cluster。
- 端到端（工具调用层）：对真实集群注入 OOMKilled，经 MCP 调 scan_cluster 命中 OOM_KILLED。

**环境约束（明确）**
- **LLM 端到端 blocked**：OpenAI 不可达（网络受限），LLM 驱动的完整 Agent 会话本期不验证，标注为待私域 vLLM 通道（设计文档 §3.3 双通道）。本期验证到工具调用层。

**Non-Goals**
- 不做 LLM 驱动的 Agent 自主决策编排（受网络限制）。
- 不动 k8s-read（T1.2 在制品）。

## Acceptance Criteria

- [ ] scan 镜像构建 + kind load 成功；`kubectl get pod -n kagent -l app=scan` Running。
- [ ] RBAC 最小权限：scan SA 只能只读 list pods/deployments/nodes/events（`kubectl auth can-i` 验证；读 secret 被拒）。
- [ ] MCPServer CR apply 后 `mcpserver.kagent.dev/scan` status True，kagent 发现 scan_cluster。
- [ ] 端到端：注入 OOMKilled → 经 MCP 调 scan_cluster 命中 OOM_KILLED（含 Pod 名）。
- [ ] LLM 端到端在任务文档标注 blocked + 私域 vLLM 接缝。

## Notes

- 复杂任务：design.md + implement.md 齐备后 start。
- 参照 `deploy/mcp-servers/k8s-read-mcpserver.yaml` 的 MCPServer 模式。
