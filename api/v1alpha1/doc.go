// Package v1alpha1 定义 aegis CRD 的 Go 类型（K8s 契约层，kubebuilder 惯例）。
//
// 里程碑填充顺序：DecisionRecord（M1 T1.8）→ ChangeRequest/GuardrailPolicy（M2 T2.1）
// → SchedulingHint（M4 决策门通过后）。
// 设计出处：设计文档 §5（数据契约与 Schema）。
package v1alpha1
