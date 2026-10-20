// Package main 是 aegis CRD controller 的装配入口。
//
// M2 T2.1：controller-lite 用 client-go dynamic informer + internal/controller.Reconciler；
// 不做 leader election/controller-runtime（后续替换，Reconciler 语义不变）。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/aegis-dev/aegis/internal/adapters/k8s"
	"github.com/aegis-dev/aegis/internal/controller"
)

const processName = "controller"

func main() {
	os.Exit(run())
}

func run() int {
	fs := pflag.NewFlagSet(processName, pflag.ContinueOnError)
	logLevel := fs.String("log-level", "", "slog level: debug|info|warn|error（env AEGIS_LOG_LEVEL > 默认 info）")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig 路径（空=in-cluster 或 KUBECONFIG）")
	resync := fs.Duration("resync", 30*time.Second, "informer 全量重同步周期")
	enableWrites := fs.Bool("enable-writes", false, "启用 ScaleExecutor 真实写（默认 fail-closed 只读核对）")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	level := firstNonEmpty(*logLevel, os.Getenv("AEGIS_LOG_LEVEL"), "info")
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "invalid --log-level %q\n", level)
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
	cfg, err := restConfig(*kubeconfig)
	if err != nil {
		logger.Error("加载 kubeconfig 失败", "error", err)
		return 2
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		logger.Error("构造 dynamic client 失败", "error", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	rec := &controller.Reconciler{Client: dc}
	if *enableWrites {
		cs, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			logger.Error("构造 clientset 失败", "error", err)
			return 1
		}
		rec.Executor = k8s.NewScaleExecutor(cs)
		logger.Warn("controller.writes_enabled", "executor", "scale")
	} else {
		logger.Info("controller.writes_disabled", "reason", "安全默认，--enable-writes 才执行 CR")
	}
	mgr := &controller.Manager{Client: dc, Reconciler: rec, Resync: *resync, Logger: logger}
	if err := mgr.Run(ctx); err != nil {
		logger.Error("controller 退出", "error", err)
		return 1
	}
	return 0
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return rest.InClusterConfig()
	}
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		return clientcmd.BuildConfigFromFlags("", kc)
	}
	return rest.InClusterConfig()
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
