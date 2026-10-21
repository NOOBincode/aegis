// Package main 是 aegis L2 安全闸门主服务（gatekeeper）的装配入口。
//
// M2 T2.4/T2.11 软件层：提供签名信封的审批 HTTP 入口（/v1/approval）；
// MCP 代理/写执行器在后续接入（M2 T2.8）。审批终判在 internal/approval，
// gatekeeper 不信任 CLI 的本地判断。
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	mcpAddr := fs.String("mcp-addr", "", "gatekeeper MCP 监听地址 host:port（env AEGIS_GATE_MCP_ADDR > :8086）")
	tlsCert := fs.String("tls-cert", "", "审批 HTTPS 证书（env AEGIS_TLS_CERT）")
	tlsKey := fs.String("tls-key", "", "审批 HTTPS 私钥（env AEGIS_TLS_KEY）")
	tlsCA := fs.String("tls-ca", "", "mTLS 客户端 CA（env AEGIS_TLS_CA）")
	requireClientCert := fs.Bool("require-client-cert", true, "要求并校验 mTLS 客户端证书")
	mcpTLSCert := fs.String("mcp-tls-cert", "", "MCP HTTPS 证书（env AEGIS_MCP_TLS_CERT）")
	mcpTLSKey := fs.String("mcp-tls-key", "", "MCP HTTPS 私钥（env AEGIS_MCP_TLS_KEY）")
	mcpTLSCA := fs.String("mcp-tls-ca", "", "MCP mTLS 客户端 CA（env AEGIS_MCP_TLS_CA）")
	mcpRequireClientCert := fs.Bool("mcp-require-client-cert", false, "MCP 要求并校验客户端证书")
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
	gateAddr := firstNonEmpty(*mcpAddr, os.Getenv("AEGIS_GATE_MCP_ADDR"), ":8086")
	if _, _, err := net.SplitHostPort(gateAddr); err != nil {
		logger.Error("非法 mcp-addr", "addr", gateAddr, "error", err)
		return 2
	}
	secretRaw := os.Getenv("AEGIS_APPROVAL_SECRET")
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
	gateMCP := newGatekeeperMCPServer(dc, logger)
	mcpCert := firstNonEmpty(*mcpTLSCert, os.Getenv("AEGIS_MCP_TLS_CERT"))
	mcpKey := firstNonEmpty(*mcpTLSKey, os.Getenv("AEGIS_MCP_TLS_KEY"))
	mcpCA := firstNonEmpty(*mcpTLSCA, os.Getenv("AEGIS_MCP_TLS_CA"))
	mcpTLSConfig, err := buildMCPTLS(mcpCert, mcpKey, mcpCA, *mcpRequireClientCert)
	if err != nil {
		logger.Error("MCP TLS 配置失败", "error", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 2)

	// 审批端点只在有 HMAC secret 时启动；MCP request_change 不需要审批 secret 也能服务，
	// 这样 kagent 可先接入“意图入口”，审批密钥由独立部署/Secret 注入。
	if secretRaw != "" {
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
		certFile := firstNonEmpty(*tlsCert, os.Getenv("AEGIS_TLS_CERT"))
		keyFile := firstNonEmpty(*tlsKey, os.Getenv("AEGIS_TLS_KEY"))
		caFile := firstNonEmpty(*tlsCA, os.Getenv("AEGIS_TLS_CA"))
		tlsEnabled := certFile != "" || keyFile != "" || caFile != ""
		var tlsConfig *tls.Config
		actorFromCert := false
		if tlsEnabled {
			if certFile == "" || keyFile == "" {
				logger.Error("TLS 需要同时配置 cert/key")
				return 2
			}
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			if caFile != "" {
				pem, err := os.ReadFile(caFile) //nolint:gosec // 运维显式配置的 CA 路径
				if err != nil {
					logger.Error("读取 mTLS CA 失败", "error", err)
					return 2
				}
				pool := x509.NewCertPool()
				if !pool.AppendCertsFromPEM(pem) {
					logger.Error("mTLS CA 无有效证书", "file", caFile)
					return 2
				}
				tlsConfig.ClientCAs = pool
				if *requireClientCert {
					tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
					actorFromCert = true
				}
			} else if *requireClientCert {
				logger.Error("require-client-cert 需要 --tls-ca")
				return 2
			}
		}
		handler := &gatekeeper.ApprovalServer{Client: dc, Verifier: verifier, ActorFromCert: actorFromCert}
		mux := http.NewServeMux()
		mux.Handle("/v1/approval", handler)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second, TLSConfig: tlsConfig}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		logger.Info("gatekeeper 审批接口启动", "addr", addr)
		go func() {
			var err error
			if tlsEnabled {
				logger.Info("gatekeeper 审批 mTLS 启动", "addr", addr, "client_cert", actorFromCert)
				err = srv.ListenAndServeTLS(certFile, keyFile)
			} else {
				err = srv.ListenAndServe()
			}
			if err != nil && err != http.ErrServerClosed {
				errCh <- err
			}
		}()
	} else {
		logger.Info("gatekeeper 审批接口未启用（无 AEGIS_APPROVAL_SECRET/FILE）；MCP request_change 仍可用")
	}

	go func() {
		if mcpTLSConfig != nil {
			logger.Info("gatekeeper MCP TLS 启动", "addr", gateAddr, "client_cert", *mcpRequireClientCert)
			if err := gateMCP.ServeHTTPS(gateAddr, mcpCert, mcpKey, mcpTLSConfig); err != nil {
				errCh <- err
			}
			return
		}
		logger.Info("gatekeeper MCP 接口启动", "addr", gateAddr)
		if err := gateMCP.ServeHTTP(gateAddr); err != nil {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		return 0
	case err := <-errCh:
		logger.Error("gatekeeper 服务退出", "error", err)
		return 1
	}
}

func buildMCPTLS(certFile, keyFile, caFile string, requireClient bool) (*tls.Config, error) {
	if certFile == "" && keyFile == "" && caFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("MCP TLS 需要同时配置 cert/key")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile) //nolint:gosec // 运维显式配置
		if err != nil {
			return nil, fmt.Errorf("读取 MCP CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("MCP CA 无有效证书")
		}
		cfg.ClientCAs = pool
	}
	if requireClient {
		if cfg.ClientCAs == nil {
			return nil, fmt.Errorf("MCP require-client-cert 需要 --mcp-tls-ca")
		}
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
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
