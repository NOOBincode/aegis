package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"net/http/httptest"

	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/internal/scan"
)

// 黑盒：把 scan server 当不透明服务，只经 MCP 协议交互，喂边界/异常输入，验证外部行为契约。
// 复用 internal/mcpserver 的黑盒基建模式（HTTPHandler + httptest + StreamableHttpClient）。

// panicScanner 在 Scan 时 panic（测试框架 recover 隔离）。
type panicScanner struct{}

func (panicScanner) Scan(context.Context, scan.CollectOptions) (scan.Result, error) { panic("boom") }

func startScanBlackBox(t *testing.T, s Scanner) (*client.Client, func()) {
	t.Helper()
	reg := mcpserver.NewRegistry()
	if err := reg.Register(scanClusterTool(s)); err != nil {
		t.Fatalf("register: %v", err)
	}
	srv := mcpserver.NewServer("scan-test", "0.0.1", reg)
	h, err := srv.HTTPHandler()
	if err != nil {
		t.Fatalf("httphandler: %v", err)
	}
	httpSrv := httptest.NewServer(h)
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
	return c, func() { _ = c.Close(); httpSrv.Close() }
}

func callScanTool(t *testing.T, c *client.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		t.Fatalf("CallTool %s transport err: %v", name, err)
	}
	return res
}

func scanResultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// tools/list：scan_cluster 被发现，schema 与只读注解契约正确。
func TestBlackBoxListTools(t *testing.T) {
	c, done := startScanBlackBox(t, &fakeScanner{res: sampleScanResult()})
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools.Tools) != 1 {
		t.Fatalf("应恰好 1 个工具，got %d", len(tools.Tools))
	}
	tool := tools.Tools[0]
	if tool.Name != "scan_cluster" || tool.Description == "" {
		t.Errorf("工具名/描述缺失: %+v", tool)
	}
	if tool.InputSchema.Type != "object" {
		t.Errorf("应透出 inputSchema type=object，got %+v", tool.InputSchema)
	}
	if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
		t.Errorf("R0 工具应有 readOnlyHint=true")
	}
}

// tools/call 正常：返回结构化 findings（含 OOM_KILLED）。
func TestBlackBoxCallScanCluster(t *testing.T) {
	c, done := startScanBlackBox(t, &fakeScanner{res: sampleScanResult()})
	defer done()
	res := callScanTool(t, c, "scan_cluster", map[string]any{"namespace": "shop"})
	if res.IsError {
		t.Fatalf("正常调用不应 IsError：%s", scanResultText(res))
	}
	text := scanResultText(res)
	if !strings.Contains(text, "OOM_KILLED") || !strings.Contains(text, "scan-v1") {
		t.Errorf("结果应含 OOM_KILLED finding 与 scan-v1 schema：%s", text)
	}
}

// 未知工具 → 协议层拒绝。
func TestBlackBoxUnknownTool(t *testing.T) {
	c, done := startScanBlackBox(t, &fakeScanner{})
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "no_such_tool"}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("未知工具应在协议层拒绝，got %v", err)
	}
}

// 非法 format 参数 → IsError（schema 类）。
func TestBlackBoxBadFormat(t *testing.T) {
	c, done := startScanBlackBox(t, &fakeScanner{})
	defer done()
	res := callScanTool(t, c, "scan_cluster", map[string]any{"format": "xml"})
	if !res.IsError || !strings.Contains(scanResultText(res), string(mcpserver.ErrClassSchema)) {
		t.Errorf("非法 format 应 schema 错误，got %s", scanResultText(res))
	}
}

// handler panic → 框架 recover 隔离，服务存活（后续调用正常）。
func TestBlackBoxPanicSurvives(t *testing.T) {
	// 先注册 panic 工具（借用 scan_cluster 形态但 handler panic），再加一个正常工具。
	reg := mcpserver.NewRegistry()
	panicker := scanClusterTool(panicScanner{})
	panicker.Name = "scan_panic"
	if err := reg.Register(panicker); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(scanClusterTool(&fakeScanner{res: sampleScanResult()})); err != nil {
		t.Fatal(err)
	}
	srv := mcpserver.NewServer("scan-test", "0.0.1", reg)
	h, _ := srv.HTTPHandler()
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()
	c, err := client.NewStreamableHttpClient(httpSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	res := callScanTool(t, c, "scan_panic", nil)
	if !res.IsError {
		t.Fatalf("panic 应转错误结果，got %s", scanResultText(res))
	}
	// 服务存活：正常工具仍可调用。
	res2 := callScanTool(t, c, "scan_cluster", nil)
	if res2.IsError {
		t.Errorf("panic 后服务应存活，scan_cluster 被影响：%s", scanResultText(res2))
	}
}
