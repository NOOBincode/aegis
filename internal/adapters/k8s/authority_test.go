package k8s

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
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

func TestAuthorityEvents(t *testing.T) {
	now := metav1.Now()
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "hpa-rescale", Namespace: "shop"},
		InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Name: "payments", Namespace: "shop"},
		Source:         corev1.EventSource{Component: "horizontal-pod-autoscaler"},
		Reason:         "SuccessfulRescale",
		Message:        "New size: 4; reason: cpu",
		LastTimestamp:  now,
	}
	cs := fake.NewSimpleClientset(ev)
	p := NewAuthorityProvider(cs)
	evs, err := p.Events(context.Background(), authority.FieldRef{Kind: "Deployment", Namespace: "shop", Name: "payments", Field: "spec.replicas"}, now.Time.Add(-time.Minute), now.Time.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Controller != "hpa" || evs[0].Direction != "increase" {
		t.Fatalf("evs=%+v", evs)
	}
}
