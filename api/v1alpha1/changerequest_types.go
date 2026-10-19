package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ChangeRequest 是一次写操作的全生命周期载体（设计文档 §5.1，M2 T2.1）。
//
// 安全边界：本对象只表达“意图 + 风险 + 审批 + 逆操作 + 结果”，不直接触发写；
// 执行由 gatekeeper 状态机在 L2 完成（I1）。status.approval 仅由 gatekeeper
// 认证接口写入（R16）；spec 一次性写入（M2 后续 admission 加固）。
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=cr,scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Risk",type=string,JSONPath=`.spec.operations[0].riskLevel`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ChangeRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ChangeRequestSpec   `json:"spec,omitempty"`
	Status            ChangeRequestStatus `json:"status,omitempty"`
}

// ChangeRequestSpec 字段全量对齐 §5.1；第一版即包含 abort/timeout/rollback 契约。
type ChangeRequestSpec struct {
	SessionRef         string            `json:"sessionRef"`
	ClusterRef         string            `json:"clusterRef,omitempty"`
	IdempotencyKey     string            `json:"idempotencyKey"`
	Intent             string            `json:"intent"`
	Source             string            `json:"source"` // agent | human
	Operations         []ChangeOperation `json:"operations"`
	Approval           Approval          `json:"approval,omitempty"`
	GuardrailPolicyRef string            `json:"guardrailPolicyRef,omitempty"`
}

// ChangeOperation 是单个写操作及其安全证据。
type ChangeOperation struct {
	Tool             string           `json:"tool"`
	Args             map[string]any   `json:"args,omitempty"`
	RiskLevel        RiskLevel        `json:"riskLevel"`
	AuthorityCheck   AuthorityCheck   `json:"authorityCheck,omitempty"`
	EstBlastRadius   BlastRadius      `json:"estBlastRadius,omitempty"`
	DryRunResult     DryRunResult     `json:"dryRunResult,omitempty"`
	InverseOperation *ChangeOperation `json:"inverseOperation,omitempty"`
	InverseVerified  bool             `json:"inverseVerified,omitempty"`
	Timeout          metav1.Duration  `json:"timeout,omitempty"`
}

// RiskLevel 是 risk-classifier 的确定性终判（I1：非 LLM 自评）。
type RiskLevel string

// RiskR0 是无影响/纯读风险；R1–R3 由 risk-classifier 终判。
const (
	RiskR0 RiskLevel = "R0"
	RiskR1 RiskLevel = "R1"
	RiskR2 RiskLevel = "R2"
	RiskR3 RiskLevel = "R3"
)

// AuthorityCheck 是 I9 字段所有权核查结果。
type AuthorityCheck struct {
	FieldOwner string `json:"fieldOwner,omitempty"` // 如 hpa/payments；非空需走 owner API 或拒绝
	Decision   string `json:"decision,omitempty"`   // clear | via-owner-api | rejected
}

// BlastRadius 是单操作影响面估算；多操作按 §5.1 累加核算（I7）。
type BlastRadius struct {
	Pods       int `json:"pods,omitempty"`
	Nodes      int `json:"nodes,omitempty"`
	Namespaces int `json:"namespaces,omitempty"`
}

// DryRunResult 是 dry-run 结果。
type DryRunResult string

// DryRunPassed 是 dry-run 通过；failed/skipped 由状态机/闸门决定是否升级。
const (
	DryRunPassed  DryRunResult = "passed"
	DryRunFailed  DryRunResult = "failed"
	DryRunSkipped DryRunResult = "skipped"
)

// Approval 是审批证据；status.approval 由 gatekeeper 认证接口写入（R16）。
type Approval struct {
	Required  bool   `json:"required,omitempty"`
	Approver  string `json:"approver,omitempty"`
	DecidedAt string `json:"decidedAt,omitempty"`
	Channel   string `json:"channel,omitempty"` // cli | webhook
}

// ChangeRequestState 是状态机状态（§5.1）。
type ChangeRequestState string

// StatePending 是 ChangeRequest 初始态；状态迁移由 internal/controller 纯函数裁决。
const (
	StatePending             ChangeRequestState = "Pending"
	StateDryRunning          ChangeRequestState = "DryRunning"
	StateAwaitingApproval    ChangeRequestState = "AwaitingApproval"
	StateExecuting           ChangeRequestState = "Executing"
	StateVerifying           ChangeRequestState = "Verifying"
	StateCommitted           ChangeRequestState = "Committed"
	StateRolledBack          ChangeRequestState = "RolledBack"
	StatePartiallyRolledBack ChangeRequestState = "PartiallyRolledBack"
	StateAborted             ChangeRequestState = "Aborted"
	StateRejected            ChangeRequestState = "Rejected"
)

// Abort 是随时可置位的中止语义。
type Abort struct {
	Requested bool   `json:"requested,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Verification 是执行后验证（verifier）。
type Verification struct {
	Checks  []VerificationCheck `json:"checks,omitempty"`
	Healthy bool                `json:"healthy,omitempty"`
}

// VerificationCheck 是单条验证。
type VerificationCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

// Rollback 是回滚状态与截止时间。
type Rollback struct {
	Deadline  *metav1.Time `json:"deadline,omitempty"`
	Attempted bool         `json:"attempted,omitempty"`
	Result    string       `json:"result,omitempty"`
}

// ChangeRequestStatus 由 controller 独占写入。
type ChangeRequestStatus struct {
	State              ChangeRequestState `json:"state,omitempty"`
	Abort              Abort              `json:"abort,omitempty"`
	ExecutedOps        int                `json:"executedOps,omitempty"`
	Verification       Verification       `json:"verification,omitempty"`
	Rollback           Rollback           `json:"rollback,omitempty"`
	AuditRef           string             `json:"auditRef,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}
