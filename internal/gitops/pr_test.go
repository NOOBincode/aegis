package gitops

import "testing"

func req() Request {
	return Request{
		Repo: "github.com/org/gitops", CRRef: "cr-abc123", Description: "scale payments to 4",
		Patch: []byte("replicas: 4\n"), Diagnosis: "session=s, audit=a", BlastRadius: "pods=4,nodes=0,namespaces=1",
		RiskLevel: "R2", Rollback: "git revert + ArgoCD sync",
	}
}

func TestBuild(t *testing.T) {
	pr, err := Build(req(), []string{"github.com/org/gitops"})
	if err != nil {
		t.Fatal(err)
	}
	if !pr.NoClusterMutation || pr.Branch != "aegis/cr-abc123" || pr.Title == "" {
		t.Fatalf("pr=%+v", pr)
	}
	for _, section := range []string{"诊断链", "影响面", "变更内容", "期望处置级别", "回滚方式"} {
		if !contains([]string{pr.Body}, section) && !stringsContains(pr.Body, section) {
			t.Fatalf("缺段 %s：%s", section, pr.Body)
		}
	}
}

func TestBuildFailClosed(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*Request)
	}{
		{"repo", func(r *Request) { r.Repo = "evil/repo" }},
		{"cr", func(r *Request) { r.CRRef = "" }},
		{"patch-empty", func(r *Request) { r.Patch = nil }},
		{"patch-large", func(r *Request) { r.Patch = make([]byte, maxPatchBytes+1) }},
		{"missing-diagnosis", func(r *Request) { r.Diagnosis = "" }},
		{"missing-rollback", func(r *Request) { r.Rollback = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := req()
			c.mod(&r)
			if _, err := Build(r, []string{"github.com/org/gitops"}); err == nil {
				t.Fatal("应 fail-closed")
			}
		})
	}
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
