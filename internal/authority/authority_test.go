package authority

import (
	"testing"
	"time"
)

func field() FieldRef {
	return FieldRef{Kind: "Deployment", Namespace: "shop", Name: "payments", Field: "spec.replicas"}
}

func TestGraphCheckConservative(t *testing.T) {
	g := NewGraph()
	d, owners, err := g.Check(field())
	if err != nil || d != DecisionViaOwnerAPI || len(owners) != 0 {
		t.Fatalf("无证据应保守 via-owner-api：d=%s owners=%v err=%v", d, owners, err)
	}
	g.MarkClear(field())
	if d, _, _ := g.Check(field()); d != DecisionClear {
		t.Fatalf("显式 clear 应放行：%s", d)
	}
	g.AddOwner(field(), Owner{Kind: OwnerHPA, Ref: "hpa/payments", Source: "managedFields"})
	d, owners, _ = g.Check(field())
	if d != DecisionViaOwnerAPI || len(owners) != 1 || owners[0].Kind != OwnerHPA {
		t.Fatalf("HPA owner：d=%s owners=%+v", d, owners)
	}
}

func TestTranslate(t *testing.T) {
	if tr := Translate(IntentScale, []Owner{{Kind: OwnerHPA}}); tr.Reject || tr.Action != "patch_hpa_minReplicas" {
		t.Fatalf("HPA scale: %+v", tr)
	}
	if tr := Translate(IntentResources, []Owner{{Kind: OwnerVPA}}); tr.Reject || tr.Action != "patch_vpa_policy" {
		t.Fatalf("VPA resources: %+v", tr)
	}
	if tr := Translate(IntentScale, []Owner{{Kind: OwnerArgoCD}}); tr.Reject || tr.Action != "open_gitops_pr" {
		t.Fatalf("argo: %+v", tr)
	}
	if tr := Translate(IntentGeneric, []Owner{{Kind: OwnerUnknown}}); !tr.Reject {
		t.Fatalf("未知 owner 应拒绝：%+v", tr)
	}
	if tr := Translate(IntentScale, nil); !tr.Reject {
		t.Fatalf("无 owner 应 fail-closed 拒绝直写：%+v", tr)
	}
}

func TestAttribute(t *testing.T) {
	now := time.Now()
	ch := Change{Field: field(), ObservedAt: now, Direction: "increase", Magnitude: 4}
	owners := []Owner{{Kind: OwnerHPA}}
	attr := Attribute(ch, owners, []Evidence{{Controller: "hpa", At: now.Add(-10 * time.Second), Direction: "increase", Magnitude: 4}}, time.Minute)
	if attr.Verdict != VerdictAttributed || attr.Controller != "hpa" {
		t.Fatalf("应归因：%+v", attr)
	}
	partial := Attribute(ch, owners, []Evidence{{Controller: "hpa", At: now, Direction: "increase", Magnitude: 40}}, time.Minute)
	if partial.Verdict != VerdictPartial {
		t.Fatalf("幅度不符应 partial：%+v", partial)
	}
	un := Attribute(ch, owners, []Evidence{{Controller: "hpa", At: now.Add(-time.Hour), Direction: "increase"}}, time.Minute)
	if un.Verdict != VerdictUnattributed {
		t.Fatalf("窗口外应 unattributed：%+v", un)
	}
	none := Attribute(ch, nil, nil, time.Minute)
	if none.Verdict != VerdictUnattributed {
		t.Fatalf("无 owner 应 unattributed：%+v", none)
	}
}
