package scan

import "fmt"

// Severity 是 finding 的严重度（借鉴 kubeagent findings.go：Info < Warning < Critical）。
// A 层 Detector 命中一律 Critical；B 层 YAML 规则按规则声明的 level。
type Severity string

const (
	// SeverityInfo 提示级。
	SeverityInfo Severity = "info"
	// SeverityWarning 警告级。
	SeverityWarning Severity = "warning"
	// SeverityCritical 严重级（A 层 Detector 命中一律此级）。
	SeverityCritical Severity = "critical"
)

// rank 返回严重度排序权重（越大越严重），用于确定性排序。
func (s Severity) rank() int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}

// Confidence 是根因判定的置信度（借鉴 kubeagent confidence.go）。
// 直读到的 K8s 状态 = High；启发式/统计推断 = Medium。
type Confidence string

const (
	// ConfidenceHigh 直读到的 K8s 状态。
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium 启发式/统计推断。
	ConfidenceMedium Confidence = "medium"
)

// ObjRef 指向一个受影响的集群对象（不含任何敏感字段）。
type ObjRef struct {
	Kind      string `json:"kind"`      // Pod / Deployment / Node ...
	Namespace string `json:"namespace"` // 集群级对象为空串
	Name      string `json:"name"`
}

// String 返回稳定可读形式（用于排序与 Finding.ID 派生）。
func (o ObjRef) String() string {
	if o.Namespace == "" {
		return fmt.Sprintf("%s/%s", o.Kind, o.Name)
	}
	return fmt.Sprintf("%s/%s/%s", o.Kind, o.Namespace, o.Name)
}

// Finding 是一条确定性判定结果（设计文档 design.md §3.2）。
// 所有字符串字段在产出前须视为不可信数据并经消毒截断（I4）。
type Finding struct {
	// ID 稳定标识，形如 "<rule>/<obj>"，供审计/回放/回归比对。
	ID string `json:"id"`
	// RuleID 命中的规则：A 层为 detector id（如 "OOM_KILLED"），B 层为 "policy/<ruleID>" 以区分。
	RuleID string `json:"rule_id"`
	// Severity 严重度。
	Severity Severity `json:"severity"`
	// Object 受影响对象。
	Object ObjRef `json:"object"`
	// RootCause 确定性根因陈述（已脱敏）。
	RootCause string `json:"root_cause"`
	// Evidence 支撑证据（事件/状态摘要，已脱敏 + 截断）。
	Evidence []string `json:"evidence,omitempty"`
	// Confidence 置信度。
	Confidence Confidence `json:"confidence"`
	// Suggestion 处置建议（只读性提示，不触发写）。
	Suggestion string `json:"suggestion,omitempty"`
	// NextTier false=规则已结案（不惊动 LLM/人审）；true=残差需 Tier-1/2。
	// "规则层解决率"（M3 eval 一等指标，§4.6/R30）= NextTier=false 的占比。
	NextTier bool `json:"next_tier"`
}

// newFinding 构造 Finding 并派生稳定 ID（ruleID/objRef）。
// A/B 层 MVP 均为直读 K8s 状态判定，置信度固定 High；启发式/统计推断的
// Medium 置信度由具体 detector 在命中后显式下调（如 RestartLoop/ProbeFailure）。
func newFinding(ruleID string, obj ObjRef, sev Severity, rootCause string) Finding {
	return Finding{
		ID:         fmt.Sprintf("%s/%s", ruleID, obj.String()),
		RuleID:     ruleID,
		Severity:   sev,
		Object:     obj,
		RootCause:  rootCause,
		Confidence: ConfidenceHigh,
		NextTier:   false, // 默认规则结案；残差由具体判定显式置 true
	}
}
