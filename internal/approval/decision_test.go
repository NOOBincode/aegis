package approval

import (
	"errors"
	"testing"
	"time"
)

func req() Request {
	return Request{ID: "cr-1", Requester: "alice", Required: true, State: StatePending}
}

func TestApproveDeny(t *testing.T) {
	now := time.Unix(100, 0)
	got, err := Decide(req(), "bob", true, now)
	if err != nil || got.State != StateApproved || got.Approver != "bob" || got.DecidedAt.IsZero() {
		t.Fatalf("approve: %+v err=%v", got, err)
	}
	denied, err := Decide(req(), "bob", false, now)
	if err != nil || denied.State != StateDenied {
		t.Fatalf("deny: %+v err=%v", denied, err)
	}
}

func TestDecideGuards(t *testing.T) {
	now := time.Now()
	if _, err := Decide(Request{Required: false}, "bob", true, now); !errors.Is(err, ErrNotRequired) {
		t.Fatalf("not required: %v", err)
	}
	already := req()
	already.State = StateApproved
	if _, err := Decide(already, "bob", true, now); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("already: %v", err)
	}
	if _, err := Decide(req(), "", true, now); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unauth: %v", err)
	}
	if _, err := Decide(req(), "alice", true, now); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("self: %v", err)
	}
}
