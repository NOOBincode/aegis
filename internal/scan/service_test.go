package scan

import (
	"context"
	"errors"
	"testing"
)

// fakeProvider 注入构造 Snapshot。
type fakeProvider struct {
	snap Snapshot
	err  error
}

func (f fakeProvider) Collect(_ context.Context, _ CollectOptions) (Snapshot, error) {
	return f.snap, f.err
}

// fakePolicySource 可切换 Load 行为（热修改 fail-safe 测试）。
type fakePolicySource struct {
	rules []PolicyRule
	err   error
}

func (f *fakePolicySource) Load() ([]PolicyRule, error) { return f.rules, f.err }

func TestServiceScanCollectError(t *testing.T) {
	svc := NewService(fakeProvider{err: errors.New("apiserver 不可达")}, nil, nil, 0, fixedNow)
	if _, err := svc.Scan(context.Background(), CollectOptions{}); err == nil {
		t.Fatal("采集失败应向上抛错（不产出半份误报）")
	}
}

// TestServicePolicyHotReload 验证热修改 fail-safe：
// 规则更新即生效；坏规则回退上一份良好集并把告警记入盲点；首次失败无良好集则空规则+报错。
func TestServicePolicyHotReload(t *testing.T) {
	snap := Snapshot{Deployments: []DeploymentInfo{
		{Namespace: "s", Name: "d", Containers: []WorkloadContainer{{Name: "c"}}},
	}}
	good := PolicyRule{
		ID: "reliability.no-memory-limit", Match: PolicyMatch{Kind: "Deployment"},
		Assert: PolicyAssert{Path: "spec.template.spec.containers[*].resources.limits.memory", Op: OpExists},
		Level:  SeverityWarning, Message: "no memory limit",
	}
	src := &fakePolicySource{rules: []PolicyRule{good}}
	svc := NewService(fakeProvider{snap: snap}, src, nil, 0, fixedNow)

	// 第一次：良好规则生效，命中 no-limit。
	res, err := svc.Scan(context.Background(), CollectOptions{})
	if err != nil {
		t.Fatalf("首次扫描不应失败：%v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("首次应命中 1，got %+v", res.Findings)
	}

	// 热修改：换成另一条规则（privileged），无需重启即生效。
	other := PolicyRule{
		ID: "security.privileged", Match: PolicyMatch{Kind: "Deployment"},
		Assert: PolicyAssert{Path: "spec.template.spec.containers[*].securityContext.privileged", Op: OpIn, Values: []string{"true"}},
		Level:  SeverityCritical, Message: "privileged container",
	}
	src.rules = []PolicyRule{other}
	src.err = nil
	res2, _ := svc.Scan(context.Background(), CollectOptions{})
	if len(res2.Findings) != 1 || res2.Findings[0].RuleID != "policy/security.privileged" {
		t.Fatalf("热修改未生效：%+v", res2.Findings)
	}

	// 坏规则：加载失败 → 回退上一份良好集（other），并把告警记入盲点。
	src.err = errors.New("yaml: line 3: syntax error")
	res3, _ := svc.Scan(context.Background(), CollectOptions{})
	if len(res3.Findings) != 1 || res3.Findings[0].RuleID != "policy/security.privileged" {
		t.Fatalf("坏规则应回退上一份良好集：%+v", res3.Findings)
	}
	foundBlindSpot := false
	for _, p := range res3.PartialReads {
		if len(p) >= 13 && p[:13] == "policy-rules:" {
			foundBlindSpot = true
		}
	}
	if !foundBlindSpot {
		t.Errorf("规则加载失败应记入盲点：%+v", res3.PartialReads)
	}
	if svc.LastPolicyError() == nil {
		t.Error("LastPolicyError 应记录最近加载错误")
	}

	// 首次失败无良好集 → 空规则集 + 报错。
	src2 := &fakePolicySource{err: errors.New("bad")}
	svc2 := NewService(fakeProvider{snap: snap}, src2, nil, 0, fixedNow)
	res4, _ := svc2.Scan(context.Background(), CollectOptions{})
	if len(res4.Findings) != 0 {
		t.Errorf("首次失败无良好集应为空规则：%+v", res4.Findings)
	}
	if len(res4.PartialReads) == 0 {
		t.Error("首次失败应记入盲点")
	}
}
