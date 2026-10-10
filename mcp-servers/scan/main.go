// Package main 是 aegis MCP 工具进程：scan（scan_cluster 只读工具，T1.12）。
//
// 职责边界（设计文档 §4.1）：参数校验 + 执行 + 结构化返回，放行/归因判断永不下沉本层。
// M1 形态：无 L2 闸门，经 internal/adapters/k8s 只读直连（R0）；M2 收敛进闸门时工具代码不变。
//
// 本文件是进程装配入口（可 import internal/adapters/k8s）；工具 handler 见 tools.go（纯净）。
package main

import (
	"log/slog"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	k8sadapter "github.com/aegis-dev/aegis/internal/adapters/k8s"
	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/internal/policyload"
	"github.com/aegis-dev/aegis/internal/scan"
)

const (
	serverName    = "scan"
	serverVersion = "0.1.0"
	listenAddr    = ":8081"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	svc, err := newScanService()
	if err != nil {
		logger.Error("构造 scan service 失败", "error", err)
		os.Exit(1)
	}

	reg := mcpserver.NewRegistry()
	reg.MustRegister(scanClusterTool(svc)) // 元数据五字段缺一注册即 panic（fail-fast）

	logger.Info("scan 启动", "tools", reg.Len(), "addr", listenAddr)
	srv := mcpserver.NewServer(serverName, serverVersion, reg)
	if err := srv.ServeHTTP(listenAddr); err != nil {
		logger.Error("serve 失败", "error", err)
		os.Exit(1)
	}
}

// newScanService 装配真实巡检 service：clientset → 采集端口 → B 层规则来源 → Engine。
func newScanService() (*scan.Service, error) {
	cfg, err := restConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	provider := k8sadapter.NewScanSnapshotProvider(cs)
	// B 层规则来源：AEGIS_SCAN_POLICIES 目录（热修改），空则用嵌入默认规则。
	policies := policyload.FilePolicySource{Dir: os.Getenv("AEGIS_SCAN_POLICIES")}
	return scan.NewService(provider, policies, nil, 0, nil), nil
}

// restConfig 优先 in-cluster（R0 只读 SA），否则 KUBECONFIG（本地调试）。
func restConfig() (*rest.Config, error) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return rest.InClusterConfig()
	}
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		return clientcmd.BuildConfigFromFlags("", kc)
	}
	return rest.InClusterConfig()
}
