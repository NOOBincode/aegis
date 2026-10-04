// Package main 是 aegis MCP 工具进程：k8s-write。
//
// Phase 0 占位：进程生命周期由 MCP server 共享框架承载（internal/mcpserver，M1 T1.1 接入）。
// 职责边界：工具层只做参数校验 + 执行 + 结果结构化返回，放行判断永不下沉到本层。
//
// 设计出处：mcp-servers/k8s-write/README.md；设计文档 §4.1。
package main

func main() {
	// M1（T1.1）：internal/mcpserver 注册 + 工具元数据强校验（risk_hint/idempotent/reversible/est_blast_radius/timeout_ms 缺一注册失败）
	// 随后按里程碑填充工具集：K8s 写工具集（scale/restart/cordon，仅接受 ChangeRequest ID，M2 T2.8 填充）
}
