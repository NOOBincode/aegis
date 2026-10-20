package k8s

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aegis-dev/aegis/internal/authority"
)

func TestBuildGraphHPA(t *testing.T) {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "shop"},
		Spec: appsv1.DeploymentSpec{Replicas: int32Ptr(2)}}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-hpa", Namespace: "shop"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "payments", APIVersion: "apps/v1"},
		},
	}
	cs := fake.NewSimpleClientset(dep, hpa)
	g, err := NewAuthorityProvider(cs).BuildGraph(context.Background(), []string{"shop"})
	if err != nil {
		t.Fatal(err)
	}
	ref := authority.FieldRef{Kind: "Deployment", Namespace: "shop", Name: "payments", Field: "spec.replicas"}
	owners := g.Owners(ref)
	if len(owners) != 1 || owners[0].Kind != authority.OwnerHPA {
		t.Fatalf("owners=%+v", owners)
	}
	d, _, _ := g.Check(ref)
	if d != authority.DecisionViaOwnerAPI {
		t.Fatalf("decision=%s", d)
	}
}

func int32Ptr(i int32) *int32 { return &i }
