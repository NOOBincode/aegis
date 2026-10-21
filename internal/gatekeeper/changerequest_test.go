package gatekeeper

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
	"github.com/aegis-dev/aegis/internal/policy"
	"github.com/aegis-dev/aegis/internal/risk"
)

func submitFixture() SubmitRequest {
	return SubmitRequest{
		SessionRef: "s", IdempotencyKey: "intent-1", Intent: "scale", Source: "agent", RequestedBy: "alice",
		Policy: policy.Policy{BlastRadiusQuota: policy.BlastRadiusQuota{MaxPods: 10}},
		Operations: []SubmitOperation{{
			Tool: "scale_deployment", Verb: "patch", Resource: "deployments", Namespace: "shop",
			Args:        map[string]any{"namespace": "shop", "name": "payments", "replicas": 2, "from_replicas": 1},
			BlastRadius: risk.BlastRadius{Pods: 1, Namespaces: 1}, DryRunPassed: true,
		}},
	}
}

func newSubmitService(t *testing.T) *ChangeRequestService {
	t.Helper()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{changeRequestGVR: "ChangeRequestList"})
	return &ChangeRequestService{Client: dc, GVR: changeRequestGVR}
}

func TestSubmitR1CreatesPendingAndIdempotent(t *testing.T) {
	s := newSubmitService(t)
	got, err := s.Submit(context.Background(), submitFixture())
	if err != nil || !got.Created || got.Decision.Reject || got.Decision.RequiresApproval {
		t.Fatalf("R1 不应需审批：got=%+v err=%v", got, err)
	}
	again, err := s.Submit(context.Background(), submitFixture())
	if err != nil || again.Created {
		t.Fatalf("幂等应折叠：%+v err=%v", again, err)
	}
	if got.Name != again.Name {
		t.Fatalf("name=%q/%q", got.Name, again.Name)
	}
	u, err := s.Client.Resource(changeRequestGVR).Namespace("kagent").Get(context.Background(), got.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ := unstructured.NestedString(u.Object, "status", "state")
	if state != string(aegisv1.StatePending) {
		t.Fatalf("status state=%q object=%v", state, u.Object["status"])
	}
}

func TestSubmitR3RejectedNoObject(t *testing.T) {
	s := newSubmitService(t)
	req := submitFixture()
	req.Operations[0].Namespace = "kube-system"
	got, err := s.Submit(context.Background(), req)
	if err != nil || !got.Decision.Reject || got.Created {
		t.Fatalf("R3 应拒绝且不落库：%+v err=%v", got, err)
	}
}

func TestSubmitR2AwaitingApproval(t *testing.T) {
	s := newSubmitService(t)
	req := submitFixture()
	req.Operations[0].Args["replicas"] = 3
	got, err := s.Submit(context.Background(), req)
	if err != nil || !got.Created || !got.Decision.RequiresApproval {
		t.Fatalf("R2 应落 AwaitingApproval：%+v err=%v", got, err)
	}
	u, err := s.Client.Resource(changeRequestGVR).Namespace("kagent").Get(context.Background(), got.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ := unstructured.NestedString(u.Object, "status", "state")
	if state != string(aegisv1.StateAwaitingApproval) {
		t.Fatalf("R2 status state=%q want AwaitingApproval", state)
	}
}

type fakeAuthority struct{ owner string }

func (f fakeAuthority) CheckScale(context.Context, string, string) (string, string, error) {
	return f.owner, "via-owner-api", nil
}

func TestSubmitRejectsOwnedField(t *testing.T) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{changeRequestGVR: "ChangeRequestList"})
	svc := &ChangeRequestService{Client: dc, GVR: changeRequestGVR, Authority: fakeAuthority{owner: "hpa/payments"}}
	got, err := svc.Submit(context.Background(), submitFixture())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Decision.Reject || got.Created {
		t.Fatalf("有主字段必须拒绝且不落库：%+v", got)
	}
}
