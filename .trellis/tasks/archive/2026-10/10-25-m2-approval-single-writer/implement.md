# M2 审批单写者执行计划

1. 新增 `api/v1alpha1/approvaldecision_types.go` 与 `crds/aegis.dev_approvaldecisions.yaml`。
2. 修改 `internal/gatekeeper/approval_server.go`：只创建决定，实现 UID 派生名、evidenceHash、ownerReference 和幂等冲突。
3. 修改 `internal/gatekeeper/changerequest.go`：删除初始 `UpdateStatus`。
4. 修改 `internal/controller/reconciler.go`、`manager.go`：初始化状态、消费决定、watch 入队。
5. 修改 `deploy/gatekeeper/rbac.yaml`、`deploy/controller/rbac.yaml`，并同步相关注释与文档。
6. 更新 `approval_server_test.go`、`changerequest_test.go`、`reconciler_test.go` 和必要的 manager 测试。
7. 运行 gofmt、CLI/根单测、`make lint`、`make test-unit`、`make build`、`git diff --check` 和密钥检查。
8. 复核所有 `UpdateStatus` 调用点，确认只剩 controller；标注 L2 人审和未覆盖验证，不提交。

回滚点：新增 CRD 与代码均为 additive；若测试失败，保持现有 ChangeRequest 行为不弱化，逐层修复后再进入 M3。
