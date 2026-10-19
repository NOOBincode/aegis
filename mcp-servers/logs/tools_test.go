package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	k8sadapter "github.com/aegis-dev/aegis/internal/adapters/k8s"
	"github.com/aegis-dev/aegis/internal/mcpserver"
)

type fakeLogs struct {
	pods  []corev1.Pod
	lines map[string][]k8sadapter.LogLine
}

func (f *fakeLogs) ListPods(context.Context, []string, string, int) ([]corev1.Pod, error) {
	return f.pods, nil
}
func (f *fakeLogs) PodLogs(_ context.Context, ns, name string, _, _, _ int64) ([]k8sadapter.LogLine, bool, error) {
	key := ns + "/" + name
	if l, ok := f.lines[key]; ok {
		return l, false, nil
	}
	return nil, false, fmt.Errorf("boom")
}

func pod(name string) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}}}
}

func TestAggregateLogsSortAndGrep(t *testing.T) {
	t0 := time.Unix(100, 0)
	t1 := time.Unix(101, 0)
	p := &fakeLogs{
		pods: []corev1.Pod{pod("p1"), pod("p2")},
		lines: map[string][]k8sadapter.LogLine{
			"ns/p1": {{Timestamp: &t1, Content: "late target"}, {Timestamp: nil, Content: "no-time"}},
			"ns/p2": {{Timestamp: &t0, Content: "early target"}},
		},
	}
	tool := aggregateLogsTool(p)
	got := tool.Handler(context.Background(), map[string]any{
		"namespaces": []any{"ns"}, "label_selector": "app=x", "grep_hint": "target",
	})
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	data, ok := got.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type=%T", got.Data)
	}
	entries, ok := data["entries"].([]map[string]any)
	if !ok {
		t.Fatalf("entries type=%T", data["entries"])
	}
	if len(entries) != 2 || entries[0]["content"] != "early target" || entries[1]["content"] != "late target" {
		t.Fatalf("entries=%+v", entries)
	}
	if data["pods_reached"] != 2 || data["truncated"] != false {
		t.Fatalf("data=%+v", data)
	}
}

func TestAggregateLogsTooManyPods(t *testing.T) {
	var pods []corev1.Pod
	for i := 0; i < 21; i++ {
		pods = append(pods, pod(fmt.Sprintf("p%02d", i)))
	}
	tool := aggregateLogsTool(&fakeLogs{pods: pods})
	got := tool.Handler(context.Background(), map[string]any{"namespaces": []any{"ns"}, "label_selector": "app=x"})
	if got.Err == nil || got.Err.Class != mcpserver.ErrClassSchema || !strings.Contains(got.Err.Message, "超过") {
		t.Fatalf("err=%+v", got.Err)
	}
}

func TestAggregateLogsUnreachableIsPartial(t *testing.T) {
	p := &fakeLogs{
		pods:  []corev1.Pod{pod("ok"), pod("bad")},
		lines: map[string][]k8sadapter.LogLine{"ns/ok": {{Content: "normal"}}},
	}
	tool := aggregateLogsTool(p)
	got := tool.Handler(context.Background(), map[string]any{"namespaces": []any{"ns"}, "label_selector": "app=x"})
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	data, ok := got.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type=%T", got.Data)
	}
	unreachable, ok := data["unreachable"].([]string)
	if !ok {
		t.Fatalf("unreachable type=%T", data["unreachable"])
	}
	if data["pods_reached"] != 1 || len(unreachable) != 1 {
		t.Fatalf("data=%+v", data)
	}
}
