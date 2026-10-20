package gatekeeper

import (
	"context"
	"errors"
	"testing"

	"github.com/aegis-dev/aegis/internal/rollback"
)

type fakeExec struct {
	execErr   map[string]error
	verifyErr map[string]error
	rollback  []string
}

func (f *fakeExec) Execute(_ context.Context, op rollback.Operation) error {
	if f.execErr != nil {
		if err, ok := f.execErr[op.Tool]; ok {
			return err
		}
	}
	return nil
}
func (f *fakeExec) Verify(_ context.Context, op rollback.Operation) error {
	if f.verifyErr != nil {
		if err, ok := f.verifyErr[op.Tool]; ok {
			return err
		}
	}
	return nil
}
func (f *fakeExec) Rollback(_ context.Context, op rollback.Operation) error {
	f.rollback = append(f.rollback, op.Tool)
	return nil
}

func TestServiceExecutesAllowed(t *testing.T) {
	f := &fakeExec{}
	out := (&Service{Executor: f}).Apply(context.Background(), scaleInput(1))
	if out.Err != nil || out.Executed != 1 || !out.Decision.Allow {
		t.Fatalf("out=%+v", out)
	}
}

func TestServiceNoApprovalNoExecution(t *testing.T) {
	in := scaleInput(3) // R2 pending
	f := &fakeExec{}
	out := (&Service{Executor: f}).Apply(context.Background(), in)
	if out.Err != nil || out.Executed != 0 || out.Decision.Allow {
		t.Fatalf("R2 未审批不应执行：%+v", out)
	}
}

func TestServiceRollbackOnVerifyFailure(t *testing.T) {
	f := &fakeExec{verifyErr: map[string]error{"scale_deployment": errors.New("unhealthy")}}
	in := scaleInput(1)
	out := (&Service{Executor: f}).Apply(context.Background(), in)
	if out.Err == nil || out.Executed != 1 || !out.RolledBack {
		t.Fatalf("验证失败应回滚：%+v", out)
	}
	if len(f.rollback) != 1 || f.rollback[0] != "scale_deployment" {
		t.Fatalf("rollback=%v", f.rollback)
	}
}
