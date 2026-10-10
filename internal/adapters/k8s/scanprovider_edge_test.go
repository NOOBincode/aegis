package k8s

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aegis-dev/aegis/internal/scan"
)

var fixedClock = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// TestMapEdgeCases 字段缺失/边界映射：不应 panic，缺省值正确。
func TestMapEdgeCases(t *testing.T) {
	cs := fake.NewSimpleClientset(
		// 无 containerStatuses、无 ownerReferences 的 Pod。
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "s", Name: "bare"}},
		// 无 containers、无 SecurityContext 的 Deployment。
		newDeploy("s", "empty"),
		// 无 conditions 的 Node。
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "bare-node"}},
	)
	prov := NewScanSnapshotProvider(cs)
	snap, err := prov.Collect(context.Background(), scan.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snap.Pods) != 1 || len(snap.Nodes) != 1 || len(snap.Deployments) != 1 {
		t.Fatalf("采集数量错误：%+v", snap)
	}
	// 裸 Pod：无容器、无属主，字段零值不 panic。
	if snap.Pods[0].OwnerKind != "" || len(snap.Pods[0].Containers) != 0 {
		t.Errorf("裸 Pod 映射应零值：%+v", snap.Pods[0])
	}
	// 裸 Node：conditions 全 false。
	if snap.Nodes[0].Ready || snap.Nodes[0].MemoryPressure {
		t.Errorf("裸 Node 应全 false：%+v", snap.Nodes[0])
	}
}

// TestEventTimeWindowFilter 事件时间窗边界：超窗丢弃、窗内保留、无时间戳保留（不过滤）。
func TestEventTimeWindowFilter(t *testing.T) {
	mk := func(name string, lastSeen time.Time) *corev1.Event {
		e := &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{Namespace: "s", Name: name},
			Reason:     "Unhealthy", Type: "Warning",
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p"},
		}
		if !lastSeen.IsZero() {
			e.LastTimestamp = metav1.NewTime(lastSeen)
		}
		return e
	}
	cs := fake.NewSimpleClientset(
		mk("old", fixedClock.Add(-2*time.Hour)),       // 超 1h 窗 → 丢弃
		mk("recent", fixedClock.Add(-30*time.Minute)), // 窗内 → 保留
		mk("notime", time.Time{}),                     // 无时间戳 → 保留（无法判断不丢）
	)
	prov := NewScanSnapshotProvider(cs)
	prov.now = func() time.Time { return fixedClock }

	snap, err := prov.Collect(context.Background(), scan.CollectOptions{SinceSeconds: 3600})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var names []string
	for _, e := range snap.Events {
		names = append(names, e.Reason+"@"+e.InvolvedName)
	}
	// old 被丢弃；recent、notime 保留。预期 2 条。
	if len(snap.Events) != 2 {
		t.Errorf("时间窗过滤后应 2 条（recent+notime），got %d: %v", len(snap.Events), names)
	}

	// SinceSeconds=0 不过滤：全保留（3 条）。
	snap2, _ := prov.Collect(context.Background(), scan.CollectOptions{SinceSeconds: 0})
	if len(snap2.Events) != 3 {
		t.Errorf("SinceSeconds=0 应全保留 3 条，got %d", len(snap2.Events))
	}
}

// TestContainerLimitsMissing 容器无 resources.limits 时 limit 为 0（不 panic）。
func TestContainerLimitsMissing(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "s", Name: "nolimit"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}}, // 无 resources
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "c", LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}},
		}}},
	}
	cs := fake.NewSimpleClientset(pod)
	prov := NewScanSnapshotProvider(cs)
	snap, err := prov.Collect(context.Background(), scan.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snap.Pods) != 1 || len(snap.Pods[0].Containers) != 1 {
		t.Fatalf("映射缺失：%+v", snap.Pods)
	}
	c := snap.Pods[0].Containers[0]
	if c.MemoryLimitBytes != 0 {
		t.Errorf("无 limit 应为 0，got %d", c.MemoryLimitBytes)
	}
	// 137-only 且无 limit、无节点压力 → 中置信 OOM 推断走 default 建议。
	eng := scan.NewEngine(nil, 0, func() time.Time { return fixedClock })
	res := eng.Scan(snap, nil)
	var found bool
	for _, f := range res.Findings {
		if f.RuleID == "OOM_KILLED" && f.Confidence == scan.ConfidenceMedium {
			found = true
		}
	}
	if !found {
		t.Errorf("137-only 无 limit 应中置信命中 OOM：%+v", res.Findings)
	}
}

// newDeploy 构造无容器的 Deployment（边界）。
func newDeploy(ns, name string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}
