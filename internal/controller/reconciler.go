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
	"github.com/aegis-dev/aegis/internal/rollback"
)

// DefaultGVR 是 ChangeRequest 的 GVR（与 crds/ 清单同步）。
var DefaultGVR = schema.GroupVersionResource{Group: "aegis.dev", Version: "v1alpha1", Resource: "changerequests"}

const finalizer = "aegis.dev/finalizer"

// Executor 是 controller 侧写执行端口；实现由 adapters/k8s 提供。nil 时只做
// finalizer/状态初始化，不执行任何写（controller-lite 安全默认）。
type Executor interface {
	Execute(ctx context.Context, op rollback.Operation) error
	Verify(ctx context.Context, op rollback.Operation) error
	Rollback(ctx context.Context, inverse rollback.Operation) error
}

// Reconciler 是 ChangeRequest 的薄 reconcile 核心（M2 T2.1）。
type Reconciler struct {
	Client   dynamic.Interface
	GVR      schema.GroupVersionResource
	Executor Executor
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
	// Pending / AwaitingApproval+Approved 是执行入口；Executing/Verifying 只做实际状态核对，
	// 绝不重放写操作。
	if st == StatePending || (st == StateAwaitingApproval && cr.Status.Approval.State == "Approved") {
		return r.execute(ctx, gvr, cr)
	}
	action, err := RecoveryAction(st)
	if err != nil {
		setCondition(cr, "InvalidState", metav1.ConditionFalse, "InvalidState", err.Error())
		return r.updateStatus(ctx, gvr, cr)
	}
	if action == ActionReconcile {
		// 在途恢复：核对已执行操作的期望状态，不重复执行。
		return r.verifyInFlight(ctx, gvr, cr)
	}
	switch action {
	case ActionReconcile:
		// already handled by verifyInFlight above; keep guard for future.
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

func (r *Reconciler) execute(ctx context.Context, gvr schema.GroupVersionResource, cr *aegisv1.ChangeRequest) error {
	if r.Executor == nil {
		setCondition(cr, "ExecutorNotWired", metav1.ConditionFalse, "SafetyDefault", "Executor 未接线，拒绝执行写操作")
		cr.Status.ObservedGeneration = cr.Generation
		return r.updateStatus(ctx, gvr, cr)
	}
	cr.Status.State = aegisv1.StateExecuting
	cr.Status.ObservedGeneration = cr.Generation
	if err := r.updateStatus(ctx, gvr, cr); err != nil {
		return err
	}
	executed := make([]int, 0, len(cr.Spec.Operations))
	for i := range cr.Spec.Operations {
		op := toRollbackOperation(cr.Spec.Operations[i])
		if err := r.Executor.Execute(ctx, op); err != nil {
			cr.Status.ExecutedOps = len(executed)
			r.rollback(ctx, cr, executed)
			return r.updateStatus(ctx, gvr, cr)
		}
		executed = append(executed, i)
		if err := r.Executor.Verify(ctx, op); err != nil {
			cr.Status.ExecutedOps = len(executed)
			r.rollback(ctx, cr, executed)
			return r.updateStatus(ctx, gvr, cr)
		}
	}
	cr.Status.ExecutedOps = len(executed)
	cr.Status.State = aegisv1.StateVerifying
	cr.Status.Verification = aegisv1.Verification{Healthy: true}
	cr.Status.State = aegisv1.StateCommitted
	setCondition(cr, "Committed", metav1.ConditionTrue, "Verified", "执行并验证通过")
	return r.updateStatus(ctx, gvr, cr)
}

func (r *Reconciler) verifyInFlight(ctx context.Context, gvr schema.GroupVersionResource, cr *aegisv1.ChangeRequest) error {
	if r.Executor == nil {
		setCondition(cr, "ActualStateCheckRequired", metav1.ConditionTrue, "InFlight", "Executing/Verifying 需先核对集群实际状态，不重复执行")
		setCondition(cr, "ExecutorNotWired", metav1.ConditionFalse, "SafetyDefault", "Executor 未接线，无法核对在途状态")
		return r.updateStatus(ctx, gvr, cr)
	}
	for i := range cr.Spec.Operations {
		op := toRollbackOperation(cr.Spec.Operations[i])
		if err := r.Executor.Verify(ctx, op); err != nil {
			cr.Status.State = aegisv1.StateVerifying
			cr.Status.ExecutedOps = i + 1
			setCondition(cr, "VerificationFailed", metav1.ConditionFalse, "Unhealthy", err.Error())
			return r.updateStatus(ctx, gvr, cr)
		}
	}
	cr.Status.State = aegisv1.StateCommitted
	cr.Status.Verification = aegisv1.Verification{Healthy: true}
	setCondition(cr, "Committed", metav1.ConditionTrue, "Recovered", "在途恢复核对通过")
	return r.updateStatus(ctx, gvr, cr)
}

func (r *Reconciler) rollback(ctx context.Context, cr *aegisv1.ChangeRequest, executed []int) {
	if len(executed) == 0 {
		return
	}
	cr.Status.Rollback.Attempted = true
	allOK := true
	for i := len(executed) - 1; i >= 0; i-- {
		op := toRollbackOperation(cr.Spec.Operations[executed[i]])
		inv := rollback.Generate(op)
		if !inv.Reversible || inv.Inverse == nil {
			allOK = false
			continue
		}
		if err := r.Executor.Rollback(ctx, *inv.Inverse); err != nil {
			allOK = false
		}
	}
	if allOK {
		cr.Status.State = aegisv1.StateRolledBack
		cr.Status.Rollback.Result = "ok"
		setCondition(cr, "RolledBack", metav1.ConditionTrue, "Rollback", "失败后逆序回滚成功")
	} else {
		cr.Status.State = aegisv1.StatePartiallyRolledBack
		cr.Status.Rollback.Result = "partial"
		setCondition(cr, "RollbackPartial", metav1.ConditionFalse, "Rollback", "回滚不完整，需人工")
	}
}

func toRollbackOperation(op aegisv1.ChangeOperation) rollback.Operation {
	return rollback.Operation{Tool: op.Tool, Args: op.Args}
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
