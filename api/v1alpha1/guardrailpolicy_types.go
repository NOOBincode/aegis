package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// GuardrailPolicy 是护栏策略 YAML（设计文档 §5.2，M2 T2.1）。
// R3 hardDeny 额外由 gatekeeper 代码级清单强制（I1），本对象不能放宽它。
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=gp,scope=Namespaced
type GuardrailPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              GuardrailPolicySpec `json:"spec,omitempty"`
}

// GuardrailPolicySpec 是策略内容。
type GuardrailPolicySpec struct {
	ClusterSelector  map[string]string `json:"clusterSelector,omitempty"`
	HardDeny         []HardDenyRule    `json:"hardDeny,omitempty"`
	BlastRadiusQuota BlastRadiusQuota  `json:"blastRadiusQuota,omitempty"`
	RateLimit        RateLimitPolicy   `json:"rateLimit,omitempty"`
	SessionBudget    SessionBudget     `json:"sessionBudget,omitempty"`
	CircuitBreaker   CircuitBreaker    `json:"circuitBreaker,omitempty"`
	OPAPolicyBundle  string            `json:"opaPolicyBundle,omitempty"`
}

// HardDenyRule 是 R3 硬禁止清单条目。
type HardDenyRule struct {
	Verbs      []string `json:"verbs,omitempty"`
	Resources  []string `json:"resources,omitempty"`
	Namespaces []string `json:"namespaces,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// BlastRadiusQuota 是单次变更的爆炸半径配额（I7）。
type BlastRadiusQuota struct {
	MaxPodsPerChange       int `json:"maxPodsPerChange,omitempty"`
	MaxNodesPerChange      int `json:"maxNodesPerChange,omitempty"`
	MaxNamespacesPerChange int `json:"maxNamespacesPerChange,omitempty"`
}

// RateLimitPolicy 是变更/读路径速率限制。
type RateLimitPolicy struct {
	MaxChangesPerHour int `json:"maxChangesPerHour,omitempty"`
	MaxReadQPS        int `json:"maxReadQPS,omitempty"`
}

// SessionBudget 是会话成本上限（R4）。
type SessionBudget struct {
	MaxToolCalls int `json:"maxToolCalls,omitempty"`
	MaxLLMTokens int `json:"maxLLMTokens,omitempty"`
}

// CircuitBreaker 是熔断参数。
type CircuitBreaker struct {
	FailureRateThreshold float64 `json:"failureRateThreshold,omitempty"`
	Window               string  `json:"window,omitempty"`
	Action               string  `json:"action,omitempty"`
}
