package scan

import (
	"sync"
	"sync/atomic"
)

// defaultScanWorkers 并行扇出的默认并发度（对齐 kubeagent defaultScanWorkers=8）。
// 引擎内并行是纯 CPU 判定（不触 apiserver），worker 上限仅为防过度调度，非 QPS 限流。
const defaultScanWorkers = 8

// pmap 有界并行 map：对每个输入并行执行 fn，结果按输入索引回收（保确定性）。
//
// 实现形态：固定 worker 池 + 原子任务计数器——goroutine 数恒等于 workers（默认 8），
// 与输入规模无关。这避免了"每输入一 goroutine + 信号量"在万级输入下创建/调度/销毁
// N 个 goroutine 的开销（goroutine 创建与栈分配在大 N 时是实打实的成本）。
//
// 并发安全与任务隔离（对照 I2）：
//   - fn 必须是纯函数（无共享写）；每个任务只写自己的结果槽位 out[i]，
//     槽位按索引隔离 → 零共享写、无需加锁、无 data race。
//   - 任务分配经 atomic.Int64 取索引（单次原子读，不是共享写），worker 间不争抢内存。
//   - workers ≤0 用默认、超过输入长度钳到 len(in)，避免空转与过度并发。
//   - 结果按输入索引顺序拼接，与并行度/调度顺序无关 → 输出确定性（审计/回归可比对）。
func pmap[In, Out any](workers int, in []In, fn func(In) []Out) [][]Out {
	n := len(in)
	if n == 0 {
		return nil
	}
	if workers <= 0 {
		workers = defaultScanWorkers
	}
	workers = min(workers, n)
	out := make([][]Out, n)

	var next atomic.Int64 // 下一个待处理索引（原子取号，worker 间无锁竞争）
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				out[i] = fn(in[i]) // 仅写自己的槽位
			}
		}()
	}
	wg.Wait()
	return out
}
