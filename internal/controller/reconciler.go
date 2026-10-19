package controller

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
)

// DefaultGVR 是 ChangeRequest 的 GVR（与 crds/ 清单同步）。
var DefaultGVR = schema.GroupVersionResource{Group: "aegis.dev", Version: "v1alpha1", Resource: "changerequests"}

const finalizer = "aegis.dev/finalizer"

// Reconciler 是 ChangeRequest 的薄 reconcile 核心（M2 T2.1）。
// 它只做：finalizer、状态初始化、在途恢复动作的条件留痕、status 写回；
// 真正的 dry-run/执行/回滚由应用服务在后续 T2.8 接入，避免这里长业务分支。
type Reconciler struct {
	Client dynamic.Interface
	GVR    schema.GroupVersionResource
}

// NewReconciler 构造；GVR 为空用 DefaultGVR。
func NewReconciler(client dynamic.Interface) *Reconciler {
	return &Reconciler{Client: client, GVR: DefaultGVR}
}

// Reconcile 处理单个 ChangeRequest；返回 error 时由上层重入（本层不做指数退避）。
func (r *Reconciler) Reconcile(ctx context.Context, namespace, name string) error {
	if r.Client == nil {
		return fmt.Errorf("reconciler client 为空")
	}
	gvr := r.GVR
	if gvr.Resource == "" {
		gvr = DefaultGVR
	}
	u, err := r.Client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get ChangeRequest %s/%s: %w", namespace, name, err)
	}
	cr := &aegisv1.ChangeRequest{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cr); err != nil {
		return fmt.Errorf("decode ChangeRequest: %w", err)
	}

	if !cr.DeletionTimestamp.IsZero() {
		if IsTerminal(State(cr.Status.State)) {
			cr.Finalizers = removeString(cr.Finalizers, finalizer)
			return r.update(ctx, gvr, cr)
		}
		setCondition(cr, "DeletionBlocked", metav1.ConditionTrue, "InFlight", "在途 CR 不允许删除；先完成/回滚/中止")
		return r.updateStatus(ctx, gvr, cr)
	}

	if !containsString(cr.Finalizers, finalizer) {
		cr.Finalizers = append(cr.Finalizers, finalizer)
		if err := r.update(ctx, gvr, cr); err != nil {
			return err
		}
	}

	st := State(cr.Status.State)
	if st == "" {
		st = StatePending
		cr.Status.State = aegisv1.ChangeRequestState(st)
		cr.Status.ObservedGeneration = cr.Generation
		setCondition(cr, "Initialized", metav1.ConditionTrue, "Reconciled", "ChangeRequest 进入 Pending")
		return r.updateStatus(ctx, gvr, cr)
	}
	action, err := RecoveryAction(st)
	if err != nil {
		setCondition(cr, "InvalidState", metav1.ConditionFalse, "InvalidState", err.Error())
		return r.updateStatus(ctx, gvr, cr)
	}
	switch action {
	case ActionReconcile:
		// 重启/在途：只核对实际状态，绝不在此重放写操作（I10/F14）。
		setCondition(cr, "ActualStateCheckRequired", metav1.ConditionTrue, "InFlight", "Executing/Verifying 需先核对集群实际状态，不重复执行")
	case ActionRequeue:
		setCondition(cr, "RequeuePending", metav1.ConditionTrue, "Pending", "Pending/DryRunning 等待进入下一阶段")
	case ActionWaitApproval:
		setCondition(cr, "AwaitingApproval", metav1.ConditionTrue, "Approval", "等待审批")
	case ActionManual:
		setCondition(cr, "ManualInterventionRequired", metav1.ConditionTrue, "Manual", "Aborted/PartiallyRolledBack 需人工处理")
	case ActionNone:
		// 终态：不写 status，保持幂等。
		return nil
	}
	cr.Status.ObservedGeneration = cr.Generation
	return r.updateStatus(ctx, gvr, cr)
}

func (r *Reconciler) update(ctx context.Context, gvr schema.GroupVersionResource, cr *aegisv1.ChangeRequest) error {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
	if err != nil {
		return fmt.Errorf("encode ChangeRequest: %w", err)
	}
	_, err = r.Client.Resource(gvr).Namespace(cr.Namespace).Update(ctx, &unstructured.Unstructured{Object: u}, metav1.UpdateOptions{})
	return err
}

func (r *Reconciler) updateStatus(ctx context.Context, gvr schema.GroupVersionResource, cr *aegisv1.ChangeRequest) error {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
	if err != nil {
		return fmt.Errorf("encode ChangeRequest: %w", err)
	}
	_, err = r.Client.Resource(gvr).Namespace(cr.Namespace).UpdateStatus(ctx, &unstructured.Unstructured{Object: u}, metav1.UpdateOptions{})
	return err
}

func setCondition(cr *aegisv1.ChangeRequest, typ string, status metav1.ConditionStatus, reason, msg string) {
	cond := metav1.Condition{
		Type: typ, Status: status, Reason: reason, Message: msg,
		ObservedGeneration: cr.Generation, LastTransitionTime: metav1.Now(),
	}
	for i := range cr.Status.Conditions {
		if cr.Status.Conditions[i].Type == typ {
			cr.Status.Conditions[i] = cond
			return
		}
	}
	cr.Status.Conditions = append(cr.Status.Conditions, cond)
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func removeString(xs []string, drop string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != drop {
			out = append(out, x)
		}
	}
	return out
}
