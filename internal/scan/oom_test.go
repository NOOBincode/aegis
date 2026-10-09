package scan

import (
	"strings"
	"testing"
)

// TestOOMGradedInference 验证"无明确报错时的 OOM 推断链"：
// High=kubelet 标注 OOMKilled；Medium=仅退出码 137，按节点/容器佐证区分成因。
func TestOOMGradedInference(t *testing.T) {
	d := oomKilledDetector{fixedNow}

	t.Run("高置信-reason标注", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{
			{Name: "c", LastTerminatedReason: "OOMKilled", LastTerminatedCode: 137, MemoryLimitBytes: 1 << 28},
		}}
		f := d.Detect(PodFacts{Pod: pod})
		if f == nil || f.Confidence != ConfidenceHigh {
			t.Fatalf("应为高置信 OOM：%+v", f)
		}
	})

	t.Run("中置信-退出码137-节点压力佐证", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running", NodeName: "n1", Containers: []ContainerInfo{
			{Name: "c", LastTerminatedCode: 137}, // 无 Reason，只有裸 137
		}}
		f := d.Detect(PodFacts{Pod: pod, NodeMemoryPressure: true})
		if f == nil || f.Confidence != ConfidenceMedium {
			t.Fatalf("应为中置信推断 OOM：%+v", f)
		}
		joined := strings.Join(f.Evidence, " | ")
		if !strings.Contains(joined, "SIGKILL") && !strings.Contains(joined, "137") {
			t.Errorf("证据应含退出码 137/SIGKILL 解读：%v", f.Evidence)
		}
		if !strings.Contains(joined, "节点") {
			t.Errorf("应有节点级佐证：%v", f.Evidence)
		}
	})

	t.Run("中置信-退出码137-容器级limit佐证", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{
			{Name: "c", LastTerminatedCode: 137, MemoryLimitBytes: 1 << 28},
		}}
		f := d.Detect(PodFacts{Pod: pod}) // 无节点压力
		if f == nil || f.Confidence != ConfidenceMedium {
			t.Fatalf("应为中置信推断 OOM：%+v", f)
		}
		joined := strings.Join(f.Evidence, " | ")
		if !strings.Contains(joined, "memory limit") {
			t.Errorf("应有容器级 limit 佐证：%v", f.Evidence)
		}
	})

	t.Run("退出码143不误判为OOM", func(t *testing.T) {
		// 143（SIGTERM，滚动更新/驱逐）不应判为 OOM。
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{
			{Name: "c", LastTerminatedCode: 143},
		}}
		if f := d.Detect(PodFacts{Pod: pod}); f != nil {
			t.Errorf("143（SIGTERM）不应判 OOM：%+v", f)
		}
	})
}

// TestEvicted 验证 Pod 驱逐判定（节点资源压力触发，常与系统 OOM 同根）。
func TestEvicted(t *testing.T) {
	d := evictedDetector{}
	hit := PodFacts{Pod: PodInfo{
		Namespace: "s", Name: "p", Phase: "Failed", Reason: "Evicted",
		Message: "The node was low on resource: memory",
	}, NodeMemoryPressure: true}
	f := d.Detect(hit)
	if f == nil || f.RuleID != "EVICTED" || f.Confidence != ConfidenceHigh {
		t.Fatalf("Evicted+节点压力应为高置信命中：%+v", f)
	}

	// 非 Evicted 的 Failed 不误判。
	miss := PodFacts{Pod: PodInfo{Namespace: "s", Name: "p2", Phase: "Failed", Reason: "Completed"}}
	if f := d.Detect(miss); f != nil {
		t.Errorf("非 Evicted 不应命中：%+v", f)
	}
}

// TestExitCodeMeaning 验证裸退出码的人话解读（覆盖常见值与信号族）。
func TestExitCodeMeaning(t *testing.T) {
	cases := map[int32]string{
		0: "正常退出", 137: "SIGKILL", 143: "SIGTERM", 139: "SIGSEGV",
		127: "命令未找到", 1: "通用应用错误", 134: "SIGABRT",
	}
	for code, wantSub := range cases {
		got := exitCodeMeaning(code)
		if !strings.Contains(got, wantSub) {
			t.Errorf("exitCodeMeaning(%d)=%q, 期望含 %q", code, got, wantSub)
		}
	}
}
