package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server 把 Registry 暴露为一个 MCP server（mcp-go 实现），供 kagent ToolServer 发现（T1.1 判据：
// 空壳 server 注册成功且被 kagent 发现）。各 mcp-servers/* 进程 import 本框架完成注册与启动。
type Server struct {
	registry *Registry
	name     string
	version  string
}

// NewServer 用给定的注册表构造一个 MCP server。
func NewServer(name, version string, registry *Registry) *Server {
	return &Server{registry: registry, name: name, version: version}
}

// toMCPTool 把 aegis Tool 映射为 mcp.Tool：RawInputSchema 承载任意 JSON Schema；
// 元数据五字段既映射到 MCP annotations（readOnly/idempotent/destructive hint），
// 又完整写入 _meta["aegis_tool_metadata"] 供闸门分级复核读取。
func toMCPTool(t Tool) (mcp.Tool, error) {
	schemaBytes, err := json.Marshal(t.InputSchema)
	if err != nil {
		return mcp.Tool{}, fmt.Errorf("工具 %s 的 inputSchema 序列化失败: %w", t.Name, err)
	}
	readOnly := t.Metadata.RiskHint == RiskR0
	idempotent := t.Metadata.Idempotent
	destructive := t.Metadata.RiskHint == RiskR2 || t.Metadata.RiskHint == RiskR3
	tool := mcp.NewTool(t.Name,
		mcp.WithDescription(t.Description),
		mcp.WithToolAnnotation(mcp.ToolAnnotation{
			Title:           t.Name,
			ReadOnlyHint:    &readOnly,
			IdempotentHint:  &idempotent,
			DestructiveHint: &destructive,
		}),
	)
	// NewTool 默认填了 InputSchema{Type:"object"}；与 RawInputSchema 同设会导致 MarshalJSON 冲突报错，
	// 故清零默认 InputSchema，只用 RawInputSchema 承载任意 JSON Schema。
	tool.InputSchema = mcp.ToolInputSchema{}
	tool.RawInputSchema = schemaBytes
	metaJSON, _ := json.Marshal(t.Metadata)
	tool.Meta = &mcp.Meta{AdditionalFields: map[string]any{
		"aegis_tool_metadata": json.RawMessage(metaJSON),
	}}
	return tool, nil
}

// handlerFor 生成 MCP 工具 handler：把调用转发给 Registry.Invoke（经统一中间件链），结果转 MCP 协议。
func (s *Server) handlerFor(t Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res := s.registry.Invoke(ctx, t.Name, req.GetArguments())
		if res.Err != nil {
			// 协议错误与工具错误都以 MCP IsError 结果返回，类别/可重试标记在 JSON 里（§4 错误协议）。
			return mcp.NewToolResultError(sprintJSON(res.Err)), nil
		}
		return mcp.NewToolResultText(sprintJSON(res)), nil
	}
}

// build 把所有已注册工具挂进一个 mcp-go MCPServer。
func (s *Server) build() (*server.MCPServer, error) {
	mcpSrv := server.NewMCPServer(s.name, s.version)
	for _, t := range s.registry.List() {
		mcpTool, err := toMCPTool(t)
		if err != nil {
			return nil, err
		}
		mcpSrv.AddTool(mcpTool, s.handlerFor(t))
	}
	return mcpSrv, nil
}

// ServeStdio 经 stdio 提供 MCP 服务（kagent ToolServer 以 stdio 方式拉起/发现的进程形态）。
func (s *Server) ServeStdio(ctx context.Context) error {
	mcpSrv, err := s.build()
	if err != nil {
		return err
	}
	return server.NewStdioServer(mcpSrv).Listen(ctx, os.Stdin, os.Stdout)
}

// ServeHTTP 经 Streamable HTTP 提供 MCP 服务（集群内常驻进程形态，kagent 经 Service 发现）。
func (s *Server) ServeHTTP(addr string) error {
	mcpSrv, err := s.build()
	if err != nil {
		return err
	}
	return server.NewStreamableHTTPServer(mcpSrv).Start(addr)
}

// HTTPHandler 返回可挂进任意 http.Server / httptest 的 handler（StreamableHTTP 传输），供测试与嵌入装配。
func (s *Server) HTTPHandler() (http.Handler, error) {
	mcpSrv, err := s.build()
	if err != nil {
		return nil, err
	}
	return server.NewStreamableHTTPServer(mcpSrv), nil
}
