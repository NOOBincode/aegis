package scan

import "testing"

// 非 Deployment 的规则必须加载期拒绝，而不是"校验通过但永不评估"。
func TestPolicyRejectsUnsupportedKind(t *testing.T) {
	for _, kind := range []string{"StatefulSet", "DaemonSet", "Job", "CronJob"} {
		t.Run(kind, func(t *testing.T) {
			r := PolicyRule{
				ID:      "test." + kind,
				Match:   PolicyMatch{Kind: kind},
				Assert:  PolicyAssert{Path: "spec.template.spec.containers[*].resources.limits.memory", Op: OpExists},
				Level:   SeverityWarning,
				Message: "x",
			}
			if err := r.Validate(); err == nil {
				t.Fatalf("%s 规则应被拒绝（当前 Snapshot 不采集该 kind）", kind)
			}
		})
	}
}
