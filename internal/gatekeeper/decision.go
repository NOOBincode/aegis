// Package gatekeeper 是 L2 闸门的纯决策编排（M2 T2.8 的领域核心）。
//
// 它只回答“是否放行/是否人审/是否拒绝”，不执行写、不访问 apiserver；
// 执行、MCP 代理、mTLS 在 cmd/gatekeeper/adapters 层。这样 risk/policy/
// approval/rollback 的裁决可被纯单测覆盖，网络层只做认证与转发。
package gatekeeper

import (
	"context"

	"github.com/aegis-dev/aegis/internal/approval"
	"github.com/aegis-dev/aegis/internal/policy"
	"github.com/aegis-dev/aegis/internal/risk"
	"github.com/aegis-dev/aegis/internal/rollback"
)

// Input 是一次变更的完整裁决输入。
type Input struct {
	Risk            risk.Input
	Policy          policy.Policy
	Operations      []rollback.Operation
	Approval        approval.Request
	ChangesLastHour int
	ToolCalls       int
	LLMTokens       int
	FailureRate     float64
	DryRunPassed    bool
}

// Decision 是闸门最终裁决；Reason 可进审计，不包含 Secret/原日志。
type Decision struct {
	Allow            bool
	Reject           bool
	HardDeny         bool
	RequiresApproval bool
	AutoExecutable   bool
	Reasons          []string
	Risk             risk.Decision
	Policy           policy.Decision
	Inverses         []rollback.Result
}

// Evaluate 串联 risk → inverse → policy → approval。任何未知/失败默认拒绝或人审。
func Evaluate(in Input) Decision {
	d := Decision{}
	in.Risk.HasInverse = true
	in.Risk.DryRunPassed = in.DryRunPassed
	for _, op := range in.Operations {
		inv := rollback.Generate(op)
		d.Inverses = append(d.Inverses, inv)
		if !inv.Reversible {
			in.Risk.HasInverse = false
			d.Reasons = append(d.Reasons, "不可逆操作："+inv.Reason)
		}
	}
	d.Risk = risk.Classify(in.Risk)
	d.Reasons = append(d.Reasons, d.Risk.Reasons...)

	pReq := policy.Request{
		Verb: in.Risk.Verb, Resource: in.Risk.Resource, Namespace: in.Risk.Namespace,
		RiskLevel: string(d.Risk.Level), BlastRadius: policy.BlastRadius{
			Pods: in.Risk.BlastRadius.Pods, Nodes: in.Risk.BlastRadius.Nodes, Namespaces: in.Risk.BlastRadius.Namespaces,
		},
		ChangesLastHour: in.ChangesLastHour, ToolCalls: in.ToolCalls,
		LLMTokens: in.LLMTokens, FailureRate: in.FailureRate,
	}
	d.Policy = policy.Evaluate(in.Policy, pReq)
	d.Reasons = append(d.Reasons, d.Policy.Reasons...)

	d.HardDeny = d.Risk.HardDeny || d.Policy.HardDeny
	d.Reject = d.Risk.Level == risk.LevelR3 || d.Policy.Reject
	d.RequiresApproval = d.Risk.RequiresHuman || d.Policy.RequiresApproval || !in.Risk.HasInverse
	if d.RequiresApproval {
		d.Reasons = append(d.Reasons, "需要人审（最终状态必须为 Approved）")
	}
	if d.Reject {
		d.Allow = false
		d.AutoExecutable = false
		return d
	}
	if d.RequiresApproval && in.Approval.State != approval.StateApproved {
		d.Allow = false
		d.AutoExecutable = false
		return d
	}
	d.Allow = true
	d.AutoExecutable = !d.RequiresApproval && d.Risk.AutoExecutable && d.Policy.Allow
	return d
}

// PolicyEngine 是 OPA/Rego 求值端口；实现由 internal/adapters/opa 提供。
// 超时/错误必须由调用方视为拒绝（fail-closed），本函数不会把错误当允许。
type PolicyEngine interface {
	Evaluate(ctx context.Context, path string, input any) (allowed bool, reason string, err error)
}

// EvaluateWithOPA 在纯领域决策之上叠加 OPA 策略；OPA 失败/拒绝一律使最终结果拒绝。
// 纯 policy 已拒绝时不再调用 OPA（省一次外部调用）；OPA 只做额外否决，不做放行覆盖。
func EvaluateWithOPA(ctx context.Context, in Input, engine PolicyEngine, path string, opaInput any) Decision {
	d := Evaluate(in)
	if engine == nil {
		return d
	}
	allowed, reason, err := engine.Evaluate(ctx, path, opaInput)
	if err != nil {
		d.Allow = false
		d.Reject = true
		d.AutoExecutable = false
		d.Reasons = append(d.Reasons, "OPA 不可用/超时，fail-closed 拒绝："+err.Error())
		return d
	}
	if !allowed {
		d.Allow = false
		d.Reject = true
		d.AutoExecutable = false
		if reason == "" {
			reason = "OPA 策略拒绝"
		}
		d.Reasons = append(d.Reasons, reason)
	}
	return d
}
