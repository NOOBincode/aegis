package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// State 是 ChangeRequest 状态机状态（设计文档 §5.1，M2 T2.1）。
// 状态迁移必须经本包纯函数；reconcile 只做读取/调用/写 status（铁律 3）。
type State string

// StatePending 是 ChangeRequest 初始态，等待进入 dry-run。
const (
	StatePending             State = "Pending"
	StateDryRunning          State = "DryRunning"
	StateAwaitingApproval    State = "AwaitingApproval"
	StateExecuting           State = "Executing"
	StateVerifying           State = "Verifying"
	StateCommitted           State = "Committed"
	StateRolledBack          State = "RolledBack"
	StatePartiallyRolledBack State = "PartiallyRolledBack"
	StateAborted             State = "Aborted"
	StateRejected            State = "Rejected"
)

// Event 是驱动状态机的事件；事件不携带写参数，只表达“发生了”。
type Event string

// EventStartDryRun 启动 dry-run；事件名进入审计日志/DecisionRecord，改名需同步迁移表与测试。
const (
	EventStartDryRun       Event = "StartDryRun"
	EventDryRunPassed      Event = "DryRunPassed"
	EventDryRunFailed      Event = "DryRunFailed"
	EventNeedApproval      Event = "NeedApproval"
	EventApprovalGranted   Event = "ApprovalGranted"
	EventApprovalDenied    Event = "ApprovalDenied"
	EventExecute           Event = "Execute"
	EventExecutionDone     Event = "ExecutionDone"
	EventVerificationPass  Event = "VerificationPassed"
	EventVerificationFail  Event = "VerificationFailed"
	EventRollbackSucceeded Event = "RollbackSucceeded"
	EventRollbackPartial   Event = "RollbackPartial"
	EventAbort             Event = "Abort"
	EventReject            Event = "Reject"
)

// transitions 是唯一事实源；新增迁移必须先改此表 + 单测。
var transitions = map[State]map[Event]State{
	StatePending: {
		EventStartDryRun: StateDryRunning,
		EventReject:      StateRejected,
	},
	StateDryRunning: {
		EventDryRunPassed: StateExecuting,
		EventDryRunFailed: StateRejected,
		EventNeedApproval: StateAwaitingApproval,
	},
	StateAwaitingApproval: {
		EventApprovalGranted: StateExecuting,
		EventApprovalDenied:  StateRejected,
	},
	StateExecuting: {
		EventExecutionDone:     StateVerifying,
		EventRollbackSucceeded: StateRolledBack,
		EventRollbackPartial:   StatePartiallyRolledBack,
	},
	StateVerifying: {
		EventVerificationPass:  StateCommitted,
		EventVerificationFail:  StateRolledBack,
		EventRollbackPartial:   StatePartiallyRolledBack,
		EventRollbackSucceeded: StateRolledBack,
	},
	StatePartiallyRolledBack: {
		EventRollbackSucceeded: StateRolledBack,
		EventReject:            StateRejected,
	},
}

// Transition 返回下一状态；非法迁移返回错误，不落 status。
func Transition(from State, ev Event) (State, error) {
	if err := ValidateState(from); err != nil {
		return from, err
	}
	if ev == EventAbort {
		if IsTerminal(from) {
			return from, fmt.Errorf("状态 %s 已终态，不能 abort", from)
		}
		return StateAborted, nil
	}
	next, ok := transitions[from][ev]
	if !ok {
		return from, fmt.Errorf("非法状态迁移：%s + %s", from, ev)
	}
	return next, nil
}

// ValidateState 校验状态值合法。
func ValidateState(s State) error {
	switch s {
	case StatePending, StateDryRunning, StateAwaitingApproval, StateExecuting,
		StateVerifying, StateCommitted, StateRolledBack, StatePartiallyRolledBack,
		StateAborted, StateRejected:
		return nil
	}
	return fmt.Errorf("非法状态 %q", s)
}

// IsTerminal 返回是否终态；PartiallyRolledBack 需人工介入，不视为可自动终结。
func IsTerminal(s State) bool {
	switch s {
	case StateCommitted, StateRolledBack, StateAborted, StateRejected:
		return true
	}
	return false
}

// ReconcileAction 是 controller 重启/在途恢复时的动作建议（I10/F14）。
type ReconcileAction string

// ActionRequeue 表示 controller 需要重入（Pending/DryRunning）。
const (
	ActionRequeue      ReconcileAction = "requeue"
	ActionWaitApproval ReconcileAction = "wait-approval"
	ActionReconcile    ReconcileAction = "reconcile-actual-state"
	ActionNone         ReconcileAction = "none"
	ActionManual       ReconcileAction = "manual-intervention"
)

// RecoveryAction 决定在途 CR 的恢复动作；controller 只按此执行，不凭状态盲写/盲重试。
func RecoveryAction(s State) (ReconcileAction, error) {
	if err := ValidateState(s); err != nil {
		return "", err
	}
	switch s {
	case StatePending, StateDryRunning:
		return ActionRequeue, nil
	case StateAwaitingApproval:
		return ActionWaitApproval, nil
	case StateExecuting, StateVerifying:
		// 重启后必须核对实际集群状态，不能直接重放操作（I10/F14）。
		return ActionReconcile, nil
	case StatePartiallyRolledBack, StateAborted:
		return ActionManual, nil
	case StateCommitted, StateRolledBack, StateRejected:
		return ActionNone, nil
	}
	return "", fmt.Errorf("未处理状态 %s", s)
}

// CRNameFromKey 由幂等键派生确定性的 CR 名（前缀 + sha256 前 12 hex）。
// 重试折叠：同一 key 永远得到同一对象名，避免第二次执行。
func CRNameFromKey(key string) (string, error) {
	if err := ValidateIdempotencyKey(key); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(key))
	return "cr-" + hex.EncodeToString(sum[:])[:12], nil
}

// ValidateIdempotencyKey 拒绝空/超长/带空白的 key；不做业务意图校验（那个在 risk/policy）。
func ValidateIdempotencyKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("idempotencyKey 不能为空")
	}
	if len(key) > 128 {
		return fmt.Errorf("idempotencyKey 超过 128 字节")
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return fmt.Errorf("idempotencyKey 不能包含空白")
	}
	return nil
}
