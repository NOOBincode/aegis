package scan

import (
	"strings"
	"testing"
)

// TestNewDetectors 覆盖补齐的 4 类 A 层 detector（P1）：探针失败/重启环/启动错误/配置错误。
func TestNewDetectors(t *testing.T) {
	probePod := PodInfo{Namespace: "s", Name: "probe-0", Phase: "Running", Containers: []ContainerInfo{{Name: "c", Ready: false}}}
	probeEvents := []EventInfo{{InvolvedKind: "Pod", InvolvedName: "probe-0", Namespace: "s", Reason: "Unhealthy", Message: "Readiness probe failed: timeout"}}
	restartPod := PodInfo{Namespace: "s", Name: "flap-0", Phase: "Running", Containers: []ContainerInfo{{Name: "c", RestartCount: 5, Ready: true, LastTerminatedCode: 1}}}
	startErrPod := PodInfo{Namespace: "s", Name: "start-0", Phase: "Pending", Containers: []ContainerInfo{{Name: "c", WaitingReason: "RunContainerError"}}}
	configErrPod := PodInfo{Namespace: "s", Name: "cfg-0", Phase: "Pending", Containers: []ContainerInfo{{Name: "c", WaitingReason: "CreateContainerConfigError"}}}

	cases := []struct {
		name     string
		detector Detector
		facts    PodFacts
		wantHit  bool
		wantRule string
		wantConf Confidence
	}{
		{"探针失败命中", probeFailingDetector{}, PodFacts{Pod: probePod, Events: probeEvents}, true, "PROBE_FAILING", ConfidenceMedium},
		{"探针失败无事件不命中", probeFailingDetector{}, PodFacts{Pod: probePod}, false, "", ""},
		{"探针失败但容器Ready不命中", probeFailingDetector{}, PodFacts{Pod: PodInfo{Namespace: "s", Name: "ok", Phase: "Running", Containers: []ContainerInfo{{Name: "c", Ready: true}}}, Events: probeEvents}, false, "", ""},
		{"重启环命中", restartLoopDetector{threshold: 3}, PodFacts{Pod: restartPod}, true, "RESTART_LOOP", ConfidenceMedium},
		{"重启环低于阈值不命中", restartLoopDetector{threshold: 3}, PodFacts{Pod: PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{{Name: "c", RestartCount: 2}}}}, false, "", ""},
		{"重启环OOM不误报", restartLoopDetector{threshold: 3}, PodFacts{Pod: PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{{Name: "c", RestartCount: 5, LastTerminatedReason: "OOMKilled"}}}}, false, "", ""},
		{"启动错误命中", containerStartErrorDetector{}, PodFacts{Pod: startErrPod}, true, "CONTAINER_START_ERROR", ConfidenceMedium},
		{"配置错误命中", configErrorDetector{}, PodFacts{Pod: configErrPod}, true, "CONFIG_ERROR", ConfidenceHigh},
		{"配置错误不误判其它waiting", configErrorDetector{}, PodFacts{Pod: PodInfo{Namespace: "s", Name: "p", Phase: "Pending", Containers: []ContainerInfo{{Name: "c", WaitingReason: "ImagePullBackOff"}}}}, false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.detector.Detect(tc.facts)
			if tc.wantHit {
				if f == nil {
					t.Fatalf("期望命中 %s", tc.wantRule)
				}
				if f.RuleID != tc.wantRule {
					t.Errorf("RuleID=%q 期望 %q", f.RuleID, tc.wantRule)
				}
				if f.Confidence != tc.wantConf {
					t.Errorf("Confidence=%q 期望 %q", f.Confidence, tc.wantConf)
				}
			} else if f != nil {
				t.Fatalf("期望不命中，实际 %+v", f)
			}
		})
	}
}

// TestCrashLoopOOMMutualExclusion 验证 OOM/CrashLoop 互斥：OOMKilled 容器不重复报 CRASH_LOOP。
func TestCrashLoopOOMMutualExclusion(t *testing.T) {
	pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{{
		Name: "c", WaitingReason: "CrashLoopBackOff", RestartCount: 6, LastTerminatedReason: "OOMKilled", LastTerminatedCode: 137,
	}}}
	// OOM 命中
	if f := (oomKilledDetector{fixedNow}).Detect(PodFacts{Pod: pod}); f == nil {
		t.Fatal("OOM 应命中")
	}
	// CrashLoop 跳过（互斥）
	if f := (crashLoopDetector{fixedNow}).Detect(PodFacts{Pod: pod}); f != nil {
		t.Errorf("OOMKilled 容器不应再报 CRASH_LOOP：%+v", f)
	}
	// RestartLoop 也跳过（OOM 根因）
	if f := (restartLoopDetector{threshold: 3}).Detect(PodFacts{Pod: pod}); f != nil {
		t.Errorf("OOMKilled 容器不应再报 RESTART_LOOP：%+v", f)
	}
}

// TestQuantityDecimal 验证 quantity 小数解析（P3.8）。
func TestQuantityDecimal(t *testing.T) {
	cases := map[string]int64{
		"500m": 500, "2": 2, "0.5": 0, "1.5Gi": 1610612736, "512Mi": 536870912, "2.5": 2, "abc": 0,
	}
	for in, want := range cases {
		if got := mustQuantity(in); got != want {
			t.Errorf("mustQuantity(%q)=%d 期望 %d", in, got, want)
		}
	}
	// 可解析校验。
	if !isValidQuantity("1.5Gi") || !isValidQuantity("500m") || !isValidQuantity("2") {
		t.Error("合法 quantity 应通过校验")
	}
	if isValidQuantity("abc") || isValidQuantity("") || isValidQuantity("Mi") {
		t.Error("非法 quantity 应被拦截")
	}
}

// TestAssertViolatedEmptyValues 验证空 values 不 panic（P2.4 防御），且视为不违反。
func TestAssertViolatedEmptyValues(t *testing.T) {
	c := WorkloadContainer{Name: "c", MemoryLimitBytes: 1 << 30}
	for _, op := range []string{OpMatches, OpNotMatches, OpGt, OpGte, OpLt, OpLte} {
		a := PolicyAssert{Path: "spec.template.spec.containers[*].resources.limits.memory", Op: op, Values: nil}
		if assertViolated(a, c) {
			t.Errorf("op=%q 空 values 应视为不违反（防御未校验规则）", op)
		}
	}
}

// TestWriteSARIFDeterministic 验证 SARIF 输出字节级确定（P2.2：rules 数组排序）。
func TestWriteSARIFDeterministic(t *testing.T) {
	r := sampleResult()
	// 加多条 finding 触发多 rule，放大 map 遍历乱序概率。
	r.Findings = append(r.Findings,
		Finding{ID: "A/Pod/s/p1", RuleID: "A", Severity: SeverityCritical, Object: ObjRef{Kind: "Pod", Namespace: "s", Name: "p1"}},
		Finding{ID: "B/Pod/s/p2", RuleID: "B", Severity: SeverityWarning, Object: ObjRef{Kind: "Pod", Namespace: "s", Name: "p2"}},
		Finding{ID: "C/Pod/s/p3", RuleID: "C", Severity: SeverityInfo, Object: ObjRef{Kind: "Pod", Namespace: "s", Name: "p3"}},
	)
	var b1, b2 strings.Builder
	if err := WriteSARIF(&b1, r); err != nil {
		t.Fatal(err)
	}
	if err := WriteSARIF(&b2, r); err != nil {
		t.Fatal(err)
	}
	if b1.String() != b2.String() {
		t.Fatal("SARIF 输出应字节级确定（rules 需排序）")
	}
}
