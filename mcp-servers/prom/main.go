// Package main 是 aegis MCP 工具进程：prom（Prometheus 窄查询，T1.3）。
//
// 职责边界：只读 instant/range 两个端点；不做 PromQL 语义改写、不做告警/写操作。
// 出口经 internal/mcpserver 的 sanitize/审计/预算/超时中间件。
package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/aegis-dev/aegis/internal/adapters/prom"
	"github.com/aegis-dev/aegis/internal/breaker"
	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/internal/session"
)

const (
	serverName    = "prom"
	serverVersion = "0.1.0"
	listenAddr    = ":8082"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	base := os.Getenv("AEGIS_PROM_URL")
	if base == "" {
		base = "http://prometheus-kube-prometheus-prometheus.observability.svc:9090"
	}
	q, err := prom.New(base, 10*time.Second, maxSeries)
	if err != nil {
		logger.Error("构造 Prometheus 客户端失败", "error", err)
		os.Exit(1)
	}

	limits := session.LimitsFromEnv()
	reg := mcpserver.NewRegistry()
	budget := session.NewManager(limits)
	if limits.QPS > 0 {
		budget.WithQPS(breaker.NewQPSWindow(limits.QPS, limits.QPSWindow))
	}
	reg.SetBudget(budget)
	reg.MustRegister(promQueryTool(q))
	reg.MustRegister(promRangeTool(q))

	logger.Info("prom 启动", "tools", reg.Len(), "addr", listenAddr, "base", base)
	srv := mcpserver.NewServer(serverName, serverVersion, reg)
	if err := srv.ServeHTTP(listenAddr); err != nil {
		logger.Error("serve 失败", "error", err)
		os.Exit(1)
	}
}
