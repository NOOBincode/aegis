// Package breaker 是 aegis 限界上下文：熔断器 + 爆炸半径配额，M2 T2.6。
//
// 组织约定（internal/README.md）：domain（纯领域）+ ports（出站接口）+ service（用例编排）
// 为包内文件分层；禁止 import k8s.io/sigs.k8s.io/OPA/MCP SDK 与 internal/adapters（depguard 机检）。
package breaker
