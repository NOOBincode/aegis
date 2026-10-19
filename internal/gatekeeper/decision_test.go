package gatekeeper

import (
	"testing"

	"github.com/aegis-dev/aegis/internal/approval"
	"github.com/aegis-dev/aegis/internal/policy"
	"github.com/aegis-dev/aegis/internal/risk"
	"github.com/aegis-dev/aegis/internal/rollback"
)

func allowPolicy() policy.Policy {
	return policy.Policy{
		BlastRadiusQuota: policy.BlastRadiusQuota{MaxPods: 10, MaxNamespaces: 1},
		SessionBudget:    policy.SessionBudget{MaxToolCalls: 50, MaxLLMTokens: 200000},
	}
}

func scaleInput(delta int) Input {
	return Input{
		Risk:         risk.Input{Tool: "scale_deployment", Verb: "patch", Resource: "deployments", Namespace: "shop", Args: map[string]any{"delta": delta}},
		Policy:       allowPolicy(),
		Operations:   []rollback.Operation{{Tool: "scale_deployment", Args: map[string]any{"namespace": "shop", "name": "payments", "replicas": 4, "from_replicas": 2}}},
		DryRunPassed: true,
	}
}

func TestR1AutoAllowed(t *testing.T) {
	d := Evaluate(scaleInput(1))
	if !d.Allow || !d.AutoExecutable || d.Reject || d.RequiresApproval {
		t.Fatalf("R1 应自动放行：%+v", d)
	}
}

func TestIrreversibleRequiresApproval(t *testing.T) {
	in := scaleInput(1)
	in.Operations = []rollback.Operation{{Tool: "restart_pod", Args: map[string]any{"namespace": "shop", "name": "p"}}}
	if d := Evaluate(in); d.Allow || d.AutoExecutable || !d.RequiresApproval {
		t.Fatalf("不可逆应人审：%+v", d)
	}
}

func TestR2ApprovalLifecycle(t *testing.T) {
	in := scaleInput(3) // R2
	if d := Evaluate(in); d.Allow || !d.RequiresApproval {
		t.Fatalf("R2 pending 不应放行：%+v", d)
	}
	in.Approval = approval.Request{Required: true, State: approval.StateApproved, Approver: "bob"}
	if d := Evaluate(in); !d.Allow || d.AutoExecutable {
		t.Fatalf("R2 已批准应放行但不可自动：%+v", d)
	}
}

func TestR3RejectedEvenApproved(t *testing.T) {
	in := scaleInput(1)
	in.Risk.Namespace = "kube-system"
	in.Approval = approval.Request{State: approval.StateApproved, Approver: "bob"}
	d := Evaluate(in)
	if d.Allow || !d.Reject || !d.HardDeny {
		t.Fatalf("R3 即使有审批也应拒绝：%+v", d)
	}
}

func TestPolicyQuotaRejects(t *testing.T) {
	in := scaleInput(1)
	in.Risk.BlastRadius = risk.BlastRadius{Pods: 11}
	if d := Evaluate(in); d.Allow || !d.Reject {
		t.Fatalf("超配额应拒绝：%+v", d)
	}
}
