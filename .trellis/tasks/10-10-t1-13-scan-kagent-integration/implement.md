# T1.13 scan server kagent 集成 — 执行计划

- [ ] 1. `mcp-servers/scan/Dockerfile`（多阶段 CGO_ENABLED=0）+ 版本钉死。
- [ ] 2. `deploy/mcp-servers/scan-rbac.yaml`（SA + ClusterRole 只读 + Binding）。
- [ ] 3. `deploy/mcp-servers/scan-mcpserver.yaml`（MCPServer CR，serviceAccountName）。
- [ ] 4. 构建镜像 + kind load + apply RBAC/MCPServer CR。
- [ ] 5. 验证：pod Running、RBAC can-i、mcpserver status True、tools/list 发现 scan_cluster。
- [ ] 6. 端到端：注入 OOMKilled → 经 MCP/Service 调 scan_cluster 命中 OOM_KILLED。
- [ ] 7. 标注 LLM 端到端 blocked（OpenAI 不可达）+ 私域 vLLM 接缝。
- [ ] 8. 全量 lint/build/test + trellis-check → commit。
