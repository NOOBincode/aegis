# T1.13 scan server kagent 集成 — 技术设计

## 1. 部署拓扑

```
kind 集群
└── ns: kagent
    ├── MCPServer CR "scan"（kagent.dev/v1alpha1）
    │     spec.deployment: image=aegis/scan:0.1.0, port=8081, serviceAccountName=scan
    │     spec.httpTransport: path=/mcp, targetPort=8081
    │        │ kagent 部署容器 + 路由 /mcp 做工具发现
    │        ▼
    │     Pod scan-xxx（SA=scan，R0 只读）
    │        │ in-cluster clientset → apiserver（只读 list）
    │        ▼
    │     scan.Service → internal/scan 引擎 → findings
    ├── SA "scan" + ClusterRole（只读 pods/deployments/nodes/events）+ ClusterRoleBinding
    └── kagent 发现 scan_cluster → Agent tools 白名单
```

## 2. RBAC（最小权限，I3）

- SA `scan`（ns kagent）。
- ClusterRole `scan-read`：仅 `get/list/watch` 于 `pods`、`deployments`、`nodes`、`events`（核心 + apps API group）。**不包 secret/configmap 明文、不含任何写动词**。
- ClusterRoleBinding `scan-read` 绑定 SA。

## 3. 镜像构建

`mcp-servers/scan/Dockerfile`（多阶段）：
- builder：golang:1.26，`go work` 下 `go build ./mcp-servers/scan`（CGO_ENABLED=0 静态）。
- runtime：distroless 或 alpine 最小镜像，拷二进制。
- 版本钉死对齐 deploy/versions.md；本地 `docker build` + `kind load docker-image aegis/scan:0.1.0 --name aegis`。

## 4. MCPServer CR

参照 `deploy/mcp-servers/k8s-read-mcpserver.yaml`：`transportType: http`、`deployment.image/serviceAccountName/port=8081`、`httpTransport.path=/mcp,targetPort=8081`（三者端口一致）。

## 5. 验证分层

| 层 | 方法 | 通过判据 |
| --- | --- | --- |
| 部署 | `kubectl get pod -l app=scan` | Running |
| RBAC | `kubectl auth can-i` | list pods 允许 / get secrets 拒绝 |
| 发现 | `kubectl get mcpserver scan` + kagent tools | status True，scan_cluster 列出 |
| 端到端（工具调用层） | 注入 OOMKilled → MCP 调 scan_cluster | 命中 OOM_KILLED + Pod 名 |
| LLM 端到端 | **blocked**（OpenAI 不可达） | 标注待私域 vLLM |

端到端复用 T1.12 集成测试思路，但跑在 kagent 部署的 scan server（经 Service /mcp）上。

## 6. 失效与回滚

- 镜像/部署问题：`kubectl delete mcpserver scan` + 删 RBAC，kagent 移除该工具。
- scan server 不可用 = 该工具不可用，不影响其它工具与集群（I10：外挂控制环，fail-closed 于写路径）。
