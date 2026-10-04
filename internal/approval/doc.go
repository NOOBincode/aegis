// Package approval 是 aegis 限界上下文：审批流状态机（服务端强制，CLI 仅为视图），M2 T2.4。
//
// 组织约定（internal/README.md）：domain（纯领域）+ ports（出站接口）+ service（用例编排）
// 为包内文件分层；禁止 import k8s.io/sigs.k8s.io/OPA/MCP SDK 与 internal/adapters（depguard 机检）。
package approval
