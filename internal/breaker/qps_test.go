package breaker

import (
	"sync"
	"testing"
	"time"
)

func TestQPSWindowSliding(t *testing.T) {
	w := NewQPSWindow(2, time.Second)
	t0 := time.Unix(100, 0)
	if !w.Allow(t0) || !w.Allow(t0.Add(100*time.Millisecond)) {
		t.Fatal("前两次应允许")
	}
	if w.Allow(t0.Add(200 * time.Millisecond)) {
		t.Fatal("窗口内第三次应拒绝")
	}
	if !w.Allow(t0.Add(1100 * time.Millisecond)) {
		t.Fatal("滑出窗口后应允许")
	}
}

func TestQPSWindowConcurrent(t *testing.T) {
	w := NewQPSWindow(10, time.Second)
	now := time.Unix(200, 0)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w.Allow(now) {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 10 {
		t.Fatalf("并发下应恰好允许 10 次，got %d", allowed)
	}
}
