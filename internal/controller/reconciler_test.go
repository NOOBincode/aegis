package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
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
			"operations": []any{map[string]any{"tool": "scale_deployment", "riskLevel": "R1"}},
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
