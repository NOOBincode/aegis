// Package mcpserver 是 aegis L1 工具层共享框架（internal/mcpserver/README.md）。
//
// T1.1 落地范围：工具注册流程 + 注册表 + 元数据五字段注册期强校验 + 统一中间件链骨架 +
// 结构化结果与错误协议。放行/分级判断永不进本框架（I1/I3：放行只在 L2 闸门）。
package mcpserver

import "fmt"

// RiskHint 是工具的风险初判（R0–R3）。终判由 L2 闸门取查表与 LLM 初判中更高者（设计文档 §4.2.2，I1）。
type RiskHint string

const (
	// RiskR0 只读、无副作用（如 get/list/describe）。
	RiskR0 RiskHint = "R0"
	// RiskR1 低风险写，可直写（逆操作 + 配额兜底，ADR-001）。
	RiskR1 RiskHint = "R1"
	// RiskR2 高风险写，强制 GitOps PR 通道（ADR-001）。
	RiskR2 RiskHint = "R2"
	// RiskR3 硬拒操作，代码级 hardDeny，无审批入口。
	RiskR3 RiskHint = "R3"
)

// validRiskHints 注册期校验用的合法集合。
var validRiskHints = map[RiskHint]bool{RiskR0: true, RiskR1: true, RiskR2: true, RiskR3: true}

// BlastRadius 估算单次工具调用的影响面（I7 爆炸半径配额的工具侧输入）。
type BlastRadius struct {
	Pods       int `json:"pods,omitempty"`
	Nodes      int `json:"nodes,omitempty"`
	Namespaces int `json:"namespaces,omitempty"`
}

// ToolMetadata 是工具注册的强制元数据五字段（T1.1 完成判据：缺一注册失败）。
// 这五字段是闸门分级复核与配额核算的确定性输入，缺一则工具不可被安全分级，故注册期强制。
type ToolMetadata struct {
	// RiskHint 风险初判（R0–R3）。
	RiskHint RiskHint `json:"risk_hint"`
	// Idempotent 是否幂等（重复调用结果一致），决定重试/折叠语义（F15）。
	Idempotent bool `json:"idempotent"`
	// Reversible 是否可生成逆操作（I5）；false 的写操作自动升 R2。
	Reversible bool `json:"reversible"`
	// EstBlastRadius 估算爆炸半径（pods/nodes/namespaces）。
	EstBlastRadius BlastRadius `json:"est_blast_radius"`
	// TimeoutMs 执行超时上界（毫秒）；超时即失败返回，绝不无限挂起（I10）。
	TimeoutMs int `json:"timeout_ms"`
}

// Validate 注册期强校验：任一字段缺失或非法即返回带缺失项的错误（T1.1 判据）。
func (m ToolMetadata) Validate() error {
	if !validRiskHints[m.RiskHint] {
		return fmt.Errorf("metadata.risk_hint 缺失或非法（须 R0|R1|R2|R3），got %q", m.RiskHint)
	}
	// Idempotent / Reversible 为 bool，零值 false 合法，无需校验存在性。
	if m.EstBlastRadius.Pods < 0 || m.EstBlastRadius.Nodes < 0 || m.EstBlastRadius.Namespaces < 0 {
		return fmt.Errorf("metadata.est_blast_radius 不能为负：%+v", m.EstBlastRadius)
	}
	if m.TimeoutMs <= 0 {
		return fmt.Errorf("metadata.timeout_ms 必须为正整数（执行超时上界），got %d", m.TimeoutMs)
	}
	return nil
}
