package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 合法基线元数据：各用例在此基础上破坏单一字段。
func validMeta() ToolMetadata {
	return ToolMetadata{
		RiskHint:       RiskR0,
		Idempotent:     true,
		Reversible:     false,
		EstBlastRadius: BlastRadius{Pods: 0},
		TimeoutMs:      1000,
	}
}

// T1.1 判据：元数据五字段缺一/非法即校验失败。
func TestToolMetadataValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ToolMetadata)
		wantErr bool
	}{
		{"valid", func(*ToolMetadata) {}, false},
		{"risk_hint 缺失", func(m *ToolMetadata) { m.RiskHint = "" }, true},
		{"risk_hint 非法值", func(m *ToolMetadata) { m.RiskHint = "R9" }, true},
		{"timeout_ms 为零", func(m *ToolMetadata) { m.TimeoutMs = 0 }, true},
		{"timeout_ms 为负", func(m *ToolMetadata) { m.TimeoutMs = -5 }, true},
		{"blast_radius 为负", func(m *ToolMetadata) { m.EstBlastRadius.Pods = -1 }, true},
		// bool 零值 false 合法（idempotent/reversible 可false）
		{"bool 零值合法", func(m *ToolMetadata) { m.Idempotent = false; m.Reversible = false }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validMeta()
			tc.mutate(&m)
			err := m.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func okHandler(_ context.Context, _ map[string]any) *Result {
	return &Result{Data: "ok-data"}
}

func validTool(name string) Tool {
	return Tool{
		Name:        name,
		Description: "test tool",
		InputSchema: map[string]any{"type": "object"},
		Metadata:    validMeta(),
		Handler:     okHandler,
	}
}

// T1.1 判据：空壳 server 注册成功；缺元数据的工具被拒绝注册。
func TestRegistryRegister(t *testing.T) {
	t.Run("合法工具注册成功", func(t *testing.T) {
		r := NewRegistry()
		if err := r.Register(validTool("k8s_get_pods")); err != nil {
			t.Fatalf("Register 失败: %v", err)
		}
		if r.Len() != 1 {
			t.Fatalf("Len()=%d, want 1", r.Len())
		}
	})

	t.Run("缺元数据被拒绝注册", func(t *testing.T) {
		r := NewRegistry()
		bad := validTool("bad_tool")
		bad.Metadata = ToolMetadata{} // 全空：risk_hint 空 + timeout_ms 0
		if err := r.Register(bad); err == nil {
			t.Fatal("缺元数据的工具应被拒绝注册")
		}
		if r.Len() != 0 {
			t.Fatalf("拒绝后注册表应为空，got %d", r.Len())
		}
	})

	t.Run("缺 schema 被拒绝（I3）", func(t *testing.T) {
		r := NewRegistry()
		bad := validTool("no_schema")
		bad.InputSchema = nil
		if err := r.Register(bad); err == nil || !strings.Contains(err.Error(), "inputSchema") {
			t.Fatalf("缺 schema 应被拒且提示 inputSchema，got %v", err)
		}
	})

	t.Run("重名被拒", func(t *testing.T) {
		r := NewRegistry()
		_ = r.Register(validTool("dup"))
		if err := r.Register(validTool("dup")); err == nil {
			t.Fatal("重名工具应被拒绝")
		}
	})
}

// 错误协议：未注册工具名为协议错误，不触达 handler。
func TestInvokeUnknownTool(t *testing.T) {
	r := NewRegistry()
	res := r.Invoke(context.Background(), "nonexistent", nil)
	if res.Err == nil || res.Err.Class != ErrClassUnknownTool || !res.Err.Protocol {
		t.Fatalf("应为 unknown_tool 协议错误，got %+v", res.Err)
	}
}

// I10：超时控制——慢 handler 在 timeout_ms 上界被取消。
func TestInvokeTimeout(t *testing.T) {
	r := NewRegistry()
	slow := validTool("slow")
	slow.Metadata.TimeoutMs = 50
	slow.Handler = func(ctx context.Context, _ map[string]any) *Result {
		select {
		case <-time.After(2 * time.Second):
			return &Result{Data: "should-not-reach"}
		case <-ctx.Done():
			return &Result{Err: NewToolError(ErrClassTimeout, true, "ctx cancelled")}
		}
	}
	if err := r.Register(slow); err != nil {
		t.Fatal(err)
	}
	res := r.Invoke(context.Background(), "slow", nil)
	if res.Err == nil || res.Err.Class != ErrClassTimeout {
		t.Fatalf("应返回 timeout 工具错误，got %+v", res.Err)
	}
}

// I4（M1 形态）：读工具出口数据被 <untrusted_cluster_data> 包裹并标记。
func TestInvokeSanitizeWrap(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(validTool("k8s_get_nodes")); err != nil {
		t.Fatal(err)
	}
	res := r.Invoke(context.Background(), "k8s_get_nodes", nil)
	if res.Err != nil {
		t.Fatalf("不应出错: %v", res.Err)
	}
	if !res.Meta.Wrapped {
		t.Fatal("Meta.Wrapped 应为 true")
	}
	s, _ := res.Data.(string)
	if !strings.HasPrefix(s, untrustedOpen) || !strings.HasSuffix(s, untrustedClose) {
		t.Fatalf("数据应被隔离分隔符包裹，got %q", s)
	}
}

// §5 隔离降级：handler panic 被 recover，转为 internal 工具错误而非进程崩溃。
func TestInvokePanicRecovered(t *testing.T) {
	r := NewRegistry()
	p := validTool("panicker")
	p.Handler = func(_ context.Context, _ map[string]any) *Result { panic("boom") }
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	res := r.Invoke(context.Background(), "panicker", nil)
	if res.Err == nil || res.Err.Class != ErrClassInternal {
		t.Fatalf("panic 应转为 internal 工具错误，got %+v", res.Err)
	}
}
