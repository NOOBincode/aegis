package gatekeeper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
	"github.com/aegis-dev/aegis/internal/approval"
	"github.com/aegis-dev/aegis/internal/policy"
	"github.com/aegis-dev/aegis/internal/risk"
	"github.com/aegis-dev/aegis/internal/rollback"
)

// SubmitOperation 是 Agent/调用方提交的一次写意图；只描述意图与证据，不直接执行。
type SubmitOperation struct {
	Tool         string
	Verb         string
	Resource     string
	Namespace    string
	Args         map[string]any
	BlastRadius  risk.BlastRadius
	DryRunPassed bool
	Authority    aegisv1.AuthorityCheck
}

// SubmitRequest 是 gatekeeper 创建 ChangeRequest 的输入。
type SubmitRequest struct {
	SessionRef     string
	IdempotencyKey string
	Intent         string
	Source         string
	RequestedBy    string
	Operations     []SubmitOperation
	Policy         policy.Policy
	Approval       approval.Request
}

// SubmitResult 是提交结果；Created=false 表示幂等命中已有 CR。
type SubmitResult struct {
	Decision Decision
	Name     string
	Created  bool
}

// ChangeRequestService 负责“判定 + 落 ChangeRequest”；执行仍由 controller/gatekeeper
// executor 在后续推进，本服务不直接写业务集群资源。
type ChangeRequestService struct {
	Client dynamic.Interface
	GVR    schema.GroupVersionResource
	Now    func() time.Time
}

// Submit 创建幂等 ChangeRequest。R3 拒绝不落库；R2 落 AwaitingApproval；
// R1 落 Pending，等 controller 推进 dry-run/execute。
func (s *ChangeRequestService) Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error) {
	if s.Client == nil {
		return SubmitResult{}, fmt.Errorf("changerequest service client 为空")
	}
	if err := validateSubmit(req); err != nil {
		return SubmitResult{}, err
	}
	gvr := s.GVR
	if gvr.Resource == "" {
		gvr = changeRequestGVR
	}
	ops := make([]rollback.Operation, 0, len(req.Operations))
	riskOps := make([]risk.Input, 0, len(req.Operations))
	for _, op := range req.Operations {
		ro := rollback.Operation{Tool: op.Tool, Args: op.Args}
		ops = append(ops, ro)
		inv := rollback.Generate(ro)
		riskOps = append(riskOps, risk.Input{
			Tool: op.Tool, Verb: op.Verb, Resource: op.Resource, Namespace: op.Namespace,
			Args: op.Args, BlastRadius: op.BlastRadius, HasInverse: inv.Reversible,
			DryRunPassed: op.DryRunPassed,
		})
	}
	// 当前 Service.Submit 只对单操作做严格裁决；多操作按最后/第一条的风险合并后续扩展。
	if len(riskOps) == 0 {
		return SubmitResult{}, fmt.Errorf("operations 不能为空")
	}
	decInput := Input{
		Risk: riskOps[0], Policy: req.Policy, Operations: ops,
		Approval: req.Approval, DryRunPassed: req.Operations[0].DryRunPassed,
	}
	dec := Evaluate(decInput)
	name := crName(req.IdempotencyKey)
	if dec.Reject {
		return SubmitResult{Decision: dec, Name: name}, nil
	}
	cr := &aegisv1.ChangeRequest{
		TypeMeta:   metav1.TypeMeta{APIVersion: "aegis.dev/v1alpha1", Kind: "ChangeRequest"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kagent"},
		Spec: aegisv1.ChangeRequestSpec{
			SessionRef: req.SessionRef, RequestedBy: req.RequestedBy,
			IdempotencyKey: req.IdempotencyKey, Intent: req.Intent, Source: req.Source,
			Approval: aegisv1.Approval{Required: dec.RequiresApproval, State: "Pending", Channel: "cli"},
		},
	}
	if dec.RequiresApproval {
		cr.Status.State = aegisv1.StateAwaitingApproval
	} else {
		cr.Status.State = aegisv1.StatePending
	}
	for i, op := range req.Operations {
		inv := rollback.Generate(rollback.Operation{Tool: op.Tool, Args: op.Args})
		apiOp := aegisv1.ChangeOperation{
			Tool: op.Tool, Args: op.Args, RiskLevel: aegisv1.RiskLevel(dec.Risk.Level),
			AuthorityCheck: op.Authority, EstBlastRadius: aegisv1.BlastRadius{
				Pods: op.BlastRadius.Pods, Nodes: op.BlastRadius.Nodes, Namespaces: op.BlastRadius.Namespaces,
			},
			InverseVerified: inv.Reversible,
		}
		if op.DryRunPassed {
			apiOp.DryRunResult = aegisv1.DryRunPassed
		}
		if inv.Inverse != nil {
			apiOp.InverseOperation = &aegisv1.ChangeOperation{Tool: inv.Inverse.Tool, Args: inv.Inverse.Args}
		}
		_ = i
		cr.Spec.Operations = append(cr.Spec.Operations, apiOp)
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("encode ChangeRequest: %w", err)
	}
	created, err := s.Client.Resource(gvr).Namespace(cr.Namespace).Create(ctx, &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return SubmitResult{Decision: dec, Name: name, Created: false}, nil
	}
	if err != nil {
		return SubmitResult{}, fmt.Errorf("创建 ChangeRequest: %w", err)
	}
	// status 是 subresource：Create 时带的 status 会被 apiserver 忽略，必须显式 UpdateStatus。
	if _, err := s.Client.Resource(gvr).Namespace(cr.Namespace).UpdateStatus(ctx, created, metav1.UpdateOptions{}); err != nil {
		return SubmitResult{}, fmt.Errorf("初始化 ChangeRequest status: %w", err)
	}
	return SubmitResult{Decision: dec, Name: name, Created: true}, nil
}

func validateSubmit(req SubmitRequest) error {
	if strings.TrimSpace(req.SessionRef) == "" || strings.TrimSpace(req.IdempotencyKey) == "" ||
		strings.TrimSpace(req.Intent) == "" || strings.TrimSpace(req.Source) == "" {
		return fmt.Errorf("sessionRef/idempotencyKey/intent/source 必填")
	}
	if len(req.Operations) == 0 {
		return fmt.Errorf("operations 不能为空")
	}
	return nil
}

func crName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "cr-" + hex.EncodeToString(sum[:])[:12]
}

// 编译期保证 GVR 与 ApprovalServer 共用同一契约。
var _ = changeRequestGVR
