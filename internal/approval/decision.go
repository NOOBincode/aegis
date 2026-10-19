// Package approval 实现审批状态机的纯领域部分（M2 T2.3/T2.4）。
//
// 安全边界：审批状态在服务端，CLI 只提交意图；`--yes`/自写客户端不能绕过
// Decide 的校验。审批人身份由调用方（gatekeeper 认证接口）注入，本包不读
// kubeconfig/HTTP；R16 要求绑定身份与来源。
package approval

import (
	"errors"
	"fmt"
	"time"
)

// State 是审批状态。
type State string

// StatePending 是审批初始态；Approved/Denied 为终态，决定后不可再改。
const (
	StatePending  State = "Pending"
	StateApproved State = "Approved"
	StateDenied   State = "Denied"
)

// Request 是一次审批请求。
type Request struct {
	ID        string
	Requester string
	Required  bool
	State     State
	Approver  string
	DecidedAt time.Time
	Note      string
}

// ErrNotRequired 表示该变更不需要审批；调用方不应把“无需审批”当成“已批准”。
var ErrNotRequired = errors.New("approval not required")

// ErrAlreadyDecided 表示审批已终态。
var ErrAlreadyDecided = errors.New("approval already decided")

// ErrUnauthenticated 表示审批人身份为空。
var ErrUnauthenticated = errors.New("approver identity missing")

// ErrSelfApproval 表示请求人自批（单人环境下的 R16 退化防线）。
var ErrSelfApproval = errors.New("self approval rejected")

// Decide 处理一次 approve/deny。成功返回更新后的 Request；失败不改状态。
func Decide(req Request, actor string, approve bool, now time.Time) (Request, error) {
	if !req.Required {
		return req, ErrNotRequired
	}
	if req.State != StatePending {
		return req, ErrAlreadyDecided
	}
	if actor == "" {
		return req, ErrUnauthenticated
	}
	if req.Requester != "" && actor == req.Requester {
		return req, ErrSelfApproval
	}
	out := req
	out.Approver = actor
	out.DecidedAt = now.UTC()
	if approve {
		out.State = StateApproved
	} else {
		out.State = StateDenied
	}
	return out, nil
}

// Validate 校验状态值合法（反序列化/恢复时用）。
func Validate(s State) error {
	switch s {
	case StatePending, StateApproved, StateDenied:
		return nil
	}
	return fmt.Errorf("非法审批状态 %q", s)
}
