package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/mcp-servers/k8s-read/reader"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclient "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

// fakeReader 用 client-go fake clientset 构造只读 reader（单测不触真实集群）。
func fakeReader() *reader.Reader {
	cs := fakeclient.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "default", CreationTimestamp: metav1.Now()},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-b", Namespace: "default"}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "ev-1", Namespace: "default"}, Reason: "OOMKilled", Type: "Warning",
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "pod-a"}},
	)
	mc := metricsfake.NewSimpleClientset()
	// metrics fake 的 tracker 对 PodMetrics/NodeMetrics 的 List 返回空（版本注册坑），
	// 用 PrependReactor 直接供给测试数据（绕过 tracker）。
	// metrics API 的 REST resource 是 pods/nodes（非 podmetricses）。
	mc.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &metricsv1beta1.PodMetricsList{Items: []metricsv1beta1.PodMetrics{
			{ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "default"}},
		}}, nil
	})
	mc.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &metricsv1beta1.NodeMetricsList{Items: []metricsv1beta1.NodeMetrics{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		}}, nil
	})
	return reader.NewFromInterfaces(cs, mc)
}

func registryWithFake(t *testing.T) *mcpserver.Registry {
	t.Helper()
	reg := mcpserver.NewRegistry()
	registerTools(reg, fakeReader())
	if reg.Len() != 8 {
		t.Fatalf("应注册 8 个工具，got %d", reg.Len())
	}
	return reg
}

func invoke(t *testing.T, reg *mcpserver.Registry, name string, args map[string]any) *mcpserver.Result {
	t.Helper()
	return reg.Invoke(context.Background(), name, args)
}

// 8 个工具齐全且元数据合法（注册即强校验，注册失败 MustRegister 已 panic）。
func TestAllEightToolsRegistered(t *testing.T) {
	reg := registryWithFake(t)
	names := map[string]bool{}
	for _, tool := range reg.List() {
		names[tool.Name] = true
	}
	for _, want := range []string{"get_pods", "get_deployments", "get_nodes", "get_events", "get_pod_logs", "get_resource_metrics", "describe_pod", "describe_deployment"} {
		if !names[want] {
			t.Errorf("缺工具 %s", want)
		}
	}
}

// 各读工具的 happy path：返回结构化 items，且被 I4 包裹。
func TestReadToolsHappyPath(t *testing.T) {
	reg := registryWithFake(t)
	cases := []struct {
		tool string
		args map[string]any
		want string // 结果里应含的子串
	}{
		{"get_pods", map[string]any{"ns": "default"}, "pod-a"},
		{"get_deployments", map[string]any{"ns": "default"}, "web"},
		{"get_nodes", map[string]any{}, "node-1"},
		{"get_events", map[string]any{"ns": "default"}, "OOMKilled"},
		{"get_resource_metrics", map[string]any{"kind": "pods", "ns": "default"}, "pod-a"},
		{"get_resource_metrics", map[string]any{"kind": "nodes"}, "node-1"},
		{"describe_pod", map[string]any{"ns": "default", "name": "pod-a"}, "Running"},
		{"describe_deployment", map[string]any{"ns": "default", "name": "web"}, "web"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			res := invoke(t, reg, tc.tool, tc.args)
			if res.Err != nil {
				t.Fatalf("%s 出错: %+v", tc.tool, res.Err)
			}
			// 经 Invoke 后 sanitize 中间件已包裹。
			s, _ := res.Data.(string)
			if !strings.Contains(s, "untrusted_cluster_data") {
				t.Errorf("%s 结果应被 I4 包裹，got %v", tc.tool, res.Data)
			}
			if !strings.Contains(s, tc.want) {
				t.Errorf("%s 结果应含 %q，got %q", tc.tool, tc.want, s)
			}
		})
	}
}

// 必填参数缺失 → schema 错误（不触达集群）。
func TestRequiredArgValidation(t *testing.T) {
	reg := registryWithFake(t)
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"get_pods", map[string]any{}},                              // 缺 ns
		{"get_events", map[string]any{}},                            // 缺 ns
		{"get_pod_logs", map[string]any{"ns": "default"}},           // 缺 name
		{"describe_pod", map[string]any{"ns": "default"}},           // 缺 name
		{"get_resource_metrics", map[string]any{"kind": "pods"}},    // 缺 ns
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			res := invoke(t, reg, tc.tool, tc.args)
			if res.Err == nil || res.Err.Class != mcpserver.ErrClassSchema {
				t.Fatalf("%s 缺必填参数应返回 schema 错误，got %+v", tc.tool, res.Err)
			}
		})
	}
}

// 超限：limit > 200 → 工具错误（429 语义由上层转）。
func TestLimitCap(t *testing.T) {
	reg := registryWithFake(t)
	res := invoke(t, reg, "get_pods", map[string]any{"ns": "default", "limit": 5000})
	if res.Err == nil {
		t.Fatal("limit 超 200 应报错")
	}
	if !strings.Contains(res.Err.Message, "超上限") && !strings.Contains(res.Err.Message, "200") {
		t.Errorf("错误应提示上限，got %q", res.Err.Message)
	}
}

// get_pod_logs 的 tail_lines 上限（≤500）。
func TestPodLogsTailCap(t *testing.T) {
	reg := registryWithFake(t)
	res := invoke(t, reg, "get_pod_logs", map[string]any{"ns": "default", "name": "pod-a", "tail_lines": 9999})
	if res.Err == nil {
		t.Fatal("tail_lines 超 500 应报错")
	}
}
