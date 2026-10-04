// Package sanitize 是 aegis 限界上下文：不可信数据消毒/隔离包裹，M1 T1.5 库形态。
//
// 组织约定（internal/README.md）：domain（纯领域）+ ports（出站接口）+ service（用例编排）
// 为包内文件分层；禁止 import k8s.io/sigs.k8s.io/OPA/MCP SDK 与 internal/adapters（depguard 机检）。
package sanitize
