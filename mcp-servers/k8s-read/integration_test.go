//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/mcp-servers/k8s-read/reader"
)

// 集成/冒烟：kind 实集群（KUBECONFIG 或 ~/.kube/config）。运行：
//   go test -tags integration ./mcp-servers/k8s-read/...

func integrationClient(t *testing.T) *client.Client {
	t.Helper()
	kc := os.Getenv("KUBECONFIG")
	if kc == "" {
		home, _ := os.UserHomeDir()
		kc = filepath.Join(home, ".kube", "config")
	}
	r, err := reader.NewFromKubeconfig(kc)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	reg := mcpserver.NewRegistry()
	registerTools(reg, r)
	srv := mcpserver.NewServer("k8s-read-integration", "0.0.1", reg)
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

func callReadTool(t *testing.T, c *client.Client, name string, args map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s 返回错误：%v", name, res.Content)
	}
	var b strings.Builder
	for _, ct := range res.Content {
		if tc, ok := ct.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// 冒烟：server 启动且 8 个只读工具被发现。
func TestIntegrationSmoke(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools.Tools) != 8 {
		t.Fatalf("应发现 8 个工具，got %d", len(tools.Tools))
	}
}

// 集成：对 kind 实集群调核心读工具，断言结构化返回 + I4 包裹。
func TestIntegrationReadTools(t *testing.T) {
	c := integrationClient(t)

	// get_nodes：kind 集群必有节点（control-plane）。
	if s := callReadTool(t, c, "get_nodes", map[string]any{}); !strings.Contains(s, "untrusted_cluster_data") || !strings.Contains(s, "control-plane") {
		t.Errorf("get_nodes 应返回节点且被包裹：%s", s)
	}
	// get_pods kube-system：系统 pod 必在。
	if s := callReadTool(t, c, "get_pods", map[string]any{"ns": "kube-system"}); !strings.Contains(s, "object_count") {
		t.Errorf("get_pods 应返回对象计数：%s", s)
	}
	// get_deployments kube-system：coredns 等必在。
	if s := callReadTool(t, c, "get_deployments", map[string]any{"ns": "kube-system"}); !strings.Contains(s, "coredns") {
		t.Errorf("get_deployments 应含 coredns：%s", s)
	}
	// get_events kube-system。
	if s := callReadTool(t, c, "get_events", map[string]any{"ns": "kube-system"}); !strings.Contains(s, "untrusted_cluster_data") {
		t.Errorf("get_events 应被包裹：%s", s)
	}
	// describe_node 不存在则无；describe_pod 对真实系统 pod。
	// 取一个真实 pod 名再 describe。
	pods := callReadTool(t, c, "get_pods", map[string]any{"ns": "kube-system"})
	_ = pods // describe_pod 依赖具体 pod 名，已由 fake 单测覆盖；此处验证 list 即可。

	// get_resource_metrics 依赖 metrics-server；kind 未装则跳过（不强求）。
	mctx, mcancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer mcancel()
	mres, err := c.CallTool(mctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "get_resource_metrics", Arguments: map[string]any{"kind": "nodes"},
	}})
	if err == nil && !mres.IsError {
		t.Logf("get_resource_metrics 可用（metrics-server 已装）")
	} else {
		t.Logf("get_resource_metrics 跳过（kind 未装 metrics-server）")
	}
}
