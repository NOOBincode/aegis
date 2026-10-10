// Package reader 是 k8s-read 的只读集群访问层（M1 形态：工具层无闸门，client-go 只读直连）。
//
// 纪律（mcp-servers/k8s-read/README.md §2 / 设计文档 §4.1）：
//   - 强制分页与上限：单次 ≤200 对象、≤500 行日志；超限返回上限错误（上层转 429 + 分页引导，F5）。
//   - 只读不写：本包无任何写操作。
//   - 一切返回数据视为不可信（包裹在框架 sanitize 中间件，本包不重复包裹）。
//
// M1 形态说明：M1 尚无 L2 闸门，读工具经本包只读直连集群；M2 收敛时工具代码不变，由框架中间件承接路由。
package reader

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	v1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// 硬上限（设计文档 §4.1）。
const (
	MaxObjects  = 200 // 单次列举对象上限
	MaxLogLines = 500 // 单次日志行数上限
)

// Reader 聚合只读客户端。
type Reader struct {
	cs      kubernetes.Interface
	metrics metricsv.Interface
}

// NewInCluster 进程在集群内运行时用（ServiceAccount，R0 只读）。
func NewInCluster() (*Reader, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	return NewFromConfig(cfg)
}

// NewFromKubeconfig 本地/调试运行用（kubeconfig 路径）。
func NewFromKubeconfig(kubeconfig string) (*Reader, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	return NewFromConfig(cfg)
}

// NewFromConfig 由 rest.Config 构造。
func NewFromConfig(cfg *rest.Config) (*Reader, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	mc, err := metricsv.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metrics clientset: %w", err)
	}
	return &Reader{cs: cs, metrics: mc}, nil
}

// NewFromInterfaces 由接口构造（单测注入 fake client）。
func NewFromInterfaces(cs kubernetes.Interface, mc metricsv.Interface) *Reader {
	return &Reader{cs: cs, metrics: mc}
}

// listOpts 统一构造分页参数：limit 超上限即报错（429 语义由上层转），cursor 透传 continue token。
func listOpts(limit, cursor string) (metav1.ListOptions, error) {
	o := metav1.ListOptions{Continue: cursor}
	if limit == "" {
		o.Limit = MaxObjects
		return o, nil
	}
	var n int
	if _, err := fmt.Sscanf(limit, "%d", &n); err != nil {
		return o, fmt.Errorf("limit 非法（须整数）: %q", limit)
	}
	if n <= 0 || n > MaxObjects {
		return o, fmt.Errorf("limit 超上限：须 1..%d，got %d", MaxObjects, n)
	}
	o.Limit = int64(n)
	return o, nil
}

// Age 计算对象年龄的简短可读形式（导出供 tools 层组装条目字段）。
func Age(t metav1.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t.Time)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ---- 各只读操作见 tools 层调用；此处提供 clientset 访问器 ----

// Pods 列举 Pod（分页）。
func (r *Reader) Pods(ctx context.Context, ns, labelSelector, limit, cursor string) (*corev1.PodList, error) {
	o, err := listOpts(limit, cursor)
	if err != nil {
		return nil, err
	}
	o.LabelSelector = labelSelector
	return r.cs.CoreV1().Pods(ns).List(ctx, o)
}

// Deployments 列举 Deployment（分页）。
func (r *Reader) Deployments(ctx context.Context, ns, labelSelector, limit, cursor string) (*appsv1.DeploymentList, error) {
	o, err := listOpts(limit, cursor)
	if err != nil {
		return nil, err
	}
	o.LabelSelector = labelSelector
	return r.cs.AppsV1().Deployments(ns).List(ctx, o)
}

// Nodes 列举 Node（分页）。
func (r *Reader) Nodes(ctx context.Context, limit, cursor string) (*corev1.NodeList, error) {
	o, err := listOpts(limit, cursor)
	if err != nil {
		return nil, err
	}
	return r.cs.CoreV1().Nodes().List(ctx, o)
}

// Events 列举 Event（分页 + 时间窗）。
func (r *Reader) Events(ctx context.Context, ns, limit, cursor string) (*corev1.EventList, error) {
	o, err := listOpts(limit, cursor)
	if err != nil {
		return nil, err
	}
	return r.cs.CoreV1().Events(ns).List(ctx, o)
}

// PodLogs 读取单 Pod 日志（行数上限 + 容器 + 时间窗）。
func (r *Reader) PodLogs(ctx context.Context, ns, name, container string, tailLines int64, sinceSeconds int64) (string, error) {
	if tailLines <= 0 || tailLines > MaxLogLines {
		return "", fmt.Errorf("tail_lines 超上限：须 1..%d，got %d", MaxLogLines, tailLines)
	}
	opt := &corev1.PodLogOptions{TailLines: &tailLines}
	if container != "" {
		opt.Container = container
	}
	if sinceSeconds > 0 {
		opt.SinceSeconds = &sinceSeconds
	}
	stream, err := r.cs.CoreV1().Pods(ns).GetLogs(name, opt).Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := stream.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return string(buf), nil
}

// Pod 取单 Pod（describe 用）。
func (r *Reader) Pod(ctx context.Context, ns, name string) (*corev1.Pod, error) {
	return r.cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
}

// Deployment 取单 Deployment（describe 用）。
func (r *Reader) Deployment(ctx context.Context, ns, name string) (*appsv1.Deployment, error) {
	return r.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
}

// PodMetrics 列举 Pod 资源指标（metrics.k8s.io，分页）。
func (r *Reader) PodMetrics(ctx context.Context, ns, limit, cursor string) (*v1beta1.PodMetricsList, error) {
	o, err := listOpts(limit, cursor)
	if err != nil {
		return nil, err
	}
	return r.metrics.MetricsV1beta1().PodMetricses(ns).List(ctx, o)
}

// NodeMetrics 列举 Node 资源指标（metrics.k8s.io，分页）。
func (r *Reader) NodeMetrics(ctx context.Context, limit, cursor string) (*v1beta1.NodeMetricsList, error) {
	o, err := listOpts(limit, cursor)
	if err != nil {
		return nil, err
	}
	return r.metrics.MetricsV1beta1().NodeMetricses().List(ctx, o)
}
