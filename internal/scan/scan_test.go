package scan

import (
	"testing"
	"time"
)

var fixedNow = func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }

// ---- Detector（A 层）表驱动：命中 / 不命中 / 边界 ----

func TestDetectors(t *testing.T) {
	oomPod := PodInfo{
		Namespace: "shop", Name: "payments-0", Phase: "Running",
		Containers: []ContainerInfo{{
			Name: "web", RestartCount: 5, Ready: false,
			LastTerminatedReason: "OOMKilled", LastTerminatedCode: 137,
			LastTerminatedAt: fixedNow().Add(-2 * time.Minute), MemoryLimitBytes: 512 * 1024 * 1024,
		}},
	}
	crashPod := PodInfo{
		Namespace: "shop", Name: "api-0", Phase: "Running",
		Containers: []ContainerInfo{{Name: "api", RestartCount: 7, WaitingReason: "CrashLoopBackOff", LastTerminatedCode: 1}},
	}
	pendingPod := PodInfo{
		Namespace: "shop", Name: "job-0", Phase: "Pending",
		ScheduledConditionStatus: "False", ScheduledConditionReason: "Unschedulable",
		ScheduledConditionMessage: "0/3 nodes are available: insufficient memory",
	}
	imagePod := PodInfo{
		Namespace: "shop", Name: "web-0", Phase: "Pending",
		Containers: []ContainerInfo{{Name: "web", WaitingReason: "ImagePullBackOff"}},
	}
	healthyPod := PodInfo{
		Namespace: "shop", Name: "ok-0", Phase: "Running",
		Containers: []ContainerInfo{{Name: "ok", Ready: true}},
	}

	cases := []struct {
		name      string
		detector  Detector
		pod       PodInfo
		wantHit   bool
		wantRule  string
		wantSev   Severity
		wantConf  Confidence
		wantClose bool // NextTier==false（规则结案）
	}{
		{"oom命中", oomKilledDetector{fixedNow}, oomPod, true, "OOM_KILLED", SeverityCritical, ConfidenceHigh, true},
		{"oom不误判健康", oomKilledDetector{fixedNow}, healthyPod, false, "", "", "", false},
		{"crashloop命中", crashLoopDetector{fixedNow}, crashPod, true, "CRASH_LOOP", SeverityCritical, ConfidenceHigh, true},
		{"crashloop不误判oom", crashLoopDetector{fixedNow}, oomPod, false, "", "", "", false},
		{"pending命中", pendingUnschedulableDetector{}, pendingPod, true, "PENDING_UNSCHEDULABLE", SeverityCritical, ConfidenceHigh, true},
		{"pending不误判运行中", pendingUnschedulableDetector{}, healthyPod, false, "", "", "", false},
		{"imagepull命中", imagePullDetector{}, imagePod, true, "IMAGE_PULL_ERROR", SeverityCritical, ConfidenceHigh, true},
		{"imagepull不误判pending调度", imagePullDetector{}, pendingPod, false, "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.detector.Detect(PodFacts{Pod: tc.pod})
			if tc.wantHit {
				if f == nil {
					t.Fatalf("期望命中 %s，实际 nil", tc.wantRule)
				}
				if f.RuleID != tc.wantRule {
					t.Errorf("RuleID=%q, 期望 %q", f.RuleID, tc.wantRule)
				}
				if f.Severity != tc.wantSev {
					t.Errorf("Severity=%q, 期望 %q", f.Severity, tc.wantSev)
				}
				if f.Confidence != tc.wantConf {
					t.Errorf("Confidence=%q, 期望 %q", f.Confidence, tc.wantConf)
				}
				if f.NextTier == tc.wantClose {
					t.Errorf("NextTier=%v, 期望结案(false)", f.NextTier)
				}
				if f.ID == "" {
					t.Errorf("ID 应稳定非空")
				}
			} else if f != nil {
				t.Fatalf("期望不命中，实际命中 %+v", f)
			}
		})
	}
}

// TestRunDeterministic 验证 Run 输出按 ID 确定性排序。
func TestRunDeterministic(t *testing.T) {
	pods := []PodFacts{
		{Pod: PodInfo{Namespace: "b", Name: "p2", Phase: "Running", Containers: []ContainerInfo{{Name: "c", WaitingReason: "CrashLoopBackOff"}}}},
		{Pod: PodInfo{Namespace: "a", Name: "p1", Phase: "Running", Containers: []ContainerInfo{{Name: "c", WaitingReason: "CrashLoopBackOff"}}}},
	}
	out1 := Run(DefaultDetectors(fixedNow), pods, 0)
	out2 := Run(DefaultDetectors(fixedNow), pods, 0)
	if len(out1) != 2 {
		t.Fatalf("期望 2 命中，got %d", len(out1))
	}
	for i := range out1 {
		if out1[i].ID != out2[i].ID {
			t.Fatalf("排序不确定：%q vs %q", out1[i].ID, out2[i].ID)
		}
	}
	if out1[0].ID >= out1[1].ID {
		t.Errorf("应按 ID 升序：%q 应在 %q 前", out1[0].ID, out1[1].ID)
	}
}

// ---- Policy（B 层）校验与求值 ----

func TestPolicyValidate(t *testing.T) {
	base := PolicyRule{
		ID:    "reliability.no-memory-limit",
		Match: PolicyMatch{Kind: "Deployment"},
		Assert: PolicyAssert{
			Path: "spec.template.spec.containers[*].resources.limits.memory",
			Op:   OpExists,
		},
		Level:   SeverityWarning,
		Message: "容器未设 memory limit",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("合法规则应通过：%v", err)
	}

	bad := []struct {
		name   string
		mutate func(*PolicyRule)
	}{
		{"非法id", func(r *PolicyRule) { r.ID = "bad id!" }},
		{"涉密kind-secret", func(r *PolicyRule) { r.Match.Kind = "Secret" }},
		{"涉密kind-configmap", func(r *PolicyRule) { r.Match.Kind = "ConfigMap" }},
		{"未知kind", func(r *PolicyRule) { r.Match.Kind = "Widget" }},
		{"非法op", func(r *PolicyRule) { r.Assert.Op = "eval" }},
		{"未支持path", func(r *PolicyRule) { r.Assert.Path = "spec.template.spec.containers[*].env" }},
		{"op需values", func(r *PolicyRule) { r.Assert.Op = OpIn; r.Assert.Values = nil }},
		{"非法level", func(r *PolicyRule) { r.Level = "fatal" }},
		{"缺message", func(r *PolicyRule) { r.Message = "" }},
	}
	for _, b := range bad {
		t.Run(b.name, func(t *testing.T) {
			r := base
			b.mutate(&r)
			if err := r.Validate(); err == nil {
				t.Fatalf("应拒绝：%+v", r)
			}
		})
	}
}

func TestValidateRuleSetDup(t *testing.T) {
	r := PolicyRule{ID: "x", Match: PolicyMatch{Kind: "Deployment"},
		Assert: PolicyAssert{Path: "spec.template.spec.containers[*].resources.limits.memory", Op: OpExists},
		Level:  SeverityInfo, Message: "m"}
	if err := ValidateRuleSet([]PolicyRule{r, r}); err == nil {
		t.Fatal("重复 id 应报错")
	}
}

func TestEvaluatePolicies(t *testing.T) {
	rule := PolicyRule{
		ID:    "reliability.no-memory-limit",
		Match: PolicyMatch{Kind: "Deployment"},
		Assert: PolicyAssert{
			Path: "spec.template.spec.containers[*].resources.limits.memory",
			Op:   OpExists,
		},
		Level:   SeverityWarning,
		Message: "容器未设 memory limit",
	}
	snap := Snapshot{Deployments: []DeploymentInfo{
		{Namespace: "shop", Name: "no-limit", Containers: []WorkloadContainer{{Name: "web"}}},
		{Namespace: "shop", Name: "has-limit", Containers: []WorkloadContainer{{Name: "web", MemoryLimitBytes: 1 << 30}}},
	}}
	out := EvaluatePolicies([]PolicyRule{rule}, snap, 0)
	if len(out) != 1 {
		t.Fatalf("期望 1 命中（no-limit），got %d: %+v", len(out), out)
	}
	if out[0].Object.Name != "no-limit" {
		t.Errorf("命中对象错误：%q", out[0].Object.Name)
	}
	if out[0].RuleID != "policy/reliability.no-memory-limit" {
		t.Errorf("RuleID 应带 policy/ 前缀：%q", out[0].RuleID)
	}
}

// TestQuantity 验证 K8s quantity 解析（CPU 毫核 / 内存字节）。
func TestQuantity(t *testing.T) {
	cases := map[string]int64{
		"500m": 500, "2": 2, "1Ki": 1024, "512Mi": 512 * 1024 * 1024, "1Gi": 1 << 30, "": 0,
	}
	for in, want := range cases {
		if got := mustQuantity(in); got != want {
			t.Errorf("mustQuantity(%q)=%d, 期望 %d", in, got, want)
		}
	}
}

// ---- Engine：排序 / 结案率 / 盲点透传 ----

func TestEngineScan(t *testing.T) {
	snap := Snapshot{
		PartialReads: []string{"secrets"},
		Pods: []PodInfo{
			{Namespace: "s", Name: "oom", Phase: "Running", Containers: []ContainerInfo{{Name: "c", LastTerminatedReason: "OOMKilled"}}},
			{Namespace: "s", Name: "ok", Phase: "Running", Containers: []ContainerInfo{{Name: "c", Ready: true}}},
		},
		Deployments: []DeploymentInfo{
			{Namespace: "s", Name: "d", Containers: []WorkloadContainer{{Name: "c"}}},
		},
	}
	policy := PolicyRule{
		ID: "reliability.no-memory-limit", Match: PolicyMatch{Kind: "Deployment"},
		Assert: PolicyAssert{Path: "spec.template.spec.containers[*].resources.limits.memory", Op: OpExists},
		Level:  SeverityWarning, Message: "no limit",
	}
	eng := NewEngine(nil, 0, fixedNow)
	res := eng.Scan(snap, []PolicyRule{policy})

	if res.ScannedPods != 2 {
		t.Errorf("ScannedPods=%d, 期望 2", res.ScannedPods)
	}
	if len(res.PartialReads) != 1 || res.PartialReads[0] != "secrets" {
		t.Errorf("盲点应透传：%+v", res.PartialReads)
	}
	// 期望 2 命中：OOM(critical) + policy(warning)；critical 排前。
	if len(res.Findings) != 2 {
		t.Fatalf("期望 2 命中，got %d: %+v", len(res.Findings), res.Findings)
	}
	if res.Findings[0].Severity != SeverityCritical {
		t.Errorf("严重度降序：首位应为 critical，got %q", res.Findings[0].Severity)
	}
	if res.ResolvedByRules != 2 {
		t.Errorf("ResolvedByRules=%d, 期望 2（均规则结案）", res.ResolvedByRules)
	}
	// 消毒：证据不应超长。
	for _, f := range res.Findings {
		for _, e := range f.Evidence {
			if len(e) > evidenceMaxLen+1 {
				t.Errorf("证据未截断：len=%d", len(e))
			}
		}
	}
}
