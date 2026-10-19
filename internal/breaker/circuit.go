package breaker

import (
	"sync"
	"time"
)

// CircuitConfig 是熔断参数；FailureThreshold<=0 表示禁用（始终允许）。
type CircuitConfig struct {
	FailureThreshold float64 // 0..1，窗口内失败率阈值
	MinRequests      int     // 窗口内最少请求数才允许触发
	Window           time.Duration
	Cooldown         time.Duration // Open 后进入 HalfOpen 的等待时间
}

// CircuitState 是熔断器状态。
type CircuitState string

const (
	CircuitClosed   CircuitState = "Closed"
	CircuitOpen     CircuitState = "Open"
	CircuitHalfOpen CircuitState = "HalfOpen"
)

type circuitResult struct {
	at      time.Time
	success bool
}

// Circuit 是并发安全的失败率熔断器（M2 T2.6）。
// 它只回答是否允许继续写/调用；全局降级只读的“动作”由闸门执行（本包不 import L2）。
type Circuit struct {
	mu     sync.Mutex
	cfg    CircuitConfig
	state  CircuitState
	events []circuitResult
	opened time.Time
	probe  bool // HalfOpen 已有一次探针在途
}

// NewCircuit 构造熔断器；窗口/冷却非法时使用保守默认（30s/30s）。
func NewCircuit(cfg CircuitConfig) *Circuit {
	if cfg.Window <= 0 {
		cfg.Window = 30 * time.Second
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	if cfg.MinRequests <= 0 {
		cfg.MinRequests = 1
	}
	return &Circuit{cfg: cfg, state: CircuitClosed}
}

// State 返回当前状态（不触发迁移）。
func (c *Circuit) State() CircuitState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Allow 判断 now 时是否允许一次调用；HalfOpen 只允许一个探针。
func (c *Circuit) Allow(now time.Time) bool {
	if c == nil || c.cfg.FailureThreshold <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case CircuitOpen:
		if now.Sub(c.opened) >= c.cfg.Cooldown {
			c.state = CircuitHalfOpen
			c.probe = true
			return true
		}
		return false
	case CircuitHalfOpen:
		if c.probe {
			return false
		}
		c.probe = true
		return true
	default:
		return true
	}
}

// Record 记录一次调用结果并迁移状态。
func (c *Circuit) Record(success bool, now time.Time) {
	if c == nil || c.cfg.FailureThreshold <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case CircuitHalfOpen:
		c.probe = false
		if success {
			c.state = CircuitClosed
			c.events = nil
		} else {
			c.state = CircuitOpen
			c.opened = now
		}
		return
	case CircuitOpen:
		// Open 期间的调用通常被 Allow 拒绝；若仍被记录，不改变状态。
		return
	}
	c.events = append(c.events, circuitResult{at: now, success: success})
	c.prune(now)
	if len(c.events) < c.cfg.MinRequests {
		return
	}
	failures := 0
	for _, e := range c.events {
		if !e.success {
			failures++
		}
	}
	if float64(failures)/float64(len(c.events)) >= c.cfg.FailureThreshold {
		c.state = CircuitOpen
		c.opened = now
	}
}

// Reset 人工复位（F7：熔断后需显式复位，不自动静默恢复）。
func (c *Circuit) Reset(now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = CircuitClosed
	c.events = nil
	c.probe = false
	c.opened = time.Time{}
	_ = now
}

func (c *Circuit) prune(now time.Time) {
	cut := now.Add(-c.cfg.Window)
	i := 0
	for i < len(c.events) && !c.events[i].at.After(cut) {
		i++
	}
	if i > 0 {
		c.events = append(c.events[:0], c.events[i:]...)
	}
}
