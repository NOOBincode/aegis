// Package main 是 aegis MCP 工具进程：k8s-read（8 个只读工具，T1.2）。
//
// 职责边界（设计文档 §4.1）：参数校验 + 执行 + 结构化返回，放行/归因/审批判断永不下沉本层。
// M1 形态：无 L2 闸门，经 reader（client-go 只读直连，R0 只读 kubeconfig）；M2 收敛进闸门时工具代码不变。
//
// 设计出处：mcp-servers/k8s-read/README.md；internal/mcpserver/README.md。
package main

import (
	"log/slog"
	"os"

	"github.com/aegis-dev/aegis/internal/breaker"
	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/internal/session"
	"github.com/aegis-dev/aegis/mcp-servers/k8s-read/reader"
)

const (
	serverName    = "k8s-read"
	serverVersion = "0.2.0"
	listenAddr    = ":8080"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// 只读集群访问：集群内用 in-cluster（R0 只读 SA），否则用 KUBECONFIG（本地调试）。
	r, err := newReader()
	if err != nil {
		logger.Error("构造集群只读客户端失败", "error", err)
		os.Exit(1)
	}

	limits := session.LimitsFromEnv()
	reg := mcpserver.NewRegistry()
	budget := session.NewManager(limits)
	if limits.QPS > 0 {
		budget.WithQPS(breaker.NewQPSWindow(limits.QPS, limits.QPSWindow))
	}
	reg.SetBudget(budget)
	registerTools(reg, r) // 8 个只读工具；元数据五字段缺一注册即 panic（fail-fast）

	logger.Info("k8s-read 启动", "tools", reg.Len(), "addr", listenAddr)
	srv := mcpserver.NewServer(serverName, serverVersion, reg)
	if err := srv.ServeHTTP(listenAddr); err != nil {
		logger.Error("serve 失败", "error", err)
		os.Exit(1)
	}
}

// newReader 优先 in-cluster（Pod 内），否则 KUBECONFIG 环境变量指向的 kubeconfig。
func newReader() (*reader.Reader, error) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return reader.NewInCluster()
	}
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		return reader.NewFromKubeconfig(kc)
	}
	// 默认 in-cluster 失败时返回错误（fail-fast：无集群访问能力的只读工具无意义）。
	return reader.NewInCluster()
}
