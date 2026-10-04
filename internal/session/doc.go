// Package session 是 aegis 限界上下文：会话与预算（sessionBudget 执行点），M1 T1.4。
//
// 组织约定（internal/README.md）：domain（纯领域）+ ports（出站接口）+ service（用例编排）
// 为包内文件分层；禁止 import k8s.io/sigs.k8s.io/OPA/MCP SDK 与 internal/adapters（depguard 机检）。
package session
