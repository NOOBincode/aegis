package breaker

import (
	"sync"
	"time"
)

// QPSWindow 是纯 stdlib 的滑动窗口限流器（M1 T1.4 的 apiserver/QPS 预算）。
// 只回答 "now 这一刻是否允许一次调用"；时间由调用方注入，便于单测无 sleep。
// 跨进程/多副本的全局 QPS 属 M2（Redis 端口），这里不假装是分布式限流。
type QPSWindow struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   []time.Time
}

// NewQPSWindow 构造滑动窗口；limit<=0 表示不限流，但保留对象便于注入。
func NewQPSWindow(limit int, window time.Duration) *QPSWindow {
	if window <= 0 {
		window = time.Second
	}
	return &QPSWindow{limit: limit, window: window}
}

// Allow 返回 now 时刻是否允许一次调用；允许时记录一次 hit。
func (w *QPSWindow) Allow(now time.Time) bool {
	if w == nil || w.limit <= 0 {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	cut := now.Add(-w.window)
	// 去掉窗口外的 hit；hits 按时间递增追加，故只需从头部裁。
	i := 0
	for i < len(w.hits) && !w.hits[i].After(cut) {
		i++
	}
	if i > 0 {
		w.hits = append(w.hits[:0], w.hits[i:]...)
	}
	if len(w.hits) >= w.limit {
		return false
	}
	w.hits = append(w.hits, now)
	return true
}

// Used 返回当前窗口内命中数（观测用，不加锁地返回近似值不在此实现）。
func (w *QPSWindow) Used(now time.Time) int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	cut := now.Add(-w.window)
	n := 0
	for _, t := range w.hits {
		if t.After(cut) {
			n++
		}
	}
	return n
}
