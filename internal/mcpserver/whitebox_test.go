package mcpserver

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- 白盒：元数据校验边界 ----------

func TestMetadataValidateBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ToolMetadata)
		ok     bool
	}{
		{"risk_hint R1 合法", func(m *ToolMetadata) { m.RiskHint = RiskR1 }, true},
		{"risk_hint R2 合法", func(m *ToolMetadata) { m.RiskHint = RiskR2 }, true},
		{"risk_hint R3 合法", func(m *ToolMetadata) { m.RiskHint = RiskR3 }, true},
		{"risk_hint 小写非法", func(m *ToolMetadata) { m.RiskHint = "r0" }, false},
		{"risk_hint 带空格非法", func(m *ToolMetadata) { m.RiskHint = " R0" }, false},
		{"timeout=1 边界合法", func(m *ToolMetadata) { m.TimeoutMs = 1 }, true},
		{"timeout 极大值合法", func(m *ToolMetadata) { m.TimeoutMs = 1 << 30 }, true},
		{"nodes 为负", func(m *ToolMetadata) { m.EstBlastRadius.Nodes = -1 }, false},
		{"namespaces 为负", func(m *ToolMetadata) { m.EstBlastRadius.Namespaces = -1 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validMeta()
			tc.mutate(&m)
			if err := m.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate()=%v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// ---------- 白盒：注册校验顺序与并发安全 ----------

func TestRegisterValidationOrder(t *testing.T) {
	r := NewRegistry()
	// 名称空最先被拦（即使其他也缺）
	if err := r.Register(Tool{}); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("空名称应最先被拦，got %v", err)
	}
	// 有名称无 handler
	if err := r.Register(Tool{Name: "x"}); err == nil || !strings.Contains(err.Error(), "handler") {
		t.Fatalf("缺 handler 应被拦，got %v", err)
	}
	// 有 handler 无 schema
	if err := r.Register(Tool{Name: "x", Handler: okHandler}); err == nil || !strings.Contains(err.Error(), "inputSchema") {
		t.Fatalf("缺 schema 应被拦，got %v", err)
	}
	// 有 schema 无合法元数据
	if err := r.Register(Tool{Name: "x", Handler: okHandler, InputSchema: map[string]any{"type": "object"}}); err == nil {
		t.Fatal("缺合法元数据应被拦")
	}
}

func TestMustRegisterPanics(t *testing.T) {
	r := NewRegistry()
	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister 对非法工具应 panic（启动期 fail-fast）")
		}
	}()
	r.MustRegister(Tool{Name: "bad"}) // 缺 handler/schema/metadata
}

func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(validTool("tool_a"))
	var wg sync.WaitGroup
	// 并发注册不同名工具 + 并发调用，验证无 data race / 死锁 / panic。
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			_ = r.Register(validTool(strings.Repeat("t", n%5+1) + string(rune('a'+n%26)) + string(rune('0'+n%10))))
		}(i)
		go func() { defer wg.Done(); _ = r.Invoke(context.Background(), "tool_a", nil) }()
	}
	wg.Wait()
	if r.Len() < 1 {
		t.Fatal("并发后注册表应有至少一个工具")
	}
}

// ---------- 白盒：中间件链顺序（顺序即语义） ----------

func TestMiddlewareChainOrder(t *testing.T) {
	var order []string
	probe := func(tag string) Middleware {
		return func(_ Tool, next Handler) Handler {
			return func(ctx context.Context, args map[string]any) *Result {
				order = append(order, tag)
				return next(ctx, args)
			}
		}
	}
	tool := validTool("probe")
	mws := []Middleware{probe("1-authn"), probe("2-ratelimit"), probe("3-sanitize"), probe("4-audit"), probe("5-timeout")}
	h := chain(tool, mws, func(_ context.Context, _ map[string]any) *Result { order = append(order, "handler"); return &Result{} })
	h(context.Background(), nil)
	want := "1-authn,2-ratelimit,3-sanitize,4-audit,5-timeout,handler"
	if strings.Join(order, ",") != want {
		t.Fatalf("中间件顺序错误: got %v want %v", order, want)
	}
}

// ---------- 白盒：sanitize 包裹各数据类型 ----------

func TestSanitizeWrapTypes(t *testing.T) {
	cases := []struct {
		name string
		data any
		want string // 期望包裹后的子串
	}{
		{"字符串", "pods: pod-a", "pod-a"},
		{"map", map[string]any{"k": "v"}, `"k":"v"`},
		{"[]byte", []byte("raw-bytes"), "raw-bytes"},
		{"nil 不包裹", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := validTool("t")
			tool.Handler = func(_ context.Context, _ map[string]any) *Result { return &Result{Data: tc.data} }
			r := NewRegistry()
			_ = r.Register(tool)
			res := r.Invoke(context.Background(), "t", nil)
			if tc.data == nil {
				if res.Data != nil {
					t.Fatalf("nil 数据不应被包裹，got %v", res.Data)
				}
				return
			}
			s, _ := res.Data.(string)
			if !strings.Contains(s, untrustedOpen) || !strings.Contains(s, tc.want) {
				t.Fatalf("包裹结果应含分隔符与数据，got %q", s)
			}
		})
	}
}

// ---------- 白盒：错误协议分类 ----------

func TestErrorProtocolClassification(t *testing.T) {
	// 协议错误：Protocol=true，不可重试语义由调用方定。
	pe := newProtocolError(ErrClassSchema, "bad arg")
	if !pe.Protocol || pe.Retryable {
		t.Errorf("协议错误 Protocol 应为 true 且默认不可重试，got %+v", pe)
	}
	// 工具错误：Protocol=false，可带可重试标记。
	te := NewToolError(ErrClassTimeout, true, "超时")
	if te.Protocol || !te.Retryable {
		t.Errorf("工具错误 Protocol 应为 false 且可重试，got %+v", te)
	}
	// Error() 字符串形态
	if got := te.Error(); !strings.Contains(got, "timeout") {
		t.Errorf("Error() 应含 error class，got %q", got)
	}
}

// ---------- 白盒：超时边界 + handler 忽略 ctx 的行为 ----------

func TestTimeoutBoundaryAndIgnoreCtx(t *testing.T) {
	// handler 无视 ctx 一直跑：框架仍须在 timeout_ms 上界返回（结果丢弃，goroutine 随 handler 返回而结束）。
	r := NewRegistry()
	tool := validTool("stubborn")
	tool.Metadata.TimeoutMs = 30
	handlerReturned := make(chan struct{})
	tool.Handler = func(_ context.Context, _ map[string]any) *Result {
		time.Sleep(80 * time.Millisecond) // 超过 timeout，但会返回（非永久挂起）
		close(handlerReturned)
		return &Result{Data: "late"}
	}
	_ = r.Register(tool)
	res := r.Invoke(context.Background(), "stubborn", nil)
	if res.Err == nil || res.Err.Class != ErrClassTimeout {
		t.Fatalf("超时上界应返回 timeout 错误，got %+v", res.Err)
	}
	// 迟到的 handler 结果不panic、不阻塞主路径（goroutine 最终完成）。
	<-handlerReturned
}

// ---------- 白盒：sprintJSON 不可序列化回退 ----------

func TestSprintJSONFallback(t *testing.T) {
	// chan 不可 JSON 序列化 → 回退 fmt。
	got := sprintJSON(make(chan int))
	if got == "" {
		t.Fatal("不可序列化类型应回退 fmt.Sprintf 非空")
	}
}

// ---------- 白盒：handler 返回 nil 的兜底 ----------

func TestNilResultHandler(t *testing.T) {
	r := NewRegistry()
	tool := validTool("nilret")
	tool.Handler = func(_ context.Context, _ map[string]any) *Result { return nil }
	_ = r.Register(tool)
	// 不应 panic；sanitize 兜底为空 Result。
	res := r.Invoke(context.Background(), "nilret", nil)
	if res == nil {
		t.Fatal("handler 返回 nil 时框架应兜底为非 nil Result")
	}
}
