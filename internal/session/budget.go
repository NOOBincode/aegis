package session

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Limits 是 M1 的最小会话预算（GuardrailPolicy 的 M2 收敛前形态）。
// 零值表示不限制；负数视为非法（NewManager 归一为 0）。
type Limits struct {
	MaxToolCalls int
	MaxTokens    int
	QPS          int
	QPSWindow    time.Duration
}

// Usage 是某个会话的当前用量（审计/错误信息用，不做决策）。
type Usage struct {
	SessionID string
	ToolCalls int
	Tokens    int
}

// Error 是预算耗尽的可识别错误；Kind 为 tool_calls|tokens|qps。
type Error struct {
	Kind      string
	SessionID string
	Limit     int
	Used      int
}

func (e *Error) Error() string {
	return fmt.Sprintf("会话预算耗尽（%s）：session=%s used=%d limit=%d", e.Kind, e.SessionID, e.Used, e.Limit)
}

type state struct {
	toolCalls int
	tokens    int
}

// QPSLimiter 是 apiserver/工具调用的 QPS 窗口端口；实现由 internal/breaker 提供，
// 本上下文不 import breaker domain（跨上下文经接口协作）。
type QPSLimiter interface {
	Allow(now time.Time) bool
}

// Manager 是并发安全的会话预算聚合器：按 sessionID 计数 tool calls / tokens，
// 并可选地接入 QPS 窗口。M1 进程内状态；M2 可换 Redis 实现（端口不变）。
type Manager struct {
	mu       sync.Mutex
	limits   Limits
	qps      QPSLimiter
	sessions map[string]*state
	now      func() time.Time
}

// NewManager 构造预算管理器。limits 的负值按 0（不限制）处理。
func NewManager(limits Limits) *Manager {
	if limits.MaxToolCalls < 0 {
		limits.MaxToolCalls = 0
	}
	if limits.MaxTokens < 0 {
		limits.MaxTokens = 0
	}
	if limits.QPS < 0 {
		limits.QPS = 0
	}
	if limits.QPSWindow <= 0 {
		limits.QPSWindow = time.Second
	}
	return &Manager{
		limits:   limits,
		sessions: make(map[string]*state),
		now:      time.Now,
	}
}

// WithQPS 注入 QPS 窗口（nil=不限 QPS）。
func (m *Manager) WithQPS(q QPSLimiter) *Manager {
	m.qps = q
	return m
}

// Limits 返回实际生效的预算（测试/错误信息用）。
func (m *Manager) Limits() Limits { return m.limits }

// Before 在工具执行前扣减一次 tool call 预算并检查 QPS；超限返回 *Error。
// 并发下由 mutex 串行化，调用方不能通过并发绕过计数。
func (m *Manager) Before(_ context.Context, sessionID, tool string) error {
	if sessionID == "" {
		// 无 session 的调用也必须计数，不能成为绕过通道；统一归入 anonymous。
		sessionID = "anonymous"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.qps != nil && !m.qps.Allow(m.now()) {
		return &Error{Kind: "qps", SessionID: sessionID, Limit: m.limits.QPS}
	}
	st := m.sessions[sessionID]
	if st == nil {
		st = &state{}
		m.sessions[sessionID] = st
	}
	if m.limits.MaxToolCalls > 0 && st.toolCalls >= m.limits.MaxToolCalls {
		return &Error{Kind: "tool_calls", SessionID: sessionID, Limit: m.limits.MaxToolCalls, Used: st.toolCalls}
	}
	// token 预算在进入下一次调用前检查：上一次结果可能刚把预算推过顶，
	// 本次调用应被 429 拦住，而不是让 Agent 继续无限取证。
	if m.limits.MaxTokens > 0 && st.tokens >= m.limits.MaxTokens {
		return &Error{Kind: "tokens", SessionID: sessionID, Limit: m.limits.MaxTokens, Used: st.tokens}
	}
	st.toolCalls++
	_ = tool // 预留按工具维度拆分；M1 只做会话维度
	return nil
}

// After 在工具执行后累计 token 估算；超限返回 *Error（调用已发生，但结果不再放行）。
func (m *Manager) After(_ context.Context, sessionID string, _ string, tokens int) error {
	if sessionID == "" {
		sessionID = "anonymous"
	}
	if tokens < 0 {
		tokens = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.sessions[sessionID]
	if st == nil {
		st = &state{}
		m.sessions[sessionID] = st
	}
	st.tokens += tokens
	if m.limits.MaxTokens > 0 && st.tokens > m.limits.MaxTokens {
		return &Error{Kind: "tokens", SessionID: sessionID, Limit: m.limits.MaxTokens, Used: st.tokens}
	}
	return nil
}

// Usage 返回某个会话的当前用量（不存在时为零值）。
func (m *Manager) Usage(sessionID string) Usage {
	if sessionID == "" {
		sessionID = "anonymous"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u := Usage{SessionID: sessionID}
	if st := m.sessions[sessionID]; st != nil {
		u.ToolCalls = st.toolCalls
		u.Tokens = st.tokens
	}
	return u
}

// Reset 清理一个会话（测试/会话结束时用）；生产可换 TTL 存储。
func (m *Manager) Reset(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
}
