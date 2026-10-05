// Package main 是 aegis MCP 工具进程：k8s-read。
//
// M1（T1.1）：经 internal/mcpserver 共享框架注册工具（元数据五字段强校验）并以 MCP 协议提供服务，
// 供 kagent ToolServer 发现。职责边界：工具层只做参数校验 + 执行 + 结果结构化返回，放行判断永不下沉。
// 真正的 8 个只读工具集在 T1.2 填充（见 README.md §2 工具清单）；此处先以 get_nodes 空壳打通框架端到端。
//
// 设计出处：mcp-servers/k8s-read/README.md；internal/mcpserver/README.md；设计文档 §4.1。
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aegis-dev/aegis/internal/mcpserver"
)

const (
	serverName    = "k8s-read"
	serverVersion = "0.1.0"
	listenAddr    = ":8080"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	reg := mcpserver.NewRegistry()

	// T1.1 空壳工具：打通"注册（元数据强校验）→ 中间件链 → MCP 服务 → kagent 发现"全链路。
	// 真实 K8s 查询在 T1.2 落地（接 internal/adapters/k8s，经 ports 抽象）。
	reg.MustRegister(mcpserver.Tool{
		Name:        "get_nodes",
		Description: "列出集群节点（只读）。T1.1 空壳：先验证框架链路，T1.2 接真实 client-go 查询。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "description": "返回上限，≤200"},
			},
		},
		Metadata: mcpserver.ToolMetadata{
			RiskHint:       mcpserver.RiskR0,
			Idempotent:     true,
			Reversible:     false, // 只读无逆操作概念，但字段必填（五字段强校验）
			EstBlastRadius: mcpserver.BlastRadius{Nodes: 0},
			TimeoutMs:      5000,
		},
		Handler: func(_ context.Context, _ map[string]any) *mcpserver.Result {
			// T1.1 空壳：返回占位；T1.2 替换为真实查询 + 分页 + 上限。
			return &mcpserver.Result{Data: map[string]any{
				"items":        []any{},
				"object_count": 0,
				"note":         "k8s-read 骨架已注册并可被发现；真实查询 T1.2 落地",
			}}
		},
	})

	logger.Info("k8s-read 骨架启动", "tools", reg.Len(), "addr", listenAddr)
	srv := mcpserver.NewServer(serverName, serverVersion, reg)
	if err := srv.ServeHTTP(listenAddr); err != nil {
		logger.Error("serve 失败", "error", err)
		os.Exit(1)
	}
}
