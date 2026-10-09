package scan

import "sync"

// defaultScanWorkers 并行扇出的默认并发度（对齐 kubeagent defaultScanWorkers=8）。
// 引擎内并行是纯 CPU 判定（不触 apiserver），worker 上限仅为防过度调度，非 QPS 限流。
const defaultScanWorkers = 8

// pmap 有界并行 map：对每个输入并行执行 fn，结果按输入索引回收（保确定性）。
//
// 并发安全与任务隔离（设计文档 §4.2.4 精神，对照 I2）：
//   - fn 必须是纯函数（无共享写）；每个 goroutine 只写自己的结果槽位 out[i]，
//     槽位按索引隔离 → 零共享写、无需加锁、无 data race。
//   - workers ≤0 或超过输入长度时钳制到 [1, len(in)]，避免空转与过度并发。
//   - 结果按输入索引顺序拼接，与并行度无关 → 输出确定性（审计/回放/回归可比对）。
func pmap[In, Out any](workers int, in []In, fn func(In) []Out) [][]Out {
	n := len(in)
	if n == 0 {
		return nil
	}
	if workers <= 0 || workers > n {
		workers = min(defaultScanWorkers, n)
	}
	out := make([][]Out, n)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range in {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}        // 占用槽位（有界并发）
			defer func() { <-sem }() // 释放槽位
			out[i] = fn(in[i])       // 仅写自己的槽位
		}(i)
	}
	wg.Wait()
	return out
}
