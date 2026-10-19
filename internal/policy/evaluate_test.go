package policy

import "testing"

func basePolicy() Policy {
	return Policy{
		HardDeny: []HardDenyRule{
			{Verbs: []string{"delete"}, Resources: []string{"namespaces"}},
			{Resources: []string{"clusterroles", "clusterrolebindings"}},
		},
		BlastRadiusQuota: BlastRadiusQuota{MaxPods: 10, MaxNodes: 1, MaxNamespaces: 1},
		RateLimit:        RateLimit{MaxChangesPerHour: 20},
		SessionBudget:    SessionBudget{MaxToolCalls: 50, MaxLLMTokens: 200000},
		CircuitBreaker:   CircuitBreaker{FailureRateThreshold: 0.3},
	}
}

func TestHardDenyAndProtection(t *testing.T) {
	for _, req := range []Request{
		{Verb: "delete", Resource: "namespaces", RiskLevel: "R1"},
		{Verb: "patch", Resource: "clusterroles", RiskLevel: "R1"},
		{Verb: "patch", Resource: "deployments", Namespace: "kube-system", RiskLevel: "R1"},
		{Verb: "patch", Resource: "secrets", RiskLevel: "R1"},
	} {
		d := Evaluate(basePolicy(), req)
		if !d.Reject || !d.HardDeny || d.Allow {
			t.Fatalf("应硬拒：req=%+v d=%+v", req, d)
		}
	}
}

func TestReadAllowed(t *testing.T) {
	d := Evaluate(basePolicy(), Request{Verb: "list", Resource: "pods", Namespace: "kube-system", RiskLevel: "R0"})
	if d.Reject || !d.Allow {
		t.Fatalf("读操作应允许：%+v", d)
	}
}

func TestQuotaRateBudget(t *testing.T) {
	if d := Evaluate(basePolicy(), Request{Verb: "patch", Resource: "deployments", RiskLevel: "R1", BlastRadius: BlastRadius{Pods: 11}}); !d.Reject {
		t.Fatalf("超影响面应拒绝：%+v", d)
	}
	if d := Evaluate(basePolicy(), Request{Verb: "patch", Resource: "deployments", RiskLevel: "R1", ChangesLastHour: 20}); !d.Reject {
		t.Fatalf("超速率应拒绝：%+v", d)
	}
	if d := Evaluate(basePolicy(), Request{Verb: "patch", Resource: "deployments", RiskLevel: "R1", ToolCalls: 50}); !d.Reject {
		t.Fatalf("超预算应拒绝：%+v", d)
	}
}

func TestCircuitBreakerForcesReadOnly(t *testing.T) {
	d := Evaluate(basePolicy(), Request{Verb: "patch", Resource: "deployments", RiskLevel: "R1", FailureRate: 0.5})
	if !d.Reject || !d.ForceReadOnly {
		t.Fatalf("熔断应降级只读：%+v", d)
	}
}

func TestApprovalByRisk(t *testing.T) {
	if d := Evaluate(basePolicy(), Request{Verb: "patch", Resource: "deployments", RiskLevel: "R1"}); d.RequiresApproval || !d.Allow {
		t.Fatalf("R1 不应审批：%+v", d)
	}
	if d := Evaluate(basePolicy(), Request{Verb: "patch", Resource: "deployments", RiskLevel: "R2"}); !d.RequiresApproval || d.Allow {
		t.Fatalf("R2 应审批：%+v", d)
	}
	if d := Evaluate(basePolicy(), Request{Verb: "patch", Resource: "deployments", RiskLevel: ""}); !d.RequiresApproval {
		t.Fatalf("未知级别应 fail-closed 审批：%+v", d)
	}
}
