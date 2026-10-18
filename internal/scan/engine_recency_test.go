package scan

import (
	"strings"
	"testing"
	"time"
)

// 陈旧 SystemOOM 事件不得参与当前 137 推断，否则会把容器 OOM 误归因为节点级 OOM。
func TestEngineIgnoresStaleSystemOOM(t *testing.T) {
	mk := func(lastSeen time.Time) Snapshot {
		return Snapshot{
			Pods: []PodInfo{{
				Namespace: "s", Name: "p", Phase: "Running", NodeName: "n1",
				Containers: []ContainerInfo{{Name: "c", LastTerminatedCode: 137}},
			}},
			Events: []EventInfo{{
				InvolvedKind: "Node", InvolvedName: "n1", Reason: "SystemOOM", LastSeen: lastSeen,
			}},
		}
	}

	t.Run("近期事件参与佐证", func(t *testing.T) {
		res := NewEngine(nil, 0, fixedNow).Scan(mk(fixedNow().Add(-time.Minute)), nil)
		if len(res.Findings) != 1 {
			t.Fatalf("应命中 1 条 OOM，got %+v", res.Findings)
		}
		if joined := strings.Join(res.Findings[0].Evidence, " | "); !strings.Contains(joined, "节点级") {
			t.Fatalf("近期 SystemOOM 应作为节点级佐证：%v", res.Findings[0].Evidence)
		}
	})

	t.Run("陈旧事件不参与佐证", func(t *testing.T) {
		res := NewEngine(nil, 0, fixedNow).Scan(mk(fixedNow().Add(-2*eventEvidenceWindow)), nil)
		if len(res.Findings) != 1 {
			t.Fatalf("应命中 1 条 OOM，got %+v", res.Findings)
		}
		if joined := strings.Join(res.Findings[0].Evidence, " | "); strings.Contains(joined, "节点级") {
			t.Fatalf("陈旧 SystemOOM 不应作为节点级佐证：%v", res.Findings[0].Evidence)
		}
	})
}

// filterRecentEvents 保留零时间戳（fake/旧对象），丢弃窗口外事件。
func TestFilterRecentEvents(t *testing.T) {
	now := fixedNow()
	in := []EventInfo{
		{Reason: "old", LastSeen: now.Add(-2 * eventEvidenceWindow)},
		{Reason: "new", LastSeen: now.Add(-time.Second)},
		{Reason: "zero"},
	}
	got := filterRecentEvents(in, now)
	if len(got) != 2 || got[0].Reason != "new" || got[1].Reason != "zero" {
		t.Fatalf("过滤结果不符：%+v", got)
	}
}
