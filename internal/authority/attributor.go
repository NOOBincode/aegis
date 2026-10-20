package authority

import (
	"context"
	"fmt"
	"time"
)

// EvidenceSource 是归因证据端口（K8s Events/HPA status/audit/指标由 adapter 实现）。
// 领域不 import k8s；证据缺失时返回空列表，由 Attribute 降为 partial/unattributed。
type EvidenceSource interface {
	Events(ctx context.Context, ref FieldRef, since, until time.Time) ([]Evidence, error)
}

// Attributor 是归因用例编排：图谱 owner + 证据源 + 时间窗 → Attribution。
type Attributor struct {
	Graph    *Graph
	Evidence EvidenceSource
	Window   time.Duration
}

// Attribute 执行一次归因；证据源错误不硬归因，降为 partial（缺失证据）并返回错误供审计。
func (a *Attributor) Attribute(ctx context.Context, ch Change) (Attribution, error) {
	if a.Graph == nil {
		return Attribution{Verdict: VerdictUnattributed, Reasons: []string{"authority graph 未就绪，fail-closed 残差"}}, nil
	}
	owners := a.Graph.Owners(ch.Field)
	if len(owners) == 0 {
		return Attribute(ch, nil, nil, a.Window), nil
	}
	if a.Evidence == nil {
		return Attribution{Verdict: VerdictPartial, Reasons: []string{"无证据源接线，不能硬归因"}}, nil
	}
	window := a.Window
	if window <= 0 {
		window = 2 * time.Minute
	}
	since := ch.ObservedAt.Add(-window)
	until := ch.ObservedAt.Add(window)
	evs, err := a.Evidence.Events(ctx, ch.Field, since, until)
	if err != nil {
		return Attribution{Verdict: VerdictPartial, Reasons: []string{fmt.Sprintf("证据源不可用，降级 partial：%v", err)}}, fmt.Errorf("evidence: %w", err)
	}
	return Attribute(ch, owners, evs, window), nil
}
