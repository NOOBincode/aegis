package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ApprovalDecision 是一次不可变审批决定；ChangeRequest status 仅由 controller 回写。
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=ad,scope=Namespaced
// +kubebuilder:printcolumn:name="Decision",type=string,JSONPath=`.spec.decision`
// +kubebuilder:printcolumn:name="Approver",type=string,JSONPath=`.spec.approver`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ApprovalDecision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ApprovalDecisionSpec `json:"spec"`
}

// ApprovalDecisionSpec 绑定一个 ChangeRequest 的唯一审批结论。
type ApprovalDecisionSpec struct {
	ChangeRequestRef ApprovalChangeRequestRef `json:"changeRequestRef"`
	Approver         string                   `json:"approver"`
	Decision         string                   `json:"decision"` // Approved | Denied
	Channel          string                   `json:"channel"`  // cli | webhook
	DecidedAt        string                   `json:"decidedAt"`
	EvidenceHash     string                   `json:"evidenceHash"`
	Note             string                   `json:"note,omitempty"`
}

// ApprovalChangeRequestRef 防止删除重建的同名 ChangeRequest 误用旧决定。
type ApprovalChangeRequestRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// ApprovalDecisionList 是 ApprovalDecision 的列表契约。
//
// +kubebuilder:object:root=true
type ApprovalDecisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApprovalDecision `json:"items"`
}
