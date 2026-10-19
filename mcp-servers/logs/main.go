// Package main 是 aegis MCP 工具进程：logs（多 Pod 日志聚合，T1.3）。
//
// 只读 pods/pods.log；日志是不可信数据，出口由 internal/mcpserver 统一 sanitize/审计/预算。
package main

import (
	"log/slog"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	k8sadapter "github.com/aegis-dev/aegis/internal/adapters/k8s"
	"github.com/aegis-dev/aegis/internal/breaker"
	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/internal/session"
)

const (
	serverName    = "logs"
	serverVersion = "0.1.0"
	listenAddr    = ":8083"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cs, err := newClientset()
	if err != nil {
		logger.Error("构造 k8s client 失败", "error", err)
		os.Exit(1)
	}
	provider := k8sadapter.NewObservabilityProvider(cs)

	limits := session.LimitsFromEnv()
	reg := mcpserver.NewRegistry()
	budget := session.NewManager(limits)
	if limits.QPS > 0 {
		budget.WithQPS(breaker.NewQPSWindow(limits.QPS, limits.QPSWindow))
	}
	reg.SetBudget(budget)
	reg.MustRegister(aggregateLogsTool(provider))

	logger.Info("logs 启动", "tools", reg.Len(), "addr", listenAddr)
	srv := mcpserver.NewServer(serverName, serverVersion, reg)
	if err := srv.ServeHTTP(listenAddr); err != nil {
		logger.Error("serve 失败", "error", err)
		os.Exit(1)
	}
}

func newClientset() (kubernetes.Interface, error) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
		return kubernetes.NewForConfig(cfg)
	}
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kc)
		if err != nil {
			return nil, err
		}
		return kubernetes.NewForConfig(cfg)
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}
