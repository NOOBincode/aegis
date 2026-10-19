package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeQPS struct {
	mu    sync.Mutex
	allow int
}

func (f *fakeQPS) Allow(time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allow++
	return f.allow <= 2
}

func TestToolCallBudget(t *testing.T) {
	m := NewManager(Limits{MaxToolCalls: 2})
	ctx := context.Background()
	if err := m.Before(ctx, "s1", "a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Before(ctx, "s1", "b"); err != nil {
		t.Fatal(err)
	}
	if err := m.Before(ctx, "s1", "c"); err == nil {
		t.Fatal("第三次应超限")
	}
	if err := m.Before(ctx, "s2", "a"); err != nil {
		t.Fatalf("不同会话不应互相影响：%v", err)
	}
	u := m.Usage("s1")
	if u.ToolCalls != 2 {
		t.Fatalf("usage=%+v", u)
	}
}

func TestTokenBudget(t *testing.T) {
	m := NewManager(Limits{MaxTokens: 10})
	ctx := context.Background()
	if err := m.After(ctx, "s", "a", 6); err != nil {
		t.Fatal(err)
	}
	if err := m.After(ctx, "s", "a", 4); err != nil {
		t.Fatalf("刚好到限不应报错：%v", err)
	}
	if err := m.After(ctx, "s", "a", 1); err == nil {
		t.Fatal("超过 token 限应报错")
	}
	if err := m.Before(ctx, "s", "a"); err == nil {
		t.Fatal("token 已超限，下一次 Before 应直接 429")
	}
}

func TestQPSBefore(t *testing.T) {
	m := NewManager(Limits{}.withDefaultWindow()).WithQPS(&fakeQPS{})
	ctx := context.Background()
	if err := m.Before(ctx, "s", "a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Before(ctx, "s", "a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Before(ctx, "s", "a"); err == nil {
		t.Fatal("QPS 超限应报错")
	}
}

func TestConcurrentNoBypass(t *testing.T) {
	m := NewManager(Limits{MaxToolCalls: 10})
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.Before(ctx, "same", "tool") == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 10 {
		t.Fatalf("并发下应恰好 10 次通过，got %d", ok)
	}
}

func TestAnonymousShared(t *testing.T) {
	m := NewManager(Limits{MaxToolCalls: 1})
	ctx := context.Background()
	if err := m.Before(ctx, "", "a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Before(ctx, "", "b"); err == nil {
		t.Fatal("匿名会话必须共享预算，不能绕过")
	}
}

// withDefaultWindow 仅补 QPSWindow 的零值（Manager 会归一，但测试显式）。
func (l Limits) withDefaultWindow() Limits {
	if l.QPSWindow == 0 {
		l.QPSWindow = time.Second
	}
	return l
}
