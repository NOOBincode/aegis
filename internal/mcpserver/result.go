package mcpserver

import (
	"encoding/json"
	"fmt"
)

// sprintJSON 把任意结果序列化为紧凑 JSON 字符串（用于包裹与审计摘要）；失败回退 fmt。
func sprintJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// 结构化返回（internal/mcpserver/README.md §4）：统一返回体分三段 data/meta/error。
// 读工具强制分页与上限（单次 ≤500 行日志 / ≤200 对象，设计文档 §4.1）。

// Meta 承载返回的元信息：截断/包裹标记、分页游标、剩余预算。
type Meta struct {
	// Truncated 结果是否被上限截断（F5：超限读返回 429 + 分页引导）。
	Truncated bool `json:"truncated"`
	// Wrapped 是否已做 <untrusted_cluster_data> 包裹（I4）。
	Wrapped bool `json:"wrapped"`
	// NextCursor 分页游标；空表示无更多。
	NextCursor string `json:"next_cursor,omitempty"`
	// RemainingBudget 会话剩余预算提示（F4），供 Agent 自决收敛。
	RemainingBudget *Budget `json:"remaining_budget,omitempty"`
}

// Budget 是读路径双预算的剩余量快照（F4/F5）。
type Budget struct {
	ToolCallsLeft int `json:"tool_calls_left,omitempty"`
	TokensLeft    int `json:"tokens_left,omitempty"`
}

// Result 是工具调用的统一返回体。Data 为业务结果；Err 非空表示失败（见错误协议）。
type Result struct {
	Data any    `json:"data,omitempty"`
	Meta Meta   `json:"meta"`
	Err  *Error `json:"error,omitempty"`
}

// ErrorClass 区分错误类别，供审计与失效语义判断（§5）。
type ErrorClass string

// 工具调用错误类别常量（§4 错误协议：协议错误 vs 工具错误不混用）。
const (
	// 协议错误（调用本身不成立，不触达 handler）。

	ErrClassSchema      ErrorClass = "schema_validation" // 参数 schema 校验失败
	ErrClassEnvelope    ErrorClass = "envelope_invalid"  // 信封验签/序号乱序（F15）
	ErrClassUnknownTool ErrorClass = "unknown_tool"      // 未注册工具名
	ErrClassRateLimited ErrorClass = "rate_limited"      // 双预算超限（F4/F5）

	// 工具错误（调用成立但执行失败）。

	ErrClassNotFound   ErrorClass = "not_found"      // 集群对象不存在
	ErrClassTimeout    ErrorClass = "timeout"        // 超时（I10）
	ErrClassDownstream ErrorClass = "downstream_err" // apiserver 返回错误
	ErrClassInternal   ErrorClass = "internal"       // 框架内 panic 已 recover（隔离降级）
)

// Error 是结构化错误。Protocol=true 表示协议错误（调用不成立，未触达 handler）。
type Error struct {
	Class    ErrorClass `json:"class"`
	Message  string     `json:"message"`
	Protocol bool       `json:"protocol"` // 协议错误 true / 工具错误 false
	// Retryable 仅工具错误有意义：是否可安全重试（由 handler 语义决定）。
	Retryable bool `json:"retryable,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("[%s] %s", e.Class, e.Message)
}

// newProtocolError 构造协议错误（不触达 handler）。
func newProtocolError(class ErrorClass, format string, args ...any) *Error {
	return &Error{Class: class, Message: fmt.Sprintf(format, args...), Protocol: true}
}

// NewToolError 构造工具错误（供 handler 返回；retryable 由 handler 语义决定）。
// 注意：写工具的任何工具错误一律向上抛给闸门状态机，不得被框架解释为降级放行（§4）。
func NewToolError(class ErrorClass, retryable bool, format string, args ...any) *Error {
	return &Error{Class: class, Message: fmt.Sprintf(format, args...), Protocol: false, Retryable: retryable}
}
