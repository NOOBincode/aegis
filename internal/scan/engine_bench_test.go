package scan

import (
	"fmt"
	"testing"
	"time"
)

// 规则层性能基线（大集群规模）。fixtures 为测量而造（大规模），与正确性测试分离。

// benchSnapshot 构造大规模快照：nPods（10% OOM / 10% CrashLoop / 5% Pending / 余正常）、
// nDeploys（部分缺 memory limit 触发 B 层）、nEvents（部分 Unhealthy/SystemOOM）。
func benchSnapshot(nPods, nDeploys, nEvents int) Snapshot {
	pods := make([]PodInfo, nPods)
	for i := range pods {
		p := PodInfo{
			Namespace: "shop", Name: fmt.Sprintf("pod-%d", i),
			Phase: "Running", NodeName: fmt.Sprintf("node-%d", i%50),
			OwnerKind: "Deployment", OwnerName: "app",
			Containers:     []ContainerInfo{{Name: "main", Ready: true}},
			InitContainers: []ContainerInfo{{Name: "init", Ready: true}},
		}
		switch i % 20 {
		case 0: // OOMKilled（reason 标注，高置信）
			p.Containers = []ContainerInfo{{
				Name: "main", RestartCount: 5, Ready: false,
				LastTerminatedReason: "OOMKilled", LastTerminatedCode: 137,
				LastTerminatedAt: fixedNow().Add(-time.Minute), MemoryLimitBytes: 1 << 28,
			}}
		case 1: // CrashLoop（非 OOM）
			p.Containers = []ContainerInfo{{
				Name: "main", WaitingReason: "CrashLoopBackOff", RestartCount: 7, LastTerminatedCode: 1,
			}}
		case 2: // Pending 不可调度
			p.Phase = "Pending"
			p.ScheduledConditionStatus = "False"
			p.ScheduledConditionReason = "Unschedulable"
			p.ScheduledConditionMessage = "0/50 nodes are available: insufficient memory"
		case 3: // 137-only（中置信 OOM 推断 + 节点佐证）
			p.Containers = []ContainerInfo{{
				Name: "main", RestartCount: 2, LastTerminatedCode: 137,
				LastTerminatedAt: fixedNow().Add(-time.Minute), MemoryLimitBytes: 1 << 28,
			}}
		}
		pods[i] = p
	}

	deploys := make([]DeploymentInfo, nDeploys)
	for i := range deploys {
		d := DeploymentInfo{
			Namespace: "shop", Name: fmt.Sprintf("deploy-%d", i),
			Replicas: 3, ReadyReplicas: 3,
		}
		if i%3 == 0 { // 缺 limit + latest tag → 触发 B 层多条规则
			d.Containers = []WorkloadContainer{{Name: "c", Image: "registry.local/app:latest"}}
		} else {
			d.Containers = []WorkloadContainer{{
				Name: "c", Image: "registry.local/app:v1.2.3",
				HasReadinessProbe: true, HasLivenessProbe: true, MemoryLimitBytes: 1 << 28,
			}}
		}
		deploys[i] = d
	}

	events := make([]EventInfo, nEvents)
	for i := range events {
		ev := EventInfo{
			Namespace: "shop", Reason: "Unhealthy", Type: "Warning", Count: 1,
			Message:      "Readiness probe failed: context deadline exceeded",
			InvolvedKind: "Pod", InvolvedName: fmt.Sprintf("pod-%d", i%nPods),
			LastSeen: fixedNow().Add(-time.Minute),
		}
		if i%100 == 0 { // 少量节点级 SystemOOM
			ev.InvolvedKind = "Node"
			ev.InvolvedName = fmt.Sprintf("node-%d", i%50)
			ev.Reason = "SystemOOM"
			ev.Message = "System OOM encountered"
		}
		events[i] = ev
	}

	nodes := make([]NodeInfo, 50)
	for i := range nodes {
		nodes[i] = NodeInfo{Name: fmt.Sprintf("node-%d", i), Ready: true, MemoryPressure: i%10 == 0}
	}
	return Snapshot{Pods: pods, Deployments: deploys, Events: events, Nodes: nodes}
}

// benchPolicies B 层规则（默认包的代表子集）。
func benchPolicies() []PolicyRule {
	mk := func(id, path, op string, vals ...string) PolicyRule {
		return PolicyRule{ID: id, Match: PolicyMatch{Kind: "Deployment"},
			Assert: PolicyAssert{Path: path, Op: op, Values: vals}, Level: SeverityWarning, Message: id}
	}
	return []PolicyRule{
		mk("reliability.no-memory-limit", "spec.template.spec.containers[*].resources.limits.memory", OpExists),
		mk("reliability.no-readiness-probe", "spec.template.spec.containers[*].readinessProbe", OpExists),
		mk("reliability.no-liveness-probe", "spec.template.spec.containers[*].livenessProbe", OpExists),
		mk("reliability.image-latest-tag", "spec.template.spec.containers[*].imageTag", OpNotIn, "latest"),
		mk("security.privileged-container", "spec.template.spec.containers[*].securityContext.privileged", OpNotIn, "true"),
	}
}

// BenchmarkEngineScan 全引擎扫描（A+B 层 + 排序 + 消毒），按并发度分档。
func BenchmarkEngineScan(b *testing.B) {
	snap := benchSnapshot(5000, 500, 2000)
	pols := benchPolicies()
	for _, workers := range []int{1, 8, 16} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			eng := NewEngine(nil, workers, fixedNow)
			b.ReportAllocs()
			for b.Loop() {
				eng.Scan(snap, pols)
			}
		})
	}
}

// BenchmarkRunDetectors A 层扇出（detector × pod）。
func BenchmarkRunDetectors(b *testing.B) {
	snap := benchSnapshot(5000, 0, 2000)
	byPod := indexEventsByPod(snap.Events)
	nodeP := indexNodeMemoryPressure(snap.Nodes)
	nodeOOM := indexNodeSystemOOM(snap.Events)
	facts := make([]PodFacts, len(snap.Pods))
	for i, p := range snap.Pods {
		facts[i] = PodFacts{Pod: p, Events: byPod[podKey(p.Namespace, p.Name)],
			NodeMemoryPressure: nodeP[p.NodeName], NodeSystemOOM: nodeOOM[p.NodeName],
			All: allContainers(p)}
	}
	for _, workers := range []int{1, 8} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			detectors := DefaultDetectors(fixedNow)
			b.ReportAllocs()
			for b.Loop() {
				Run(detectors, facts, workers)
			}
		})
	}
}

// BenchmarkEvaluatePolicies B 层规则求值（deployment × rule）。
func BenchmarkEvaluatePolicies(b *testing.B) {
	snap := benchSnapshot(0, 2000, 0)
	pols := benchPolicies()
	b.ReportAllocs()
	for b.Loop() {
		EvaluatePolicies(pols, snap, 8)
	}
}
