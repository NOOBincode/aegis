package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Handler 是工具的执行体：参数校验 + 执行 + 结果结构化返回，不含任何放行判断（§4.1 职责边界）。
// 返回 *Result（Err 字段承载工具错误）；框架在中间件链里负责包裹/限流/埋点/超时。
type Handler func(ctx context.Context, args map[string]any) *Result

// Tool 是一个可注册的 aegis 工具。
type Tool struct {
	// Name 工具名（唯一，注册表键）。
	Name string
	// Description 给 LLM 的工具说明。
	Description string
	// InputSchema 入参 JSON Schema（I3：强 schema 校验，窄接口）。nil 视为缺失。
	InputSchema map[string]any
	// Metadata 元数据五字段（注册期强校验）。
	Metadata ToolMetadata
	// Handler 执行体。
	Handler Handler
}

// Registry 是工具注册表。注册期即做强校验（非运行期发现），缺一字段即拒绝（T1.1 判据）。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register 注册一个工具。校验顺序：名称/执行体 → schema → 元数据五字段。任一不过即拒绝并给出缺失项。
func (r *Registry) Register(t Tool) error {
	if t.Name == "" {
		return fmt.Errorf("注册失败：tool.name 为空")
	}
	if t.Handler == nil {
		return fmt.Errorf("注册失败 [%s]：handler 为空", t.Name)
	}
	if t.InputSchema == nil {
		return fmt.Errorf("注册失败 [%s]：inputSchema 缺失（I3 强 schema 校验要求窄接口）", t.Name)
	}
	if err := t.Metadata.Validate(); err != nil {
		return fmt.Errorf("注册失败 [%s]：%w", t.Name, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[t.Name]; dup {
		return fmt.Errorf("注册失败 [%s]：工具名重复", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// MustRegister 同 Register，但失败即 panic——仅供进程启动期装配使用（启动期失败 = fail-fast）。
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get 按名取工具。
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List 返回全部已注册工具（按名排序，保证确定性输出供审计/发现）。
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Len 返回已注册工具数。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// Invoke 经统一中间件链调用一个已注册工具。未注册工具名返回协议错误（不触达 handler，§4 错误协议）。
// 放行/分级判断不在此——框架只做通道安全、限流、包裹、埋点、超时（I1/I3）。
func (r *Registry) Invoke(ctx context.Context, name string, args map[string]any) *Result {
	tool, ok := r.Get(name)
	if !ok {
		return &Result{Err: newProtocolError(ErrClassUnknownTool, "未注册的工具名 %q", name)}
	}
	return chain(tool, defaultChain(), tool.Handler)(ctx, args)
}
