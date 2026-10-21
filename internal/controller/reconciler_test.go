package controller

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
	"github.com/aegis-dev/aegis/internal/rollback"
)

func newFakeCR(state string, deleting bool) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "aegis.dev/v1alpha1",
		"kind":       "ChangeRequest",
		"metadata": map[string]any{
			"name": "cr-test", "namespace": "default", "generation": int64(1),
		},
		"spec": map[string]any{
			"sessionRef": "s", "idempotencyKey": "k", "intent": "i", "source": "agent",
			"operations": []any{map[string]any{
				"tool": "scale_deployment", "riskLevel": "R1",
				"args": map[string]any{"namespace": "shop", "name": "payments", "replicas": int64(2), "from_replicas": int64(1)},
			}},
		},
	}
	if state != "" {
		obj["status"] = map[string]any{"state": state}
	}
	if deleting {
		meta, ok := obj["metadata"].(map[string]any)
		if !ok {
			panic("metadata shape")
		}
		meta["deletionTimestamp"] = "2026-01-01T00:00:00Z"
		meta["finalizers"] = []any{finalizer}
	}
	return &unstructured.Unstructured{Object: obj}
}

func newFake(t *testing.T, obj *unstructured.Unstructured) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		DefaultGVR: "ChangeRequestList",
	}, obj)
	return NewReconciler(dc)
}

func getCR(t *testing.T, r *Reconciler) *aegisv1.ChangeRequest {
	t.Helper()
	u, err := r.Client.Resource(DefaultGVR).Namespace("default").Get(context.Background(), "cr-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cr := &aegisv1.ChangeRequest{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cr); err != nil {
		t.Fatal(err)
	}
	return cr
}

func TestReconcileInitializesAndFinalizes(t *testing.T) {
	r := newFake(t, newFakeCR("", false))
	if err := r.Reconcile(context.Background(), "default", "cr-test"); err != nil {
		t.Fatal(err)
	}
	cr := getCR(t, r)
	if cr.Status.State != aegisv1.StatePending || !containsString(cr.Finalizers, finalizer) {
		t.Fatalf("cr=%+v", cr)
	}
}

func TestReconcileInFlightDoesNotExecute(t *testing.T) {
	r := newFake(t, newFakeCR(string(aegisv1.StateExecuting), false))
	if err := r.Reconcile(context.Background(), "default", "cr-test"); err != nil {
		t.Fatal(err)
	}
	cr := getCR(t, r)
	if cr.Status.State != aegisv1.StateExecuting {
		t.Fatalf("状态不应被 reconcile 重放：%s", cr.Status.State)
	}
	found := false
	for _, c := range cr.Status.Conditions {
		if c.Type == "ActualStateCheckRequired" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺实际状态核对条件：%+v", cr.Status.Conditions)
	}
}

func TestReconcileDeletion(t *testing.T) {
	r := newFake(t, newFakeCR(string(aegisv1.StateCommitted), true))
	if err := r.Reconcile(context.Background(), "default", "cr-test"); err != nil {
		t.Fatal(err)
	}
	cr := getCR(t, r)
	if containsString(cr.Finalizers, finalizer) {
		t.Fatalf("终态删除应移除 finalizer：%+v", cr.Finalizers)
	}
}

type fakeExec struct {
	execErr   error
	verifyErr error
	executed  int
	rolled    int
}

func (f *fakeExec) Execute(context.Context, rollback.Operation) error {
	if f.execErr != nil {
		return f.execErr
	}
	f.executed++
	return nil
}
func (f *fakeExec) Verify(context.Context, rollback.Operation) error {
	if f.verifyErr != nil {
		return f.verifyErr
	}
	return nil
}
func (f *fakeExec) Rollback(context.Context, rollback.Operation) error {
	f.rolled++
	return nil
}

func TestReconcileExecutesPending(t *testing.T) {
	r := newFake(t, newFakeCR("Pending", false))
	f := &fakeExec{}
	r.Executor = f
	if err := r.Reconcile(context.Background(), "default", "cr-test"); err != nil {
		t.Fatal(err)
	}
	cr := getCR(t, r)
	if cr.Status.State != aegisv1.StateCommitted || cr.Status.ExecutedOps != 1 || f.executed != 1 {
		t.Fatalf("state=%s executed=%d fake=%d", cr.Status.State, cr.Status.ExecutedOps, f.executed)
	}
}

func TestReconcileRollbackOnVerifyFailure(t *testing.T) {
	r := newFake(t, newFakeCR("Pending", false))
	f := &fakeExec{verifyErr: errors.New("unhealthy")}
	r.Executor = f
	if err := r.Reconcile(context.Background(), "default", "cr-test"); err != nil {
		t.Fatal(err)
	}
	cr := getCR(t, r)
	if cr.Status.State != aegisv1.StateRolledBack || f.rolled != 1 {
		t.Fatalf("state=%s rolled=%d", cr.Status.State, f.rolled)
	}
}

func TestReconcilePendingButApprovalRequiredDoesNotExecute(t *testing.T) {
	obj := newFakeCR("Pending", false)
	spec, _ := obj.Object["spec"].(map[string]any)
	spec["approval"] = map[string]any{"required": true}
	r := newFake(t, obj)
	f := &fakeExec{}
	r.Executor = f
	if err := r.Reconcile(context.Background(), "default", "cr-test"); err != nil {
		t.Fatal(err)
	}
	cr := getCR(t, r)
	if cr.Status.State != aegisv1.StateAwaitingApproval || f.executed != 0 {
		t.Fatalf("审批 required 未 Approved 不得执行：state=%s executed=%d", cr.Status.State, f.executed)
	}
}

func TestReconcileAwaitingApprovalDoesNotExecute(t *testing.T) {
	r := newFake(t, newFakeCR("AwaitingApproval", false))
	f := &fakeExec{}
	r.Executor = f
	if err := r.Reconcile(context.Background(), "default", "cr-test"); err != nil {
		t.Fatal(err)
	}
	cr := getCR(t, r)
	if cr.Status.State != aegisv1.StateAwaitingApproval || f.executed != 0 {
		t.Fatalf("未审批不应执行：state=%s fake=%d", cr.Status.State, f.executed)
	}
}

func TestReconcileDeletedCRDropsQueueItem(t *testing.T) {
	r := newFake(t, newFakeCR("Pending", false))
	if err := r.Reconcile(context.Background(), "default", "does-not-exist"); err != nil {
		t.Fatalf("NotFound 应静默丢弃队列项，得到：%v", err)
	}
}
