package mcpserver

import "context"

// sessionKey 是 MCP HTTP 传输注入的会话标识。mcp-go 的 WithHTTPContextFunc
// 在解析 Mcp-Session-Id 后调用，我们把它写入 ctx，审计中间件即可把同一次
// Agent 调用的多个 tool call 串起来（每个 server 的 session id 不同，所以
// 审计回放还需要时间窗/调用方 trace 归并；至少单 server 内可关联）。
type sessionKey struct{}

// WithSessionID 返回带 session id 的 ctx（供 HTTP 传输层使用）。
func WithSessionID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionKey{}, id)
}

// SessionID 读取 ctx 中的 session id；没有则返回空串。
func SessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}
