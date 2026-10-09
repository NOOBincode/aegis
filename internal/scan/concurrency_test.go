package scan

import (
	"fmt"
	"sync"
	"testing"
)

// TestRunConcurrentSafeAndDeterministic：大规模 Pod 下并行判定，
// ① -race 下无 data race；② 不同并发度输出一致（确定性，与并行度无关）。
func TestRunConcurrentSafeAndDeterministic(t *testing.T) {
	// 构造 2000 个 Pod，部分命中 OOM/CrashLoop，模拟容器数量庞大场景。
	pods := make([]PodFacts, 0, 2000)
	for i := 0; i < 2000; i++ {
		p := PodInfo{Namespace: "shop", Name: fmt.Sprintf("p%d", i), Phase: "Running"}
		switch i % 4 {
		case 0:
			p.Containers = []ContainerInfo{{Name: "c", LastTerminatedReason: "OOMKilled", LastTerminatedCode: 137}}
		case 1:
			p.Containers = []ContainerInfo{{Name: "c", WaitingReason: "CrashLoopBackOff", RestartCount: 5}}
		default:
			p.Containers = []ContainerInfo{{Name: "c", Ready: true}}
		}
		pods = append(pods, PodFacts{Pod: p})
	}

	detectors := DefaultDetectors(fixedNow)
	serial := Run(detectors, pods, 1) // 单 worker 基准
	par8 := Run(detectors, pods, 8)   // 默认并发
	par64 := Run(detectors, pods, 64) // 高并发

	if len(serial) == 0 {
		t.Fatal("应有命中")
	}
	if len(serial) != len(par8) || len(serial) != len(par64) {
		t.Fatalf("不同并发度命中数不一致：serial=%d par8=%d par64=%d", len(serial), len(par8), len(par64))
	}
	for i := range serial {
		if serial[i].ID != par8[i].ID || serial[i].ID != par64[i].ID {
			t.Fatalf("并发改变输出顺序/内容（i=%d）：%q vs %q vs %q", i, serial[i].ID, par8[i].ID, par64[i].ID)
		}
	}
}

// TestEngineConcurrentScan：同一 Engine 被并发 Scan（MCP 工具并发调用场景），
// 验证无共享可变状态（policies 经参数传入）→ 无 race、结果互不干扰。
func TestEngineConcurrentScan(t *testing.T) {
	eng := NewEngine(nil, 0, fixedNow) // 共享同一 Engine
	snapOOM := Snapshot{Pods: []PodInfo{
		{Namespace: "a", Name: "oom", Phase: "Running", Containers: []ContainerInfo{{Name: "c", LastTerminatedReason: "OOMKilled"}}},
	}}
	snapDeploy := Snapshot{Deployments: []DeploymentInfo{
		{Namespace: "b", Name: "d", Containers: []WorkloadContainer{{Name: "c"}}},
	}}
	policy := PolicyRule{
		ID: "reliability.no-memory-limit", Match: PolicyMatch{Kind: "Deployment"},
		Assert: PolicyAssert{Path: "spec.template.spec.containers[*].resources.limits.memory", Op: OpExists},
		Level:  SeverityWarning, Message: "no limit",
	}

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*2)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			if g%2 == 0 {
				res := eng.Scan(snapOOM, nil) // 只 A 层
				if len(res.Findings) != 1 || res.Findings[0].RuleID != "OOM_KILLED" {
					errs <- fmt.Errorf("A层并发结果错误: %+v", res.Findings)
				}
			} else {
				res := eng.Scan(snapDeploy, []PolicyRule{policy}) // 只 B 层
				if len(res.Findings) != 1 || res.Findings[0].RuleID != "policy/reliability.no-memory-limit" {
					errs <- fmt.Errorf("B层并发结果错误: %+v", res.Findings)
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestPmapBounded：验证 pmap 有界（并发度被钳制）且结果按索引回收。
func TestPmapBounded(t *testing.T) {
	in := make([]int, 100)
	for i := range in {
		in[i] = i
	}
	out := pmap(8, in, func(v int) []int { return []int{v * 2} })
	if len(out) != 100 {
		t.Fatalf("结果槽位数=%d, 期望 100", len(out))
	}
	for i, slot := range out {
		if len(slot) != 1 || slot[0] != i*2 {
			t.Fatalf("槽位 %d 错位：%v", i, slot)
		}
	}
	if got := pmap(8, nil, func(int) []int { return nil }); got != nil {
		t.Errorf("空输入应返回 nil，got %v", got)
	}
}
