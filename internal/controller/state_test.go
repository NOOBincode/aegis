package controller

import "testing"

func TestTransitionTable(t *testing.T) {
	cases := []struct {
		from State
		ev   Event
		want State
	}{
		{StatePending, EventStartDryRun, StateDryRunning},
		{StateDryRunning, EventDryRunPassed, StateExecuting},
		{StateDryRunning, EventDryRunFailed, StateRejected},
		{StateDryRunning, EventNeedApproval, StateAwaitingApproval},
		{StateAwaitingApproval, EventApprovalGranted, StateExecuting},
		{StateAwaitingApproval, EventApprovalDenied, StateRejected},
		{StateExecuting, EventExecutionDone, StateVerifying},
		{StateExecuting, EventRollbackSucceeded, StateRolledBack},
		{StateVerifying, EventVerificationPass, StateCommitted},
		{StateVerifying, EventVerificationFail, StateRolledBack},
		{StateVerifying, EventRollbackPartial, StatePartiallyRolledBack},
		{StatePartiallyRolledBack, EventRollbackSucceeded, StateRolledBack},
	}
	for _, c := range cases {
		got, err := Transition(c.from, c.ev)
		if err != nil || got != c.want {
			t.Fatalf("%s+%s: got=%s err=%v want=%s", c.from, c.ev, got, err, c.want)
		}
	}
}

func TestIllegalTransition(t *testing.T) {
	if _, err := Transition(StatePending, EventVerificationPass); err == nil {
		t.Fatal("Pending+VerificationPassed 应非法")
	}
	if _, err := Transition(StateCommitted, EventAbort); err == nil {
		t.Fatal("终态 abort 应非法")
	}
	if _, err := Transition("Bogus", EventAbort); err == nil {
		t.Fatal("非法状态应拒绝")
	}
}

func TestAbortFromNonTerminal(t *testing.T) {
	for _, s := range []State{StatePending, StateDryRunning, StateAwaitingApproval, StateExecuting, StateVerifying, StatePartiallyRolledBack} {
		got, err := Transition(s, EventAbort)
		if err != nil || got != StateAborted {
			t.Fatalf("%s abort: got=%s err=%v", s, got, err)
		}
	}
}

func TestRecoveryAction(t *testing.T) {
	cases := map[State]ReconcileAction{
		StatePending: ActionRequeue, StateDryRunning: ActionRequeue,
		StateAwaitingApproval: ActionWaitApproval,
		StateExecuting:        ActionReconcile, StateVerifying: ActionReconcile,
		StatePartiallyRolledBack: ActionManual, StateAborted: ActionManual,
		StateCommitted: ActionNone, StateRolledBack: ActionNone, StateRejected: ActionNone,
	}
	for s, want := range cases {
		got, err := RecoveryAction(s)
		if err != nil || got != want {
			t.Fatalf("%s: got=%s err=%v want=%s", s, got, err, want)
		}
	}
}

func TestCRNameFromKeyIdempotent(t *testing.T) {
	a, err := CRNameFromKey("intent-abc")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := CRNameFromKey("intent-abc")
	if a != b || len(a) <= 3 {
		t.Fatalf("a=%q b=%q", a, b)
	}
	if _, err := CRNameFromKey("bad key"); err == nil {
		t.Fatal("带空白 key 应拒绝")
	}
}
