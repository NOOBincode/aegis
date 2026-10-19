package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/aegis-dev/aegis/internal/redact"
	"github.com/aegis-dev/aegis/internal/sanitize"
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

// BudgetLimiter 是会话预算端口（T1.4）。实现由 internal/session 提供；
// 框架只负责调用，不把预算规则写进 MCP 层（I1 职责边界）。
type BudgetLimiter interface {
	Before(ctx context.Context, sessionID, tool string) error
	After(ctx context.Context, sessionID, tool string, tokens int) error
}

// defaultChain 是 aegis 工具的统一中间件链（M1 形态）。
func defaultChain(budget BudgetLimiter) []Middleware {
	return []Middleware{
		authnEnvelopeMiddleware,     // 1. 鉴权信封（M2 T2.11 落 mTLS+序号；M1 为接缝）
		rateLimitMiddleware(budget), // 2. 限流（T1.4 双预算；nil=不限制）
		sanitizeMiddleware,          // 3. sanitize 包裹（I4 M1 形态：标记+留痕）
		auditMiddleware,             // 4. 审计埋点（I6；M1 落结构化日志，供 internal/audit 组装）
		timeoutMiddleware,           // 5. 超时控制（I10；以 metadata.timeout_ms 为上界）
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

// rateLimitMiddleware 限流（T1.4）：每次调用按 session 聚合 tool calls/tokens，
// 预算耗尽返回协议错误 ErrClassRateLimited（429）。After 把本次 token 估算计入；
// 若本次把预算推过顶，本次结果仍返回（保留当前结论），下一次 Before 会被拦。
func rateLimitMiddleware(budget BudgetLimiter) Middleware {
	return func(tool Tool, next Handler) Handler {
		return func(ctx context.Context, args map[string]any) *Result {
			if budget == nil {
				return next(ctx, args)
			}
			sid := SessionID(ctx)
			if err := budget.Before(ctx, sid, tool.Name); err != nil {
				return &Result{Err: newProtocolError(ErrClassRateLimited, "会话预算耗尽：%v", err)}
			}
			res := next(ctx, args)
			if res == nil {
				res = &Result{}
			}
			tokens := estimateTokens(args) + estimateTokens(res.Data)
			if err := budget.After(ctx, sid, tool.Name, tokens); err != nil {
				// 不丢当前结论：结果照常返回，但显式标记预算已耗尽，下一次调用会被 Before 拦截。
				res.Meta.RemainingBudget = &Budget{}
				slog.Warn("mcp.budget.exhausted", "session_id", sid, "tool_name", tool.Name, "error", err)
			}
			return res
		}
	}
}

// estimateTokens 是读路径的保守 token 估算：JSON 字节数 / 4，至少 1。
// 不做真实 tokenizer（避免依赖/成本）；预算用于防失控，不用于计费。
func estimateTokens(v any) int {
	if v == nil {
		return 0
	}
	n := len(sprintJSON(v))
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// sanitizeMiddleware 数据隔离包裹（I4 的 M1 形态，T1.5）：工具出口数据统一包裹
// <untrusted_cluster_data> 分隔符并标记 Meta.Wrapped。模式检测与升 R2 在 M2 收敛进闸门（T2.7）。
func sanitizeMiddleware(_ Tool, next Handler) Handler {
	return func(ctx context.Context, args map[string]any) *Result {
		res := next(ctx, args)
		if res == nil {
			res = &Result{}
		}
		// 只包裹业务数据；错误结果（Data=nil）不产出空包裹，也不误标 wrapped=true。
		if res.Data != nil {
			raw := sprintJSON(res.Data)
			hit := sanitize.DetectInjection(raw)
			wrapped, redacted := sanitize.WrapWithStats(res.Data)
			res.Data = wrapped
			res.Meta.Wrapped = true
			res.Meta.Redacted += redacted
			res.Meta.InjectionHit = res.Meta.InjectionHit || hit.Matched
			res.Meta.InjectionPatterns = append(res.Meta.InjectionPatterns, hit.Patterns...)
		}
		return res
	}
}

// untrustedOpen/untrustedClose 是 I4 的隔离分隔符（唯一实现见 internal/sanitize；
// 这里保留别名供既有测试/审计断言引用，不在本包重复字面量）。
const (
	untrustedOpen  = sanitize.Open
	untrustedClose = sanitize.Close
)

// wrapUntrusted 把集群返回数据包裹为不可信标记（转义防伪造边界见 internal/sanitize）。
func wrapUntrusted(data any) any {
	return sanitize.Wrap(data)
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
		// 审计只落"元数据 + 摘要"：参数与结果摘要先做敏感信息脱敏，绝不落原文；
		// 外部 AI/日志排查需要原文时，走 DecisionRecord 的受控拷贝，而不是 slog。
		argsPreview, _ := redact.String(sprintJSON(args))
		if len(argsPreview) > 512 {
			argsPreview = argsPreview[:512] + "…"
		}
		rawResult := sprintJSON(resData(res))
		sum := sha256.Sum256([]byte(rawResult))
		redactedCount := redact.Count(rawResult)
		resultPreview, _ := redact.String(rawResult)
		if len(resultPreview) > 1024 {
			resultPreview = resultPreview[:1024] + "…"
		}
		inj := sanitize.DetectInjection(rawResult)
		slog.Info("mcp.tool.invoke",
			"tool_name", tool.Name,
			"risk_hint", string(tool.Metadata.RiskHint),
			"latency_ms", latency,
			"timeout_ms", tool.Metadata.TimeoutMs,
			"error_class", ec,
			"session_id", SessionID(ctx),
			"args_preview", argsPreview,
			"result_sha256", hex.EncodeToString(sum[:]),
			"result_bytes", len(rawResult),
			"redacted_count", redactedCount,
			"result_preview", resultPreview,
			"injection_hit", inj.Matched,
			"injection_patterns", inj.Patterns,
			"audit", "mcp.tool",
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

// resData 取 Result 的业务数据（nil 安全），供审计摘要/哈希使用。
func resData(r *Result) any {
	if r == nil {
		return nil
	}
	return r.Data
}
