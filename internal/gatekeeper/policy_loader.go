package gatekeeper

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	aegisv1 "github.com/aegis-dev/aegis/api/v1alpha1"
	"github.com/aegis-dev/aegis/internal/policy"
)

var guardrailPolicyGVR = schema.GroupVersionResource{Group: "aegis.dev", Version: "v1alpha1", Resource: "guardrailpolicies"}

// PolicyLoader 是 gatekeeper MCP 的策略来源端口；失败必须 fail-closed，不允许
// “读不到策略就按空策略放行”。
type PolicyLoader interface {
	Load(ctx context.Context) (policy.Policy, error)
}

// GuardrailPolicyLoader 从 kagent 命名空间的 GuardrailPolicy CR 读取默认策略。
type GuardrailPolicyLoader struct {
	Client    dynamic.Interface
	Namespace string
	Name      string
}

// Load 读取并转换策略；不存在/解码失败都返回错误。
func (l *GuardrailPolicyLoader) Load(ctx context.Context) (policy.Policy, error) {
	if l.Client == nil {
		return policy.Policy{}, fmt.Errorf("guardrail policy client 为空")
	}
	ns := l.Namespace
	if ns == "" {
		ns = "kagent"
	}
	name := l.Name
	if name == "" {
		name = "aegis-default"
	}
	u, err := l.Client.Resource(guardrailPolicyGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return policy.Policy{}, fmt.Errorf("读取 GuardrailPolicy %s/%s: %w", ns, name, err)
	}
	spec := aegisv1.GuardrailPolicySpec{}
	if raw, ok := u.Object["spec"].(map[string]any); ok {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &spec); err != nil {
			return policy.Policy{}, fmt.Errorf("解码 GuardrailPolicy: %w", err)
		}
	}
	return policyFromSpec(spec), nil
}

func policyFromSpec(spec aegisv1.GuardrailPolicySpec) policy.Policy {
	p := policy.Policy{
		BlastRadiusQuota: policy.BlastRadiusQuota{
			MaxPods: spec.BlastRadiusQuota.MaxPodsPerChange, MaxNodes: spec.BlastRadiusQuota.MaxNodesPerChange,
			MaxNamespaces: spec.BlastRadiusQuota.MaxNamespacesPerChange,
		},
		RateLimit: policy.RateLimit{
			MaxChangesPerHour: spec.RateLimit.MaxChangesPerHour, MaxReadQPS: spec.RateLimit.MaxReadQPS,
		},
		SessionBudget: policy.SessionBudget{
			MaxToolCalls: spec.SessionBudget.MaxToolCalls, MaxLLMTokens: spec.SessionBudget.MaxLLMTokens,
		},
		CircuitBreaker: policy.CircuitBreaker{
			FailureRateThreshold: spec.CircuitBreaker.FailureRateThreshold,
			Window:               spec.CircuitBreaker.Window, Action: spec.CircuitBreaker.Action,
		},
		OPAPolicyBundle: spec.OPAPolicyBundle,
	}
	for _, r := range spec.HardDeny {
		p.HardDeny = append(p.HardDeny, policy.HardDenyRule{
			Verbs: r.Verbs, Resources: r.Resources, Namespaces: r.Namespaces, Note: r.Note,
		})
	}
	return p
}
