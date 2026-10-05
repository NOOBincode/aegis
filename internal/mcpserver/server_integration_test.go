package mcpserver

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestServerDiscoveryIntegration 是 T1.1 判据的集成形态：空壳 server 注册成功 → 经 MCP 协议
// 客户端能 initialize / tools/list 发现 get_nodes（含元数据）/ tools/call 调通且结果被 I4 包裹。
// 用 mcp-go 真实 client + 真实 StreamableHTTP 传输（httptest），避免 curl/SSE 的手工歧义。
func TestServerDiscoveryIntegration(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(validTool("get_nodes"))

	srv := NewServer("k8s-read", "0.1.0", reg)
	handler, err := srv.HTTPHandler()
	if err != nil {
		t.Fatalf("build mcp server: %v", err)
	}
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := client.NewStreamableHttpClient(httpSrv.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client start: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// 发现：tools/list 必须列出 get_nodes，且携带 aegis 元数据（_meta）。
	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var found *mcp.Tool
	for i := range tools.Tools {
		if tools.Tools[i].Name == "get_nodes" {
			found = &tools.Tools[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("tools/list 未列出 get_nodes，got %d 个工具", len(tools.Tools))
	}
	if found.Annotations.IdempotentHint == nil || !*found.Annotations.IdempotentHint {
		t.Errorf("get_nodes 应带 idempotentHint=true（来自元数据）")
	}
	if found.Annotations.ReadOnlyHint == nil || !*found.Annotations.ReadOnlyHint {
		t.Errorf("get_nodes R0 应映射 readOnlyHint=true")
	}

	// 调用：tools/call 返回被 <untrusted_cluster_data> 包裹的结果。
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "get_nodes"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_nodes 调用不应报错: %+v", res.Content)
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			text += tc.Text
		}
	}
	// 结果文本是 Result 的 JSON；包裹分隔符的尖括号会被 JSON 转义（< → <），故只校验分隔符名。
	if !strings.Contains(text, "untrusted_cluster_data") || !strings.Contains(text, `"wrapped":true`) {
		t.Errorf("结果应被 <untrusted_cluster_data> 包裹且 meta.wrapped=true（I4），got %q", text)
	}
}
