package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// DecisionRecord 是一次诊断会话的审计对象（设计文档 §5.3，M1 T1.8）。
//
// M1 只定义可落盘/可回放的契约，不做 admission 不可变与 WORM 导出（M2 加固）。
// 记录内容必须脱敏：禁止出现 API key/Secret/完整日志原文；toolCalls 只存
// 参数预览与结果哈希/预览（已由 internal/redact 处理）。
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=dr,scope=Namespaced
// +kubebuilder:printcolumn:name="Session",type=string,JSONPath=`.spec.sessionRef`
// +kubebuilder:printcolumn:name="Outcome",type=string,JSONPath=`.spec.outcome`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type DecisionRecord struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DecisionRecordSpec `json:"spec,omitempty"`
}

// DecisionRecordSpec 是 DecisionRecord 的完整字段集（§5.3）。
type DecisionRecordSpec struct {
	SessionRef         string              `json:"sessionRef"`
	ClusterSnapshotRef string              `json:"clusterSnapshotRef,omitempty"`
	LLMTraceRef        string              `json:"llmTraceRef,omitempty"`
	ToolCalls          []ToolCall          `json:"toolCalls,omitempty"`
	SanitizationEvents []SanitizationEvent `json:"sanitizationEvents,omitempty"`
	ChangeRequests     []string            `json:"changeRequests,omitempty"`
	Outcome            string              `json:"outcome,omitempty"`
	RetentionDays      int                 `json:"retentionDays,omitempty"`
}

// ToolCall 记录一次只读工具调用（审计/回放的最小单元）。
type ToolCall struct {
	Seq           int    `json:"seq"`
	Timestamp     string `json:"timestamp"`
	SessionID     string `json:"sessionId,omitempty"`
	Tool          string `json:"tool"`
	ArgsPreview   string `json:"argsPreview,omitempty"`
	ResultSHA256  string `json:"resultSha256,omitempty"`
	ResultBytes   int    `json:"resultBytes,omitempty"`
	ResultPreview string `json:"resultPreview,omitempty"`
	ErrorClass    string `json:"errorClass,omitempty"`
	RedactedCount int    `json:"redactedCount,omitempty"`
	LatencyMs     int64  `json:"latencyMs,omitempty"`
}

// SanitizationEvent 记录一次脱敏命中（只计数，不含原值）。
type SanitizationEvent struct {
	Timestamp string `json:"timestamp"`
	Count     int    `json:"count"`
	Source    string `json:"source,omitempty"`
}
