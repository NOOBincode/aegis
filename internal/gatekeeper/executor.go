package gatekeeper

import (
	"context"
	"errors"
	"fmt"

	"github.com/aegis-dev/aegis/internal/rollback"
)

// Executor 是真实写执行端口；实现由 adapters/k8s-write 提供。
// 本包不直接持有 kubeconfig/写客户端；Service 只按 Decision 决定是否调用。
type Executor interface {
	Execute(ctx context.Context, op rollback.Operation) error
	Verify(ctx context.Context, op rollback.Operation) error
	Rollback(ctx context.Context, inverse rollback.Operation) error
}

// Outcome 是一次 Apply 的结果；Err 非空时记录失败阶段。
type Outcome struct {
	Decision   Decision
	Executed   int
	RolledBack bool
	Err        error
}

// Service 把 gatekeeper.Decision 接到 Executor：只有 Allow 才执行；失败逆序回滚。
// 审批未通过的请求不会触达 Executor（R2 无审不执行）。
type Service struct {
	Executor Executor
}

// Apply 执行一次已裁决变更。执行/验证/回滚失败都返回 Outcome.Err，且不回退为放行。
func (s *Service) Apply(ctx context.Context, in Input) Outcome {
	d := Evaluate(in)
	out := Outcome{Decision: d}
	if !d.Allow || s.Executor == nil {
		if s.Executor == nil && d.Allow {
			out.Err = errors.New("executor 未接线，fail-closed 不执行")
			out.Decision.Allow = false
		}
		return out
	}
	executed := make([]int, 0, len(in.Operations))
	for i, op := range in.Operations {
		if err := s.Executor.Execute(ctx, op); err != nil {
			out.Err = fmt.Errorf("execute op[%d] %s: %w", i, op.Tool, err)
			out.Executed = len(executed)
			s.rollback(ctx, &out, in.Operations, executed)
			return out
		}
		executed = append(executed, i)
		if err := s.Executor.Verify(ctx, op); err != nil {
			out.Err = fmt.Errorf("verify op[%d] %s: %w", i, op.Tool, err)
			out.Executed = len(executed)
			s.rollback(ctx, &out, in.Operations, executed)
			return out
		}
	}
	out.Executed = len(executed)
	return out
}

func (s *Service) rollback(_ context.Context, out *Outcome, ops []rollback.Operation, executed []int) {
	out.RolledBack = true
	for i := len(executed) - 1; i >= 0; i-- {
		op := ops[executed[i]]
		inv := rollback.Generate(op)
		if !inv.Reversible || inv.Inverse == nil {
			out.RolledBack = false
			if out.Err != nil {
				out.Err = fmt.Errorf("%w; op[%d] 无可执行逆操作", out.Err, executed[i])
			}
			continue
		}
		if err := s.Executor.Rollback(context.Background(), *inv.Inverse); err != nil {
			out.RolledBack = false
			if out.Err != nil {
				out.Err = fmt.Errorf("%w; rollback op[%d] 失败: %v", out.Err, executed[i], err)
			}
		}
	}
}
