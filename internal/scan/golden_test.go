package scan

import (
	"testing"
	"time"
)

// golden 场景：eval 场景库第 1 号（落地方案 T1.10，M1 验收口径）——Pod OOMKilled。
// 该测试固化"规则层不经 LLM 即结案"的预期（减少 AI 审核压力的直接证据），
// 规则改动导致快照漂移即失败（回归门禁，I8/R30）。

// oomScenarioSnapshot 构造 OOMKilled 故障场景（对应 kind 集群注入 OOM 的期望根因）。
func oomScenarioSnapshot() Snapshot {
	return Snapshot{
		Pods: []PodInfo{{
			Namespace: "shop", Name: "payments-7d9f", Phase: "Running", NodeName: "node-1",
			OwnerKind: "Deployment", OwnerName: "payments",
			Containers: []ContainerInfo{{
				Name: "payments", RestartCount: 6, Ready: false,
				LastTerminatedReason: "OOMKilled", LastTerminatedCode: 137,
				LastTerminatedAt: fixedNow().Add(-3 * time.Minute),
				MemoryLimitBytes: 512 * 1024 * 1024,
			}},
		}},
		Nodes: []NodeInfo{{Name: "node-1", Ready: true}},
	}
}

// TestGoldenOOMScenario 固化 M1 验收口径：OOMKilled 场景规则层命中根因且结案（NextTier=false）。
func TestGoldenOOMScenario(t *testing.T) {
	eng := NewEngine(nil, 0, fixedNow)
	res := eng.Scan(oomScenarioSnapshot(), nil)

	if len(res.Findings) == 0 {
		t.Fatal("OOM 场景应命中至少一条 finding")
	}
	var oom *Finding
	for i := range res.Findings {
		if res.Findings[i].RuleID == "OOM_KILLED" {
			oom = &res.Findings[i]
		}
	}
	if oom == nil {
		t.Fatalf("缺 OOM_KILLED 命中：%+v", res.Findings)
	}
	// 验收口径核心：根因命中标注 + 高置信 + 规则结案（不惊动 LLM/人审）。
	if oom.Object.Name != "payments-7d9f" {
		t.Errorf("命中对象错误：%q", oom.Object.Name)
	}
	if oom.Confidence != ConfidenceHigh {
		t.Errorf("OOMKilled 应为高置信，got %q", oom.Confidence)
	}
	if oom.NextTier {
		t.Errorf("OOMKilled 应规则结案（NextTier=false），不惊动上层")
	}
	if res.ResolvedByRules != len(res.Findings) {
		t.Errorf("本场景全部应由规则结案：resolved=%d total=%d", res.ResolvedByRules, len(res.Findings))
	}
}

// TestGoldenDeterministicOutput 固化确定性输出：同一场景两次扫描结果完全一致（可回放/可比对）。
func TestGoldenDeterministicOutput(t *testing.T) {
	eng := NewEngine(nil, 0, fixedNow)
	r1 := eng.Scan(oomScenarioSnapshot(), nil)
	r2 := eng.Scan(oomScenarioSnapshot(), nil)
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("两次扫描命中数不一致：%d vs %d", len(r1.Findings), len(r2.Findings))
	}
	for i := range r1.Findings {
		if r1.Findings[i].ID != r2.Findings[i].ID {
			t.Fatalf("输出不确定（i=%d）：%q vs %q", i, r1.Findings[i].ID, r2.Findings[i].ID)
		}
	}
}
