package gatekeeper

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

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
	if err != nil || !got.Created || got.Decision.Reject {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	again, err := s.Submit(context.Background(), submitFixture())
	if err != nil || again.Created {
		t.Fatalf("幂等应折叠：%+v err=%v", again, err)
	}
	if got.Name != again.Name {
		t.Fatalf("name=%q/%q", got.Name, again.Name)
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
}
