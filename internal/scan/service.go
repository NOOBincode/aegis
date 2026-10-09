package scan

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Service 是 scan 上下文的用例编排（铁律 1：service 协作 domain 与 ports）。
// 职责：取数（SnapshotProvider）→ 引擎判定（Engine）→ 产出 Result。
// 不含放行判断（I1），不含 IO 细节（委托 ports）。
type Service struct {
	provider SnapshotProvider
	policies PolicySource
	engine   *Engine

	mu          sync.RWMutex
	lastGood    []PolicyRule // 上一份校验通过的规则集（热修改 fail-safe 回退）
	lastGoodErr error
}

// NewService 装配 service。detectors 为 nil 时用 A 层默认注册表；workers ≤0 用默认并发度。
func NewService(p SnapshotProvider, ps PolicySource, detectors []Detector, workers int, now func() time.Time) *Service {
	return &Service{
		provider: p,
		policies: ps,
		engine:   NewEngine(detectors, workers, now),
	}
}

// Scan 执行一次巡检：采集 → 装载 B 层规则（热修改，fail-safe）→ 引擎判定。
// 规则加载失败不阻断巡检：回退到上一份已知良好规则集，并把告警记入盲点（绝不带病运行）。
//
// 并发安全：Engine 无状态，B 层规则作为参数传入本次 Scan（而非写 Engine 字段）；
// 并发 Scan 各自独立，互不干扰。
func (s *Service) Scan(ctx context.Context, opts CollectOptions) (Result, error) {
	if s.provider == nil {
		return Result{}, fmt.Errorf("scan: SnapshotProvider 未装配")
	}
	snap, err := s.provider.Collect(ctx, opts)
	if err != nil {
		return Result{}, fmt.Errorf("scan: 采集快照失败: %w", err)
	}

	rules, rerr := s.loadPolicies()
	if rerr != nil {
		// fail-safe：规则损坏不当作"无异常"，把告警显式带进结果。
		snap.PartialReads = append(snap.PartialReads, "policy-rules:"+rerr.Error())
	}
	res := s.engine.Scan(snap, rules)
	return res, nil
}

// loadPolicies 从 PolicySource 装载规则并维护 lastGood 回退集（热修改核心）。
func (s *Service) loadPolicies() ([]PolicyRule, error) {
	if s.policies == nil {
		return nil, nil // 无 B 层来源，仅 A 层
	}
	rules, err := s.policies.Load()
	if err == nil {
		if verr := ValidateRuleSet(rules); verr != nil {
			err = verr
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.lastGood = rules
		s.lastGoodErr = nil
		return rules, nil
	}
	// 加载/校验失败：回退上一份良好集；首次失败且无良好集 → 空规则 + 报错。
	s.lastGoodErr = err
	if s.lastGood == nil {
		return nil, fmt.Errorf("规则加载失败且无已知良好集: %w", err)
	}
	return s.lastGood, fmt.Errorf("规则加载失败，回退上一份良好集: %w", err)
}

// LastPolicyError 返回最近一次规则加载错误（供观测/告警）。
func (s *Service) LastPolicyError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastGoodErr
}
