package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/aegis-dev/aegis/internal/scan"
)

// 端到端：fake clientset → Collect 采集映射 → Engine 判定，验证字段映射正确性
// 与"无明确报错时的 OOM 推断链"（OOMKilled 在 LastTerminationState、Unschedulable
// 在 PodScheduled condition、节点压力佐证 137-only 推断）。
func TestCollectAndScan(t *testing.T) {
	finishedAt := metav1.NewTime(time.Date(2026, 10, 8, 11, 58, 0, 0, time.UTC))
	memLimit := resource.MustParse("512Mi")

	oomPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "payments-0"},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{{Name: "web", Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: memLimit},
			}}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "web", RestartCount: 5,
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "OOMKilled", ExitCode: 137, FinishedAt: finishedAt,
				}},
			}},
		},
	}
	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "batch-0"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
				Reason: "Unschedulable", Message: "0/3 nodes are available: insufficient memory",
			}},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
		}},
	}
	noLimitDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "api", Image: "registry.local/api:latest"}},
			}},
		},
	}
	// 137-only Pod（无 Reason，节点有 MemoryPressure → 节点级系统 OOM 推断）。
	bareOOMPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "worker-0"},
		Spec:       corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: "w"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "w",
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 137, FinishedAt: finishedAt, // 无 Reason
				}},
			}},
		},
	}

	cs := fake.NewSimpleClientset(oomPod, pendingPod, node, noLimitDeploy, bareOOMPod)
	prov := NewScanSnapshotProvider(cs)
	snap, err := prov.Collect(context.Background(), scan.CollectOptions{Namespace: "shop"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snap.Pods) != 3 || len(snap.Nodes) != 1 || len(snap.Deployments) != 1 {
		t.Fatalf("采集数量错误：pods=%d nodes=%d deploys=%d", len(snap.Pods), len(snap.Nodes), len(snap.Deployments))
	}

	// 字段映射抽查：OOM 的 LastTerminationState 必须被正确映射。
	var mapped *scan.PodInfo
	for i := range snap.Pods {
		if snap.Pods[i].Name == "payments-0" {
			mapped = &snap.Pods[i]
		}
	}
	if mapped == nil || len(mapped.Containers) != 1 {
		t.Fatalf("OOM pod 映射缺失：%+v", snap.Pods)
	}
	c := mapped.Containers[0]
	if c.LastTerminatedReason != "OOMKilled" || c.LastTerminatedCode != 137 {
		t.Errorf("OOM 信号映射错误：%+v", c)
	}
	if c.MemoryLimitBytes != memLimit.Value() {
		t.Errorf("memory limit 映射错误：got %d want %d", c.MemoryLimitBytes, memLimit.Value())
	}

	// 引擎判定：应命中 OOM(高置信,reason标注) + PENDING + 137-only(中置信) + B 层规则。
	eng := scan.NewEngine(nil, 0, nil)
	res := eng.Scan(snap, nil) // 本用例不挂 B 层规则，专注 A 层映射
	byRule := map[string]scan.Finding{}
	for _, f := range res.Findings {
		byRule[f.RuleID+"/"+f.Object.Name] = f
	}
	if f, ok := byRule["OOM_KILLED/payments-0"]; !ok || f.Confidence != scan.ConfidenceHigh {
		t.Errorf("缺高置信 OOM 命中：%+v", res.Findings)
	}
	if _, ok := byRule["PENDING_UNSCHEDULABLE/batch-0"]; !ok {
		t.Errorf("缺 Pending 命中：%+v", res.Findings)
	}
	if f, ok := byRule["OOM_KILLED/worker-0"]; !ok || f.Confidence != scan.ConfidenceMedium {
		t.Errorf("缺 137-only 中置信 OOM 推断命中：%+v", res.Findings)
	}
}

// TestCollectForbiddenBlindSpot：单类资源被拒读记入盲点而非整体失败/静默当空。
func TestCollectForbiddenBlindSpot(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "p"}})
	// 模拟 events 被 RBAC 拒（403 Forbidden）。
	cs.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "", Resource: "events"}, "", errors.New("denied by RBAC"))
	})

	prov := NewScanSnapshotProvider(cs)
	snap, err := prov.Collect(context.Background(), scan.CollectOptions{})
	if err != nil {
		t.Fatalf("可降级错误不应整体失败：%v", err)
	}
	// pods 正常采集，events 记盲点（不静默当空）。
	if len(snap.Pods) != 1 {
		t.Errorf("pods 应正常采集：%+v", snap.Pods)
	}
	found := false
	for _, b := range snap.PartialReads {
		if strings.HasPrefix(b, "events:") {
			found = true
		}
	}
	if !found {
		t.Errorf("events 拒读应记入盲点：%+v", snap.PartialReads)
	}
}
