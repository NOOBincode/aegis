// Package main 是 aegis L2 安全闸门主服务（gatekeeper）的装配入口。
//
// M2 T2.4/T2.11 软件层：提供签名信封的审批 HTTP 入口（/v1/approval）；
// MCP 代理/写执行器在后续接入（M2 T2.8）。审批终判在 internal/approval，
// gatekeeper 不信任 CLI 的本地判断。
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/aegis-dev/aegis/internal/gatekeeper"
	"github.com/aegis-dev/aegis/internal/session"
)

const processName = "gatekeeper"

func main() {
	os.Exit(run())
}

func run() int {
	fs := pflag.NewFlagSet(processName, pflag.ContinueOnError)
	logLevel := fs.String("log-level", "", "slog level: debug|info|warn|error（env AEGIS_LOG_LEVEL > 默认 info）")
	approvalAddr := fs.String("approval-addr", "", "审批 HTTP 监听地址 host:port（env AEGIS_APPROVAL_ADDR > :8085）")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig 路径（空=in-cluster 或 KUBECONFIG）")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	level := firstNonEmpty(*logLevel, os.Getenv("AEGIS_LOG_LEVEL"), "info")
	var lvl slog.Level
	switch strings.ToLower(level) {
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
	addr := firstNonEmpty(*approvalAddr, os.Getenv("AEGIS_APPROVAL_ADDR"), ":8085")
	if _, _, err := net.SplitHostPort(addr); err != nil {
		logger.Error("非法 approval-addr", "addr", addr, "error", err)
		return 2
	}
	secretRaw := os.Getenv("AEGIS_APPROVAL_SECRET")
	if secretRaw == "" {
		logger.Info("gatekeeper 启动（审批接口未启用：AEGIS_APPROVAL_SECRET 为空）", "addr", addr)
		return 0
	}
	secret, err := decodeSecret(secretRaw)
	if err != nil {
		logger.Error("AEGIS_APPROVAL_SECRET 非法", "error", err)
		return 2
	}
	verifier, err := session.NewVerifier(secret, time.Minute)
	if err != nil {
		logger.Error("构造信封校验器失败", "error", err)
		return 2
	}
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
	handler := &gatekeeper.ApprovalServer{Client: dc, Verifier: verifier}
	mux := http.NewServeMux()
	mux.Handle("/v1/approval", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	logger.Info("gatekeeper 审批接口启动", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("审批接口退出", "error", err)
		return 1
	}
	return 0
}

func decodeSecret(raw string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(raw); err == nil && len(b) >= 16 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) >= 16 {
		return b, nil
	}
	return nil, fmt.Errorf("必须是 base64 编码的 ≥16 字节密钥")
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
