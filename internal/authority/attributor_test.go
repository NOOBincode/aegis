package authority

import (
	"context"
	"testing"
	"time"
)

type fakeEvidence struct {
	evs []Evidence
	err error
}

func (f fakeEvidence) Events(context.Context, FieldRef, time.Time, time.Time) ([]Evidence, error) {
	return f.evs, f.err
}

func TestAttributorWithEvidence(t *testing.T) {
	g := NewGraph()
	f := field()
	g.AddOwner(f, Owner{Kind: OwnerHPA, Ref: "hpa/payments"})
	now := time.Now()
	a := &Attributor{Graph: g, Evidence: fakeEvidence{evs: []Evidence{{Controller: "hpa", At: now, Direction: "increase", Magnitude: 2}}}, Window: time.Minute}
	got, err := a.Attribute(context.Background(), Change{Field: f, ObservedAt: now, Direction: "increase", Magnitude: 2})
	if err != nil || got.Verdict != VerdictAttributed {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestAttributorEvidenceErrorIsPartial(t *testing.T) {
	g := NewGraph()
	f := field()
	g.AddOwner(f, Owner{Kind: OwnerHPA})
	a := &Attributor{Graph: g, Evidence: fakeEvidence{err: context.DeadlineExceeded}, Window: time.Minute}
	got, err := a.Attribute(context.Background(), Change{Field: f, ObservedAt: time.Now(), Direction: "increase", Magnitude: 1})
	if err == nil || got.Verdict != VerdictPartial {
		t.Fatalf("证据源错误应 partial 且返回 err：got=%+v err=%v", got, err)
	}
}

func TestAttributorNoEvidenceSource(t *testing.T) {
	g := NewGraph()
	f := field()
	g.AddOwner(f, Owner{Kind: OwnerHPA})
	a := &Attributor{Graph: g, Window: time.Minute}
	got, err := a.Attribute(context.Background(), Change{Field: f, ObservedAt: time.Now()})
	if err != nil || got.Verdict != VerdictPartial {
		t.Fatalf("无证据源应 partial：%+v", got)
	}
}
