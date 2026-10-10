package scan

import (
	"strings"
	"testing"
)

// 边缘情况测试：detector 边界 / policy 匹配 / 引擎空态 / pmap 边界 / 脱敏截断 / 报告空态。
// 聚焦易错且影响正确性、安全（不误报/不漏报/不 panic）的路径。

// ---- Detector 边缘 ----

func TestOOMEdgeCases(t *testing.T) {
	d := oomKilledDetector{fixedNow}

	t.Run("当前态terminated-OOMKilled", func(t *testing.T) {
		// OOMKilled 出现在当前 State.Terminated（非 LastTerminationState）。
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{{
			Name: "c", TerminatedReason: "OOMKilled", TerminatedCode: 137,
		}}}
		f := d.Detect(PodFacts{Pod: pod})
		if f == nil || f.Confidence != ConfidenceHigh {
			t.Errorf("当前态 OOMKilled 应高置信命中：%+v", f)
		}
	})

	t.Run("137仅当前态terminated", func(t *testing.T) {
		// 仅 TerminatedCode=137（无 LastTerminated，无 Reason）→ 中置信推断。
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{{
			Name: "c", TerminatedCode: 137, MemoryLimitBytes: 1 << 28,
		}}}
		f := d.Detect(PodFacts{Pod: pod})
		if f == nil || f.Confidence != ConfidenceMedium {
			t.Errorf("当前态 137 应中置信命中：%+v", f)
		}
	})

	t.Run("137无任何佐证走default分支", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{{
			Name: "c", LastTerminatedCode: 137, // 无 limit、无节点压力
		}}}
		f := d.Detect(PodFacts{Pod: pod})
		if f == nil || !strings.Contains(f.Suggestion, "dmesg") {
			t.Errorf("无佐证应走 default 建议（dmesg）：%+v", f)
		}
	})

	t.Run("多容器仅后者命中", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{
			{Name: "ok", Ready: true},
			{Name: "bad", LastTerminatedReason: "OOMKilled", LastTerminatedCode: 137},
		}}
		f := d.Detect(PodFacts{Pod: pod})
		if f == nil || !strings.Contains(f.RootCause, "bad") {
			t.Errorf("应命中第二个容器 bad：%+v", f)
		}
	})

	t.Run("init容器OOM命中", func(t *testing.T) {
		// init 容器也查（containers() 合并 init+常规）。
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running",
			InitContainers: []ContainerInfo{{Name: "init", LastTerminatedReason: "OOMKilled", LastTerminatedCode: 137}}}
		f := d.Detect(PodFacts{Pod: pod})
		if f == nil || !strings.Contains(f.RootCause, "init") {
			t.Errorf("init 容器 OOM 应命中：%+v", f)
		}
	})

	t.Run("正常退出码0不命中", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Succeeded", Containers: []ContainerInfo{{
			Name: "c", TerminatedCode: 0, TerminatedReason: "Completed",
		}}}
		if f := d.Detect(PodFacts{Pod: pod}); f != nil {
			t.Errorf("正常退出不应命中 OOM：%+v", f)
		}
	})
}

func TestDetectorEdgeCases(t *testing.T) {
	t.Run("Evicted-非Evicted的Failed不命中", func(t *testing.T) {
		for _, reason := range []string{"Completed", "Error", "DeadlineExceeded"} {
			pod := PodInfo{Namespace: "s", Name: "p", Phase: "Failed", Reason: reason}
			if f := (evictedDetector{}).Detect(PodFacts{Pod: pod}); f != nil {
				t.Errorf("Failed+Reason=%q 不应命中 Evicted", reason)
			}
		}
	})

	t.Run("Evicted-无节点压力为Medium", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Failed", Reason: "Evicted"}
		f := (evictedDetector{}).Detect(PodFacts{Pod: pod})
		if f == nil || f.Confidence != ConfidenceMedium {
			t.Errorf("无节点压力的 Evicted 应 Medium：%+v", f)
		}
	})

	t.Run("ImagePull-ErrImagePull也命中", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Pending", Containers: []ContainerInfo{{Name: "c", WaitingReason: "ErrImagePull"}}}
		if f := (imagePullDetector{}).Detect(PodFacts{Pod: pod}); f == nil {
			t.Error("ErrImagePull 应命中")
		}
	})

	t.Run("Pending-condition为空不命中", func(t *testing.T) {
		// Phase=Pending 但无 PodScheduled=False（如刚创建尚未调度评估）。
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Pending"}
		if f := (pendingUnschedulableDetector{}).Detect(PodFacts{Pod: pod}); f != nil {
			t.Errorf("Pending 但无 Unschedulable condition 不应命中：%+v", f)
		}
	})

	t.Run("Pending-Reason空时兜底Unschedulable", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Pending", ScheduledConditionStatus: "False"}
		f := (pendingUnschedulableDetector{}).Detect(PodFacts{Pod: pod})
		if f == nil || !strings.Contains(f.RootCause, "Unschedulable") {
			t.Errorf("空 Reason 应兜底 Unschedulable：%+v", f)
		}
	})

	t.Run("ProbeFailing-非Running不命中", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Pending", Containers: []ContainerInfo{{Name: "c", Ready: false}}}
		events := []EventInfo{{InvolvedKind: "Pod", InvolvedName: "p", Namespace: "s", Reason: "Unhealthy", Message: "Readiness probe failed"}}
		if f := (probeFailingDetector{}).Detect(PodFacts{Pod: pod, Events: events}); f != nil {
			t.Errorf("非 Running 不应命中探针失败：%+v", f)
		}
	})

	t.Run("ProbeFailing-Unhealthy但非probe不命中", func(t *testing.T) {
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{{Name: "c", Ready: false}}}
		events := []EventInfo{{InvolvedKind: "Pod", InvolvedName: "p", Namespace: "s", Reason: "Unhealthy", Message: "some other reason"}}
		if f := (probeFailingDetector{}).Detect(PodFacts{Pod: pod, Events: events}); f != nil {
			t.Errorf("Unhealthy 但 message 不含 probe 不应命中：%+v", f)
		}
	})

	t.Run("RestartLoop-阈值边界", func(t *testing.T) {
		d := restartLoopDetector{threshold: 3}
		mk := func(n int) PodFacts {
			return PodFacts{Pod: PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{{Name: "c", RestartCount: n}}}}
		}
		if f := d.Detect(mk(3)); f == nil {
			t.Error("RestartCount==threshold(3) 应命中")
		}
		if f := d.Detect(mk(2)); f != nil {
			t.Error("RestartCount=2 低于阈值不应命中")
		}
		// 有 waiting（CrashLoopBackOff）由 CRASH_LOOP 管，RESTART_LOOP 不重复。
		crashy := PodFacts{Pod: PodInfo{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{{Name: "c", RestartCount: 5, WaitingReason: "CrashLoopBackOff"}}}}
		if f := d.Detect(crashy); f != nil {
			t.Error("CrashLoopBackOff 态不应由 RESTART_LOOP 重复报")
		}
	})

	t.Run("ContainerStartError-各waiting族", func(t *testing.T) {
		d := containerStartErrorDetector{}
		for _, reason := range []string{"RunContainerError", "CreateContainerError", "StartError"} {
			pod := PodInfo{Namespace: "s", Name: "p", Phase: "Pending", Containers: []ContainerInfo{{Name: "c", WaitingReason: reason}}}
			if f := d.Detect(PodFacts{Pod: pod}); f == nil {
				t.Errorf("WaitingReason=%q 应命中", reason)
			}
		}
		// 非启动错误族不命中
		pod := PodInfo{Namespace: "s", Name: "p", Phase: "Pending", Containers: []ContainerInfo{{Name: "c", WaitingReason: "ContainerCreating"}}}
		if f := d.Detect(PodFacts{Pod: pod}); f != nil {
			t.Errorf("ContainerCreating 不应命中启动错误：%+v", f)
		}
	})
}

// ---- Policy 边缘 ----

func TestPolicyEdgeCases(t *testing.T) {
	t.Run("nsMatch语义", func(t *testing.T) {
		if !nsMatch(nil, "any") {
			t.Error("空 namespaces 应全匹配")
		}
		if !nsMatch([]string{"*"}, "any") {
			t.Error("* 应全匹配")
		}
		if !nsMatch([]string{"prod", "shop"}, "shop") {
			t.Error("精确匹配应命中")
		}
		if nsMatch([]string{"prod"}, "dev") {
			t.Error("不匹配应 false")
		}
	})

	t.Run("imageTag带端口镜像", func(t *testing.T) {
		// registry:5000/app:v1 —— LastIndex(":") 在端口处，需正确取 tag。
		if got := imageTag("registry.local:5000/app:v1.2.3"); got != "v1.2.3" {
			t.Errorf("带端口镜像 tag=%q 期望 v1.2.3", got)
		}
		if got := imageTag("registry.local:5000/app"); got != "latest" {
			t.Errorf("带端口无 tag 应为 latest，got %q", got)
		}
		if got := imageTag("app"); got != "latest" {
			t.Errorf("无 tag 应为 latest，got %q", got)
		}
		if got := imageTag("app:1.0"); got != "1.0" {
			t.Errorf("app:1.0 tag=%q 期望 1.0", got)
		}
	})

	t.Run("未知path返回零值不命中exists", func(t *testing.T) {
		// fieldValue 对未知 path 返回 zero（present=false）→ exists 违反（视为缺）。
		c := WorkloadContainer{Name: "c"}
		a := PolicyAssert{Path: "spec.unknown.path", Op: OpExists}
		if !assertViolated(a, c) {
			t.Error("未知 path 的 exists 应判定违反（present=false）")
		}
	})

	t.Run("globMatch各模式", func(t *testing.T) {
		cases := []struct {
			pattern, s string
			want       bool
		}{
			{"*", "anything", true},
			{"prod-*", "prod-a", true},
			{"*-system", "kube-system", true},
			{"*prod*", "my-prod-ns", true},
			{"exact", "exact", true},
			{"exact", "other", false},
			{"prod-*", "dev-a", false},
		}
		for _, c := range cases {
			if got := globMatch(c.pattern, c.s); got != c.want {
				t.Errorf("globMatch(%q,%q)=%v 期望 %v", c.pattern, c.s, got, c.want)
			}
		}
	})
}

// ---- Engine 边缘 ----

func TestEngineEdgeCases(t *testing.T) {
	t.Run("空Snapshot", func(t *testing.T) {
		eng := NewEngine(nil, 0, fixedNow)
		res := eng.Scan(Snapshot{}, nil)
		if len(res.Findings) != 0 || res.ScannedPods != 0 {
			t.Errorf("空 Snapshot 应 0 finding：%+v", res)
		}
	})

	t.Run("全nil字段不panic", func(t *testing.T) {
		eng := NewEngine(nil, 0, fixedNow)
		res := eng.Scan(Snapshot{Pods: []PodInfo{{}}}, nil) // 全零值 Pod
		// 不 panic 即可；零值 Pod（无 phase/无容器）不应命中任何 detector。
		if len(res.Findings) != 0 {
			t.Errorf("零值 Pod 不应命中：%+v", res.Findings)
		}
	})

	t.Run("事件InvolvedKind非Pod被跳过", func(t *testing.T) {
		snap := Snapshot{
			Pods:   []PodInfo{{Namespace: "s", Name: "p", Phase: "Running", Containers: []ContainerInfo{{Name: "c", Ready: false}}}},
			Events: []EventInfo{{InvolvedKind: "Node", InvolvedName: "n1", Reason: "Unhealthy", Message: "probe failed"}},
		}
		// 事件是 Node 的，不该归到 Pod → 探针失败不命中（事件佐证不匹配）。
		eng := NewEngine(nil, 0, fixedNow)
		res := eng.Scan(snap, nil)
		for _, f := range res.Findings {
			if f.RuleID == "PROBE_FAILING" {
				t.Errorf("Node 事件不应佐证 Pod 探针失败")
			}
		}
	})
}

// ---- pmap 边缘 ----

func TestPmapEdgeCases(t *testing.T) {
	t.Run("workers超过输入长度", func(t *testing.T) {
		out := pmap(100, []int{1, 2, 3}, func(v int) []int { return []int{v} })
		if len(out) != 3 {
			t.Fatalf("槽位数=%d 期望 3", len(out))
		}
		for i, s := range out {
			if len(s) != 1 || s[0] != i+1 {
				t.Errorf("槽位 %d 错位：%v", i, s)
			}
		}
	})

	t.Run("单元素", func(t *testing.T) {
		out := pmap(8, []int{42}, func(v int) []int { return []int{v * 2} })
		if len(out) != 1 || out[0][0] != 84 {
			t.Errorf("单元素错误：%v", out)
		}
	})

	t.Run("fn返回空slice", func(t *testing.T) {
		out := pmap(4, []int{1, 2, 3}, func(int) []int { return nil })
		if len(out) != 3 {
			t.Fatalf("槽位数=%d 期望 3", len(out))
		}
		for i, s := range out {
			if len(s) != 0 {
				t.Errorf("槽位 %d 应为空：%v", i, s)
			}
		}
	})

	t.Run("workers为负数钳到默认", func(t *testing.T) {
		out := pmap(-5, []int{1, 2}, func(v int) []int { return []int{v} })
		if len(out) != 2 {
			t.Errorf("负 workers 应钳到默认仍正确：%v", out)
		}
	})
}

// ---- clip 脱敏截断边缘 ----

func TestClipEdgeCases(t *testing.T) {
	if got := clip(""); got != "" {
		t.Errorf("空串应不变，got %q", got)
	}
	exact := strings.Repeat("a", evidenceMaxLen)
	if got := clip(exact); got != exact {
		t.Errorf("正好 %d 字符应不截断", evidenceMaxLen)
	}
	over := strings.Repeat("b", evidenceMaxLen+50)
	got := clip(over)
	if len(got) > evidenceMaxLen+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("超长应截断+省略号，len=%d", len(got))
	}
	if got := clip("line1\nline2"); strings.Contains(got, "\n") {
		t.Errorf("多行应单行化，got %q", got)
	}
}

// ---- 报告边缘 ----

func TestReportEdgeCases(t *testing.T) {
	empty := Result{GeneratedAt: fixedNow()}

	t.Run("空findings的三种格式不panic", func(t *testing.T) {
		var buf strings.Builder
		if err := WriteJSON(&buf, empty); err != nil || !strings.Contains(buf.String(), `"total_findings": 0`) {
			t.Errorf("空 JSON 报告错误：%v", err)
		}
		buf.Reset()
		if err := WriteSARIF(&buf, empty); err != nil {
			t.Errorf("空 SARIF 错误：%v", err)
		}
		buf.Reset()
		if err := WriteText(&buf, empty); err != nil {
			t.Errorf("空 text 错误：%v", err)
		}
	})

	t.Run("k8sURI集群级对象无namespace", func(t *testing.T) {
		if got := k8sURI(ObjRef{Kind: "Node", Name: "n1"}); got != "k8s://Node/n1" {
			t.Errorf("集群级对象 URI 错误：%q", got)
		}
		if got := k8sURI(ObjRef{Kind: "Pod", Namespace: "s", Name: "p"}); got != "k8s://s/Pod/p" {
			t.Errorf("带 ns 对象 URI 错误：%q", got)
		}
	})
}

// ---- Service 装配边缘 ----

func TestServiceEdgeCases(t *testing.T) {
	t.Run("provider为nil返回错误", func(t *testing.T) {
		svc := NewService(nil, nil, nil, 0, fixedNow)
		if _, err := svc.Scan(t.Context(), CollectOptions{}); err == nil {
			t.Error("provider 未装配应报错")
		}
	})

	t.Run("policies为nil仅A层", func(t *testing.T) {
		snap := Snapshot{Deployments: []DeploymentInfo{{Namespace: "s", Name: "d", Containers: []WorkloadContainer{{Name: "c"}}}}}
		svc := NewService(fakeProvider{snap: snap}, nil, nil, 0, fixedNow)
		res, err := svc.Scan(t.Context(), CollectOptions{})
		if err != nil {
			t.Fatalf("无 B 层来源不应报错：%v", err)
		}
		// 无 policy 来源 → 无 B 层 finding（本快照也无 A 层命中）。
		for _, f := range res.Findings {
			if strings.HasPrefix(f.RuleID, "policy/") {
				t.Errorf("policies=nil 不应有 B 层 finding：%+v", f)
			}
		}
	})
}
