//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"net/http/httptest"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	k8sadapter "github.com/aegis-dev/aegis/internal/adapters/k8s"
	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/internal/policyload"
	"github.com/aegis-dev/aegis/internal/scan"
)

// 集成/冒烟：kind 实集群（KUBECONFIG 或 ~/.kube/config）。运行：
//   go test -tags integration ./mcp-servers/scan/...

func integrationClientset(t *testing.T) *kubernetes.Clientset {
	t.Helper()
	kc := os.Getenv("KUBECONFIG")
	if kc == "" {
		home, _ := os.UserHomeDir()
		kc = filepath.Join(home, ".kube", "config")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	return cs
}

// integrationServer 起挂了真实 scan service 的 MCP server（httptest），返回 client。
func integrationServer(t *testing.T) *client.Client {
	t.Helper()
	cs := integrationClientset(t)
	prov := k8sadapter.NewScanSnapshotProvider(cs)
	svc := scan.NewService(prov, policyload.FilePolicySource{}, nil, 0, nil)

	reg := mcpserver.NewRegistry()
	if err := reg.Register(scanClusterTool(svc)); err != nil {
		t.Fatalf("register: %v", err)
	}
	srv := mcpserver.NewServer("scan-integration", "0.0.1", reg)
	h, err := srv.HTTPHandler()
	if err != nil {
		t.Fatalf("httphandler: %v", err)
	}
	httpSrv := httptest.NewServer(h)
	t.Cleanup(httpSrv.Close)

	c, err := client.NewStreamableHttpClient(httpSrv.URL)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// 冒烟：server 启动且 scan_cluster 被发现。
func TestIntegrationSmoke(t *testing.T) {
	c := integrationServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "scan_cluster" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tools/list 应发现 scan_cluster：%+v", tools.Tools)
	}
}

// 集成：kind 注入 OOMKilled Pod → scan_cluster 经 MCP 调用命中 OOM_KILLED。
func TestIntegrationOOMDetection(t *testing.T) {
	cs := integrationClientset(t)
	c := integrationServer(t)
	ctx := context.Background()
	const name = "scan-it-oom"

	// 注入 OOMKilled 场景（postgres 镜像已在 kind 节点，awk 指数分配内存 + 64Mi limit）。
	_ = cs.CoreV1().Pods("default").Delete(ctx, name, metav1.DeleteOptions{})
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: map[string]string{"app": "scan-it"}},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{{
				Name:    "demo",
				Image:   "docker.io/library/postgres:18.6-alpine3.23",
				Command: []string{"awk", "BEGIN{a=\"xxxxxxxx\";while(1){a=a a}}"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
				},
			}},
		},
	}
	if _, err := cs.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("创建 OOM pod: %v", err)
	}
	t.Cleanup(func() { _ = cs.CoreV1().Pods("default").Delete(context.Background(), name, metav1.DeleteOptions{}) })

	// 等待 OOMKilled（lastState.terminated reason=OOMKilled 或 exitCode=137）。
	if !waitForOOMKilled(t, cs, "default", name, 90*time.Second) {
		t.Fatal("等待 OOMKilled 超时")
	}

	// 经 MCP 调 scan_cluster（限定 default ns）。
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := c.CallTool(callCtx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "scan_cluster", Arguments: map[string]any{"namespace": "default"},
	}})
	if err != nil {
		t.Fatalf("call scan_cluster: %v", err)
	}
	if res.IsError {
		t.Fatalf("scan_cluster 返回错误：%s", scanResultText(res))
	}
	text := scanResultText(res)
	if !strings.Contains(text, "OOM_KILLED") || !strings.Contains(text, name) {
		t.Errorf("应命中 %s 的 OOM_KILLED，got：%s", name, text)
	}
}

// waitForOOMKilled 轮询 Pod 直至出现 OOM 终止（reason=OOMKilled 或 exitCode=137）。
func waitForOOMKilled(t *testing.T, cs *kubernetes.Clientset, ns, name string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p, err := cs.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err == nil {
			for _, cst := range p.Status.ContainerStatuses {
				if cst.LastTerminationState.Terminated != nil {
					tt := cst.LastTerminationState.Terminated
					if tt.Reason == "OOMKilled" || tt.ExitCode == 137 {
						return true
					}
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}
