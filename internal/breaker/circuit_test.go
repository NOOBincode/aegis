package breaker

import (
	"sync"
	"testing"
	"time"
)

func TestCircuitOpensAndHalfOpen(t *testing.T) {
	c := NewCircuit(CircuitConfig{FailureThreshold: 0.5, MinRequests: 4, Window: 10 * time.Second, Cooldown: 5 * time.Second})
	now := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		if !c.Allow(now) {
			t.Fatal("closed 应允许")
		}
		c.Record(false, now)
	}
	if c.State() == CircuitOpen {
		t.Fatal("3 次未达 MinRequests 不应打开")
	}
	if !c.Allow(now) {
		t.Fatal("应允许第 4 次")
	}
	c.Record(false, now)
	if c.State() != CircuitOpen {
		t.Fatalf("失败率应打开：%s", c.State())
	}
	if c.Allow(now.Add(time.Second)) {
		t.Fatal("open 冷却期内应拒绝")
	}
	if !c.Allow(now.Add(6 * time.Second)) {
		t.Fatal("冷却后应允许一次 half-open 探针")
	}
	if c.State() != CircuitHalfOpen || c.Allow(now.Add(6*time.Second)) {
		t.Fatal("half-open 同时只允许一个探针")
	}
	c.Record(true, now.Add(6*time.Second))
	if c.State() != CircuitClosed {
		t.Fatalf("探针成功应闭合：%s", c.State())
	}
}

func TestCircuitHalfOpenFailureReopens(t *testing.T) {
	c := NewCircuit(CircuitConfig{FailureThreshold: 0.5, MinRequests: 1, Window: time.Second, Cooldown: time.Second})
	now := time.Unix(1000, 0)
	c.Record(false, now)
	if c.State() != CircuitOpen {
		t.Fatalf("state=%s", c.State())
	}
	if !c.Allow(now.Add(2 * time.Second)) {
		t.Fatal("应进入 half-open")
	}
	c.Record(false, now.Add(2*time.Second))
	if c.State() != CircuitOpen {
		t.Fatalf("half-open 失败应重开：%s", c.State())
	}
}

func TestCircuitResetAndWindow(t *testing.T) {
	c := NewCircuit(CircuitConfig{FailureThreshold: 0.6, MinRequests: 2, Window: 2 * time.Second, Cooldown: time.Second})
	now := time.Unix(1000, 0)
	c.Record(false, now)
	c.Record(true, now)
	if c.State() == CircuitOpen {
		t.Fatal("50% 不应触发 0.6 阈值")
	}
	c.Record(true, now.Add(3*time.Second)) // 旧事件滑出窗口，只剩 1 个成功
	if c.State() == CircuitOpen {
		t.Fatal("窗口滑出后不应打开")
	}
	c.Reset(now)
	if c.State() != CircuitClosed {
		t.Fatal("reset 应闭合")
	}
}

func TestCircuitConcurrent(t *testing.T) {
	c := NewCircuit(CircuitConfig{FailureThreshold: 0.5, MinRequests: 10, Window: time.Second, Cooldown: time.Second})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			now := time.Unix(1000, 0)
			if c.Allow(now) {
				c.Record(false, now)
			}
		}()
	}
	wg.Wait()
	if c.State() != CircuitOpen {
		t.Fatalf("并发失败应打开：%s", c.State())
	}
}
