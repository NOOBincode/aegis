// Package risk 是 aegis 限界上下文：风险分级（R0–R3 判定表）：资源类型×动词×作用域确定性查表，M2 T2.2。
//
// 组织约定（internal/README.md）：domain（纯领域）+ ports（出站接口）+ service（用例编排）
// 为包内文件分层；禁止 import k8s.io/sigs.k8s.io/OPA/MCP SDK 与 internal/adapters（depguard 机检）。
package risk
