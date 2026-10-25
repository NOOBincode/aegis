package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/util/workqueue"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
)

func TestEnqueueApprovalDecision(t *testing.T) {
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer queue.ShutDown()
	enqueueApprovalDecision(approvalDecisionForTest("uid-cr-test", "Approved"), queue, nil)
	item, shutdown := queue.Get()
	if shutdown {
		t.Fatal("queue unexpectedly shut down")
	}
	if item != "default/cr-test" {
		t.Fatalf("key=%q, want default/cr-test", item)
	}
	queue.Done(item)
}

func TestManagerConsumesApprovalDecision(t *testing.T) {
	obj := newFakeCR("AwaitingApproval", false)
	requireApproval(obj)
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		DefaultGVR: "ChangeRequestList", ApprovalDecisionGVR: "ApprovalDecisionList",
	}, obj)
	reconciler := NewReconciler(dc)
	reconciler.Executor = &fakeExec{}
	manager := &Manager{Client: dc, Reconciler: reconciler, Resync: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	time.Sleep(100 * time.Millisecond)
	if _, err := dc.Resource(ApprovalDecisionGVR).Namespace("default").Create(
		context.Background(), approvalDecisionForTest("uid-cr-test", "Approved"), metav1.CreateOptions{},
	); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	committed := false
	for time.Now().Before(deadline) {
		u, err := dc.Resource(DefaultGVR).Namespace("default").Get(context.Background(), "cr-test", metav1.GetOptions{})
		if err == nil {
			state, _, _ := unstructured.NestedString(u.Object, "status", "state")
			if state == string(aegisv1.StateCommitted) {
				committed = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	err := <-done
	if !committed {
		t.Fatalf("manager did not consume approval decision: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
}
