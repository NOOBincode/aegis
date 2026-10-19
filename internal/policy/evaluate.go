// Package policy 实现 GuardrailPolicy 的确定性求值（M2 T2.3 的纯领域部分）。
//
// 输入是策略 + 一次变更请求事实（动词/资源/命名空间/影响面/速率/预算/熔断），
// 输出 allow/reject/requires-approval/force-readonly 与可审计原因。OPA/Rego 是
// 适配器实现细节（M2 T2.3 后半），本包不 import OPA/k8s，便于纯单测与 fail-closed。
package policy

import (
	"fmt"
	"strings"
)

// Policy 是 GuardrailPolicy spec 的纯领域映射（字段与 api/v1alpha1 对齐）。
type Policy struct {
	HardDeny         []HardDenyRule
	BlastRadiusQuota BlastRadiusQuota
	RateLimit        RateLimit
	SessionBudget    SessionBudget
	CircuitBreaker   CircuitBreaker
	OPAPolicyBundle  string
}

// HardDenyRule 是 R3 硬拒规则；空字段视为不限制，`*` 匹配任意。
type HardDenyRule struct {
	Verbs      []string
	Resources  []string
	Namespaces []string
	Note       string
}

// BlastRadiusQuota 是单次变更影响面上限；0 表示不限制。
type BlastRadiusQuota struct {
	MaxPods       int
	MaxNodes      int
	MaxNamespaces int
}

// RateLimit 是变更/读路径速率上限。
type RateLimit struct {
	MaxChangesPerHour int
	MaxReadQPS        int
}

// SessionBudget 是会话成本上限。
type SessionBudget struct {
	MaxToolCalls int
	MaxLLMTokens int
}

// CircuitBreaker 是失败率熔断参数。
type CircuitBreaker struct {
	FailureRateThreshold float64
	Window               string
	Action               string
}

// BlastRadius 是本次变更影响面。
type BlastRadius struct {
	Pods       int
	Nodes      int
	Namespaces int
}

// Request 是一次策略求值输入；RiskLevel 由 risk-classifier 给出。
type Request struct {
	Verb            string
	Resource        string
	Namespace       string
	RiskLevel       string
	BlastRadius     BlastRadius
	ChangesLastHour int
	ToolCalls       int
	LLMTokens       int
	FailureRate     float64
}

// Decision 是策略结果。Reject=true 时禁止；RequiresApproval=true 时必须人审；
// 两者都为 false 才允许自动执行。
type Decision struct {
	Allow            bool
	Reject           bool
	RequiresApproval bool
	ForceReadOnly    bool
	HardDeny         bool
	Reasons          []string
}

// Evaluate 执行确定性策略求值。未知风险级别 fail-closed 为人审。
func Evaluate(p Policy, req Request) Decision {
	d := Decision{Allow: true}
	verb := strings.ToLower(req.Verb)
	res := strings.ToLower(req.Resource)
	ns := strings.ToLower(req.Namespace)
	read := isRead(verb)

	if !read {
		// 代码级保护域不依赖 policy 是否配置（I1：策略不能放宽硬拒）。
		if ns == "kube-system" || ns == "gatekeeper-system" || ns == "kube-public" || ns == "kube-node-lease" {
			d.Reject = true
			d.HardDeny = true
			d.Reasons = append(d.Reasons, "保护命名空间写操作 R3 硬拒："+ns)
		}
		for _, rule := range p.HardDeny {
			if match(rule, verb, res, ns) {
				d.Reject = true
				d.HardDeny = true
				note := rule.Note
				if note == "" {
					note = "hardDeny 命中"
				}
				d.Reasons = append(d.Reasons, note)
				break
			}
		}
		if isSecretsWrite(res, verb) {
			d.Reject = true
			d.HardDeny = true
			d.Reasons = append(d.Reasons, "写 Secret 是 R3 硬拒")
		}
	}

	if !read {
		q := p.BlastRadiusQuota
		if (q.MaxPods > 0 && req.BlastRadius.Pods > q.MaxPods) ||
			(q.MaxNodes > 0 && req.BlastRadius.Nodes > q.MaxNodes) ||
			(q.MaxNamespaces > 0 && req.BlastRadius.Namespaces > q.MaxNamespaces) {
			d.Reject = true
			d.Reasons = append(d.Reasons, fmt.Sprintf("爆炸半径超配额：%+v", req.BlastRadius))
		}
		if p.RateLimit.MaxChangesPerHour > 0 && req.ChangesLastHour >= p.RateLimit.MaxChangesPerHour {
			d.Reject = true
			d.Reasons = append(d.Reasons, "每小时变更数超限")
		}
	}
	if p.SessionBudget.MaxToolCalls > 0 && req.ToolCalls >= p.SessionBudget.MaxToolCalls {
		d.Reject = true
		d.Reasons = append(d.Reasons, "会话工具调用预算耗尽")
	}
	if p.SessionBudget.MaxLLMTokens > 0 && req.LLMTokens >= p.SessionBudget.MaxLLMTokens {
		d.Reject = true
		d.Reasons = append(d.Reasons, "会话 token 预算耗尽")
	}
	if p.CircuitBreaker.FailureRateThreshold > 0 && req.FailureRate > p.CircuitBreaker.FailureRateThreshold {
		d.ForceReadOnly = true
		d.Reject = true
		d.Reasons = append(d.Reasons, "熔断：失败率超阈值，降级只读")
	}

	// R2 必须人审；未知级别 fail-closed 为人审（R3 已被硬拒覆盖）。
	switch strings.ToUpper(strings.TrimSpace(req.RiskLevel)) {
	case "R0", "R1":
		// 不需要审批
	case "R2":
		d.RequiresApproval = true
		d.Reasons = append(d.Reasons, "R2 强制人审")
	case "R3":
		d.Reject = true
		d.HardDeny = true
		d.Reasons = append(d.Reasons, "R3 硬拒")
	default:
		d.RequiresApproval = true
		d.Reasons = append(d.Reasons, "未知风险级别 fail-closed 人审")
	}

	if d.Reject || d.RequiresApproval {
		d.Allow = false
	}
	return d
}

func isRead(verb string) bool {
	return verb == "get" || verb == "list" || verb == "watch" || verb == "describe"
}

func isSecretsWrite(res, verb string) bool {
	if res != "secrets" {
		return false
	}
	return verb == "create" || verb == "update" || verb == "patch" || verb == "delete"
}

func match(rule HardDenyRule, verb, res, ns string) bool {
	if !matchAny(rule.Verbs, verb) || !matchAny(rule.Resources, res) || !matchAny(rule.Namespaces, ns) {
		return false
	}
	// 三组都为空等于未配置，不命中；避免空规则误杀。
	return len(rule.Verbs)+len(rule.Resources)+len(rule.Namespaces) > 0
}

func matchAny(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if p == "*" || strings.EqualFold(p, value) {
			return true
		}
	}
	return false
}
