package k8s

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aegis-dev/aegis/internal/rollback"
)

func TestScaleExecutor(t *testing.T) {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Replicas: int32Ptr(2)}}
	cs := fake.NewSimpleClientset(dep)
	e := NewScaleExecutor(cs)
	op := rollback.Operation{Tool: "scale_deployment", Args: map[string]any{"namespace": "shop", "name": "payments", "replicas": 4}}
	if err := e.Execute(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if err := e.Verify(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	inv := rollback.Operation{Tool: "scale_deployment", Args: map[string]any{"namespace": "shop", "name": "payments", "replicas": 2}}
	if err := e.Rollback(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.AppsV1().Deployments("shop").Get(context.Background(), "payments", metav1.GetOptions{})
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 2 {
		t.Fatalf("replicas=%v", got.Spec.Replicas)
	}
}

func TestScaleExecutorRejectsOtherTools(t *testing.T) {
	e := NewScaleExecutor(fake.NewSimpleClientset())
	if err := e.Execute(context.Background(), rollback.Operation{Tool: "delete_deployment"}); err == nil {
		t.Fatal("非 scale 工具应拒绝")
	}
}
