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

// 黑盒：把 server 当不透明服务，只经 MCP 协议交互，喂边界/异常输入，验证外部行为契约。

// startBlackBox 起一个挂了给定工具的 MCP server，返回真实 MCP client 与清理函数。
func startBlackBox(t *testing.T, tools ...Tool) (*client.Client, func()) {
	t.Helper()
	reg := NewRegistry()
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	srv := NewServer("blackbox", "0.0.1", reg)
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

func callTool(t *testing.T, c *client.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		t.Fatalf("CallTool %s transport err: %v", name, err)
	}
	return res
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// 未知工具名 → 协议层拒绝（mcp-go 在 framework.Invoke 之前就拦截：invalid params / tool not found）。
// 说明：这意味着未知工具调用不经过本框架中间件链（含审计埋点）——M1 接受此缺口（无害拒绝），
// M2 若需审计未知工具探测，应在 MCP 传输层或网关掉点补充留痕（见 internal/mcpserver/README.md §6）。
func TestBlackBoxUnknownTool(t *testing.T) {
	c, done := startBlackBox(t, validTool("get_nodes"))
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "no_such_tool"}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("未知工具应在协议层被拒绝（tool not found），got err=%v", err)
	}
}

// 慢工具超时 → IsError，错误类 timeout（I10 黑盒表现）。
func TestBlackBoxToolTimeout(t *testing.T) {
	slow := validTool("slow_tool")
	slow.Metadata.TimeoutMs = 30
	slow.Handler = func(ctx context.Context, _ map[string]any) *Result {
		select {
		case <-time.After(2 * time.Second):
			return &Result{Data: "late"}
		case <-ctx.Done():
			return &Result{Err: NewToolError(ErrClassTimeout, true, "cancelled")}
		}
	}
	c, done := startBlackBox(t, slow)
	defer done()
	res := callTool(t, c, "slow_tool", nil)
	if !res.IsError || !strings.Contains(resultText(res), string(ErrClassTimeout)) {
		t.Fatalf("应返回 timeout 错误，got %+v", res)
	}
}

// handler 返回工具错误 → IsError=true 且可重试标记透传。
func TestBlackBoxToolError(t *testing.T) {
	failer := validTool("get_thing")
	failer.Handler = func(_ context.Context, _ map[string]any) *Result {
		return &Result{Err: NewToolError(ErrClassNotFound, false, "对象不存在")}
	}
	c, done := startBlackBox(t, failer)
	defer done()
	res := callTool(t, c, "get_thing", nil)
	if !res.IsError || !strings.Contains(resultText(res), string(ErrClassNotFound)) {
		t.Fatalf("应返回 not_found 工具错误，got %+v", res)
	}
}

// handler panic → 服务不死（隔离降级），后续调用仍正常。
func TestBlackBoxPanicServerSurvives(t *testing.T) {
	panicker := validTool("panicker")
	panicker.Handler = func(_ context.Context, _ map[string]any) *Result { panic("boom") }
	c, done := startBlackBox(t, panicker, validTool("get_nodes"))
	defer done()

	res := callTool(t, c, "panicker", nil)
	if !res.IsError || !strings.Contains(resultText(res), string(ErrClassInternal)) {
		t.Fatalf("panic 应转 internal 错误，got %+v", res)
	}
	// 服务存活：另一个工具仍可正常调用（顺带覆盖带参调用路径）。
	res2 := callTool(t, c, "get_nodes", map[string]any{"limit": 10})
	if res2.IsError {
		t.Fatalf("panic 后服务应存活，get_nodes 却被影响: %+v", res2)
	}
}

// tools/list 黑盒：暴露的 schema 与元数据（annotations）契约正确。
func TestBlackBoxListToolsContract(t *testing.T) {
	c, done := startBlackBox(t, validTool("get_nodes"))
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
	if tool.Name != "get_nodes" || tool.Description == "" {
		t.Errorf("工具名/描述缺失: %+v", tool)
	}
	// schema 透出：客户端把 inputSchema JSON 解析进结构化 InputSchema 字段（不是 RawInputSchema）。
	if tool.InputSchema.Type != "object" {
		t.Errorf("应透出 inputSchema type=object，got %+v", tool.InputSchema)
	}
	// 元数据 → annotations 映射。
	if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
		t.Errorf("R0 工具应有 readOnlyHint=true")
	}
	if tool.Annotations.IdempotentHint == nil || !*tool.Annotations.IdempotentHint {
		t.Errorf("应有 idempotentHint=true")
	}
}
