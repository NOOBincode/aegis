package k8s

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aegis-dev/aegis/internal/authority"
)

func TestAuthorityWatcherBuildsInformerGraph(t *testing.T) {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Replicas: int32Ptr(2)}}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "shop"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "payments", APIVersion: "apps/v1"},
		},
	}
	w := NewAuthorityWatcher(fake.NewSimpleClientset(dep, hpa), time.Second, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()
	var g *authority.Graph
	var err error
	for i := 0; i < 30; i++ {
		g, err = w.Graph(context.Background())
		if err == nil && g != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || g == nil {
		t.Fatalf("graph=%v err=%v", g, err)
	}
	owners := g.Owners(authority.FieldRef{Kind: "Deployment", Namespace: "shop", Name: "payments", Field: "spec.replicas"})
	if len(owners) == 0 || owners[0].Kind != authority.OwnerHPA {
		t.Fatalf("owners=%v", owners)
	}
	cancel()
	select {
	case <-errCh:
	case <-time.After(time.Second):
		t.Fatal("watcher 未退出")
	}
}

func TestAuthorityWatcherRefreshErrorFailClosed(t *testing.T) {
	// 无 client 时 Graph 必须报错，不能返回空图。
	w := NewAuthorityWatcher(nil, time.Second, time.Second)
	if _, err := w.Graph(context.Background()); err == nil {
		t.Fatal("空 client 应 fail-closed")
	}
}
