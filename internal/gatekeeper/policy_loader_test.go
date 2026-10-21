package gatekeeper

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
	"github.com/aegis-dev/aegis/internal/policy"
)

func TestGuardrailPolicyLoader(t *testing.T) {
	spec := aegisv1.GuardrailPolicySpec{
		BlastRadiusQuota: aegisv1.BlastRadiusQuota{MaxPodsPerChange: 3, MaxNodesPerChange: 1},
		SessionBudget:    aegisv1.SessionBudget{MaxToolCalls: 9},
		HardDeny:         []aegisv1.HardDenyRule{{Verbs: []string{"delete"}, Resources: []string{"namespaces"}}},
	}
	obj := &aegisv1.GuardrailPolicy{TypeMeta: metav1.TypeMeta{APIVersion: "aegis.dev/v1alpha1", Kind: "GuardrailPolicy"}, ObjectMeta: metav1.ObjectMeta{Name: "aegis-default", Namespace: "kagent"}, Spec: spec}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatal(err)
	}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{guardrailPolicyGVR: "GuardrailPolicyList"}, &unstructured.Unstructured{Object: raw})
	loader := &GuardrailPolicyLoader{Client: dc, Namespace: "kagent", Name: "aegis-default"}
	p, err := loader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.BlastRadiusQuota.MaxPods != 3 || p.SessionBudget.MaxToolCalls != 9 || len(p.HardDeny) != 1 {
		t.Fatalf("policy=%+v", p)
	}
}

type failingLoader struct{}

func (failingLoader) Load(context.Context) (policy.Policy, error) {
	return policy.Policy{}, errors.New("policy unavailable")
}

func TestSubmitPolicyLoaderFailClosed(t *testing.T) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{changeRequestGVR: "ChangeRequestList"})
	svc := &ChangeRequestService{Client: dc, GVR: changeRequestGVR, PolicyLoader: failingLoader{}}
	if _, err := svc.Submit(context.Background(), submitFixture()); err == nil {
		t.Fatal("策略加载失败必须 fail-closed，不得落 CR")
	}
}

type staticLoader struct{ p policy.Policy }

func (s staticLoader) Load(context.Context) (policy.Policy, error) { return s.p, nil }

func TestSubmitUsesLoadedPolicy(t *testing.T) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{changeRequestGVR: "ChangeRequestList"})
	svc := &ChangeRequestService{Client: dc, GVR: changeRequestGVR, PolicyLoader: staticLoader{p: policy.Policy{BlastRadiusQuota: policy.BlastRadiusQuota{MaxPods: 1}}}}
	req := submitFixture()
	req.Operations[0].BlastRadius.Pods = 2
	got, err := svc.Submit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Decision.Reject || got.Created {
		t.Fatalf("超爆半径应拒绝且不落库：%+v", got)
	}
}
