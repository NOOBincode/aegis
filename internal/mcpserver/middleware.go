package mcpserver

import (
	"context"
	"log/slog"
	"time"
)

// 统一中间件链（internal/mcpserver/README.md §4）：每个工具调用固定顺序经过，顺序即语义。
// 鉴权信封 → 限流 → sanitize 包裹 → 审计埋点 → 超时控制。
// M1 落地：timeout / sanitize 包裹 / 审计日志为实，鉴权信封与双预算限流为接缝（T2.11 / T1.4 收敛）。

// Middleware 包装一个工具的 Handler。拿到 Tool 是为了读其元数据（timeout_ms 等）。
type Middleware func(tool Tool, next Handler) Handler

// chain 按声明顺序组装中间件：chain[0] 在最外层（最先进入）。
func chain(tool Tool, mws []Middleware, final Handler) Handler {
	h := final
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](tool, h)
	}
	return h
}

// defaultChain 是 aegis 工具的统一中间件链（M1 形态）。
func defaultChain() []Middleware {
	return []Middleware{
		authnEnvelopeMiddleware, // 1. 鉴权信封（M2 T2.11 落 mTLS+序号；M1 为接缝）
		rateLimitMiddleware,     // 2. 限流（T1.4 落双预算；M1 为接缝）
		sanitizeMiddleware,      // 3. sanitize 包裹（I4 M1 形态：标记+留痕）
		auditMiddleware,         // 4. 审计埋点（I6；M1 落结构化日志，供 internal/audit 组装）
		timeoutMiddleware,       // 5. 超时控制（I10；以 metadata.timeout_ms 为上界）
	}
}

// authnEnvelopeMiddleware 鉴权信封校验接缝。M2 T2.11 落 mTLS + 签名信封 + 会话内序号（F15）。
// M1 暂为直通：通道安全在 M2 收敛，但接缝在此固定，工具代码不变。
func authnEnvelopeMiddleware(_ Tool, next Handler) Handler {
	return func(ctx context.Context, args map[string]any) *Result {
		// TODO(T2.11): 验签 + 序号乱序/重放拒绝。
		return next(ctx, args)
	}
}

// rateLimitMiddleware 限流接缝。T1.4 落会话工具数/token + apiserver QPS 双预算（F4/F5）。
// 超限返回协议错误 ErrClassRateLimited（429 + 分页引导）。M1 暂为直通。
func rateLimitMiddleware(_ Tool, next Handler) Handler {
	return func(ctx context.Context, args map[string]any) *Result {
		// TODO(T1.4): 双预算检查，耗尽返回协议错误并告知预算口径。
		return next(ctx, args)
	}
}

// sanitizeMiddleware 数据隔离包裹（I4 的 M1 形态，T1.5）：工具出口数据统一包裹
// <untrusted_cluster_data> 分隔符并标记 Meta.Wrapped。模式检测与升 R2 在 M2 收敛进闸门（T2.7）。
func sanitizeMiddleware(_ Tool, next Handler) Handler {
	return func(ctx context.Context, args map[string]any) *Result {
		res := next(ctx, args)
		if res == nil {
			res = &Result{}
		}
		res.Data = wrapUntrusted(res.Data)
		res.Meta.Wrapped = true
		return res
	}
}

// untrustedOpen/untrustedClose 是 I4 的隔离分隔符（T1.5 与 internal/sanitize 对齐全局唯一）。
const (
	untrustedOpen  = "<untrusted_cluster_data>"
	untrustedClose = "</untrusted_cluster_data>"
)

// wrapUntrusted 把集群返回数据包裹为不可信标记。nil 不包裹。
func wrapUntrusted(data any) any {
	if data == nil {
		return nil
	}
	return untrustedOpen + toString(data) + untrustedClose
}

func toString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return sprintJSON(v)
	}
}

// auditMiddleware 审计埋点（I6）：记录调用方、工具名、结果摘要、延迟、错误类别。
// M1 落结构化日志；后续由 internal/audit 组装进 DecisionRecord。
func auditMiddleware(tool Tool, next Handler) Handler {
	return func(ctx context.Context, args map[string]any) *Result {
		start := time.Now()
		res := next(ctx, args)
		latency := time.Since(start).Milliseconds()
		ec := ""
		if res != nil && res.Err != nil {
			ec = string(res.Err.Class)
		}
		slog.Info("mcp.tool.invoke",
			"tool_name", tool.Name,
			"risk_hint", string(tool.Metadata.RiskHint),
			"latency_ms", latency,
			"timeout_ms", tool.Metadata.TimeoutMs,
			"error_class", ec,
		)
		return res
	}
}

// timeoutMiddleware 超时控制（I10）：以 metadata.timeout_ms 为上界包住执行；超时即失败返回。
// 同时 recover handler panic（§5：进程不死于单次调用，转 internal 工具错误）。
func timeoutMiddleware(tool Tool, next Handler) Handler {
	return func(ctx context.Context, args map[string]any) *Result {
		t := tool.Metadata.TimeoutMs
		ctx, cancel := context.WithTimeout(ctx, time.Duration(t)*time.Millisecond)
		defer cancel()
		resCh := make(chan *Result, 1)
		go func() { resCh <- safeInvoke(ctx, tool, next, args) }()
		select {
		case res := <-resCh:
			return res
		case <-ctx.Done():
			return &Result{
				Meta: Meta{},
				Err:  NewToolError(ErrClassTimeout, true, "工具 %s 执行超时（>%dms）", tool.Name, t),
			}
		}
	}
}

// safeInvoke recover handler panic，转为 internal 工具错误（隔离降级，§5）。
func safeInvoke(ctx context.Context, tool Tool, h Handler, args map[string]any) (res *Result) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("mcp.tool.panic", "tool_name", tool.Name, "panic", r)
			res = &Result{Err: NewToolError(ErrClassInternal, false, "工具 %s 内部错误（已隔离）", tool.Name)}
		}
	}()
	return h(ctx, args)
}
