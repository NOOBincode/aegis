// Package k8s 实现 internal/scan 的 SnapshotProvider 端口（铁律 1：适配器实现上下文端口）。
//
// 本文件是 Tier-0 规则巡检的只读采集器：聚合 Pods/Deployments/Nodes/Events 为
// scan.Snapshot 纯结构，引擎（internal/scan）不感知 client-go（铁律 2 领域纯净）。
// 纪律（§4.1/§4.2.2/F5）：只读 List/Get、强制分页上限、数据最小化（不取 Secret/env/
// 镜像仓库全文）；部分失败（如 RBAC 拒读）记入 PartialReads 盲点，绝不静默当空。
package k8s

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/aegis-dev/aegis/internal/scan"
)

// 采集硬上限（对齐设计文档 §4.1：单次 ≤200 对象）。
const collectMaxObjects = 200

// ScanSnapshotProvider 用 client-go typed clientset 采集 scan.Snapshot。
// 实现 scan.SnapshotProvider；构造期注入 clientset（in-cluster 或 kubeconfig）。
type ScanSnapshotProvider struct {
	cs  kubernetes.Interface
	now func() time.Time // 注入时钟：事件时间窗过滤与可测试性
}

// NewScanSnapshotProvider 由 clientset 构造（单测注入 fake.NewSimpleClientset）。
func NewScanSnapshotProvider(cs kubernetes.Interface) *ScanSnapshotProvider {
	return &ScanSnapshotProvider{cs: cs, now: time.Now}
}

// Collect 并发采集 4 类对象并映射为 Snapshot。
//
// 并发与失效语义：4 类对象有界并行拉取（互不干扰，各写各的结果槽位）；
// 单类失败按类别处置——Forbidden/NotFound 记盲点继续（RBAC 缺口不应当作"无异常"），
// 连接级错误（apiserver 不可达）整体返回 error（取数失败 ≠ 无异常，见 design §6）。
func (p *ScanSnapshotProvider) Collect(ctx context.Context, opts scan.CollectOptions) (scan.Snapshot, error) {
	ns := opts.Namespace // 空 = 全集群（仍受上限约束）

	var (
		wg        sync.WaitGroup
		pods      *corev1.PodList
		deploys   *appsv1.DeploymentList
		nodes     *corev1.NodeList
		events    *corev1.EventList
		podsErr   error
		deployErr error
		nodesErr  error
		eventsErr error
	)
	wg.Add(4)
	go func() { defer wg.Done(); pods, podsErr = p.listPods(ctx, ns) }()
	go func() { defer wg.Done(); deploys, deployErr = p.listDeployments(ctx, ns) }()
	go func() { defer wg.Done(); nodes, nodesErr = p.listNodes(ctx) }()
	go func() { defer wg.Done(); events, eventsErr = p.listEvents(ctx, ns) }()
	wg.Wait()

	var snap scan.Snapshot
	var blind []string
	// 截断必须进盲点：只取第一页（Limit 上限）却静默当"全量扫描"会造成假阴性
	// （OOM Pod 落在第 201 个时报告仍显示无异常）。MVP 不隐式翻页放大 QPS（F5），
	// 但必须让调用方/报告知道覆盖不完整。
	noteTruncation("pods", pods, &blind)
	noteTruncation("deployments", deploys, &blind)
	noteTruncation("nodes", nodes, &blind)
	noteTruncation("events", events, &blind)
	// 逐类归并：硬错误（连接级）返回 error；可降级错误记盲点。
	if err := collectErr("pods", podsErr, &blind); err != nil {
		return snap, err
	}
	if err := collectErr("deployments", deployErr, &blind); err != nil {
		return snap, err
	}
	if err := collectErr("nodes", nodesErr, &blind); err != nil {
		return snap, err
	}
	if err := collectErr("events", eventsErr, &blind); err != nil {
		return snap, err
	}

	if pods != nil {
		for i := range pods.Items {
			snap.Pods = append(snap.Pods, mapPod(&pods.Items[i]))
		}
	}
	if deploys != nil {
		for i := range deploys.Items {
			snap.Deployments = append(snap.Deployments, mapDeployment(&deploys.Items[i]))
		}
	}
	if nodes != nil {
		for i := range nodes.Items {
			snap.Nodes = append(snap.Nodes, mapNode(&nodes.Items[i]))
		}
	}
	if events != nil {
		// 时间窗过滤（SinceSeconds>0）：仅保留近期事件——SystemOOM/探针等佐证不该用陈旧事件。
		var cutoff time.Time
		if opts.SinceSeconds > 0 {
			cutoff = p.now().Add(-time.Duration(opts.SinceSeconds) * time.Second)
		}
		for i := range events.Items {
			ev := mapEvent(&events.Items[i])
			if !cutoff.IsZero() && !ev.LastSeen.IsZero() && ev.LastSeen.Before(cutoff) {
				continue // 陈旧事件，丢弃
			}
			snap.Events = append(snap.Events, ev)
		}
	}
	snap.PartialReads = blind
	return snap, nil
}

// collectErr 把单类采集错误归类：连接级错误返回 error；可降级（Forbidden/NotFound）记盲点。
func collectErr(kind string, err error, blind *[]string) error {
	if err == nil {
		return nil
	}
	if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
		*blind = append(*blind, kind+":"+string(apierrors.ReasonForError(err)))
		return nil
	}
	return fmt.Errorf("采集 %s 失败（连接级，不产出半份误报）: %w", kind, err)
}

// listOpts 分页参数（上限 200）。MVP 单页采集，不隐式翻页放大 QPS（F5）；
// 规则巡检的覆盖广度上限即 collectMaxObjects；触顶时 noteTruncation 写入盲点，
// 报告 executionSuccessful=false，绝不静默当"全量扫描过"。
func listOpts() metav1.ListOptions {
	return metav1.ListOptions{Limit: collectMaxObjects}
}

// noteTruncation 把 List 的 Continue 游标（非空=还有下一页）记为采集盲点。
// 不记录游标值本身（opaque token 无审计价值，且可能很长）。
func noteTruncation(kind string, list any, blind *[]string) {
	type continued interface{ GetContinue() string }
	// typed List 的 ListMeta 不实现方法；用类型开关覆盖四类。
	var cont string
	switch v := list.(type) {
	case *corev1.PodList:
		if v != nil {
			cont = v.Continue
		}
	case *appsv1.DeploymentList:
		if v != nil {
			cont = v.Continue
		}
	case *corev1.NodeList:
		if v != nil {
			cont = v.Continue
		}
	case *corev1.EventList:
		if v != nil {
			cont = v.Continue
		}
	case continued:
		cont = v.GetContinue()
	}
	if cont != "" {
		*blind = append(*blind, kind+":truncated@limit="+strconv.Itoa(collectMaxObjects))
	}
}

// listPods/Deployments/Nodes/Events 各拉一页（上限即上限）。
func (p *ScanSnapshotProvider) listPods(ctx context.Context, ns string) (*corev1.PodList, error) {
	return p.cs.CoreV1().Pods(ns).List(ctx, listOpts())
}
func (p *ScanSnapshotProvider) listDeployments(ctx context.Context, ns string) (*appsv1.DeploymentList, error) {
	return p.cs.AppsV1().Deployments(ns).List(ctx, listOpts())
}
func (p *ScanSnapshotProvider) listNodes(ctx context.Context) (*corev1.NodeList, error) {
	return p.cs.CoreV1().Nodes().List(ctx, listOpts())
}

// listEvents 拉取一页事件（上限即上限；时间窗过滤在 Collect 映射层做，用注入时钟）。
func (p *ScanSnapshotProvider) listEvents(ctx context.Context, ns string) (*corev1.EventList, error) {
	return p.cs.CoreV1().Events(ns).List(ctx, listOpts())
}

// ---- 字段映射（client-go 类型 → scan 纯结构，数据最小化）----

// mapPod 映射 Pod。OOM 信号在 LastTerminationState/ContainerStatus；
// Unschedulable 在 PodScheduled condition；Evicted 在 status.Reason。
func mapPod(pod *corev1.Pod) scan.PodInfo {
	info := scan.PodInfo{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		Phase:     string(pod.Status.Phase),
		Reason:    pod.Status.Reason,
		Message:   pod.Status.Message,
		NodeName:  pod.Spec.NodeName,
	}
	if len(pod.OwnerReferences) > 0 {
		info.OwnerKind = pod.OwnerReferences[0].Kind
		info.OwnerName = pod.OwnerReferences[0].Name
	}
	// PodScheduled condition（Pending 不可调度判定）。
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled {
			info.ScheduledConditionStatus = string(c.Status)
			info.ScheduledConditionReason = c.Reason
			info.ScheduledConditionMessage = c.Message
		}
	}
	// 容器：status（运行态）join spec（资源声明），OOMKilled 常在 LastTerminationState。
	limitsByName := containerLimits(pod.Spec.Containers)
	info.Containers = mapContainers(pod.Status.ContainerStatuses, limitsByName)
	initLimits := containerLimits(pod.Spec.InitContainers)
	info.InitContainers = mapContainers(pod.Status.InitContainerStatuses, initLimits)
	return info
}

// containerLimits 建"容器名 → memory limit bytes"索引（从 spec.resources，status 不携带）。
func containerLimits(containers []corev1.Container) map[string]int64 {
	m := make(map[string]int64, len(containers))
	for _, c := range containers {
		if c.Resources.Limits != nil {
			m[c.Name] = c.Resources.Limits.Memory().Value()
		}
	}
	return m
}

// mapContainers 映射容器状态列表，join 资源声明。
func mapContainers(statuses []corev1.ContainerStatus, limits map[string]int64) []scan.ContainerInfo {
	out := make([]scan.ContainerInfo, 0, len(statuses))
	for _, cs := range statuses {
		ci := scan.ContainerInfo{
			Name:         cs.Name,
			RestartCount: int(cs.RestartCount),
			Ready:        cs.Ready,
		}
		if cs.State.Waiting != nil {
			ci.WaitingReason = cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil {
			ci.TerminatedReason = cs.State.Terminated.Reason
			ci.TerminatedCode = cs.State.Terminated.ExitCode
		}
		if cs.LastTerminationState.Terminated != nil {
			t := cs.LastTerminationState.Terminated
			ci.LastTerminatedReason = t.Reason
			ci.LastTerminatedCode = t.ExitCode
			ci.LastTerminatedAt = t.FinishedAt.Time
		}
		ci.MemoryLimitBytes = limits[cs.Name]
		out = append(out, ci)
	}
	return out
}

// mapDeployment 映射 Deployment（含模板容器的结构 lint 判定面）。
func mapDeployment(d *appsv1.Deployment) scan.DeploymentInfo {
	info := scan.DeploymentInfo{
		Namespace:         d.Namespace,
		Name:              d.Name,
		ReadyReplicas:     d.Status.ReadyReplicas,
		UpdatedReplicas:   d.Status.UpdatedReplicas,
		AvailableReplicas: d.Status.AvailableReplicas,
	}
	if d.Spec.Replicas != nil {
		info.Replicas = *d.Spec.Replicas
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		wc := scan.WorkloadContainer{
			Name:              c.Name,
			Image:             c.Image,
			HasReadinessProbe: c.ReadinessProbe != nil,
			HasLivenessProbe:  c.LivenessProbe != nil,
		}
		if c.Resources.Limits != nil {
			wc.MemoryLimitBytes = c.Resources.Limits.Memory().Value()
			wc.CPULimitMilli = c.Resources.Limits.Cpu().MilliValue()
		}
		if c.SecurityContext != nil {
			if c.SecurityContext.Privileged != nil {
				wc.Privileged = *c.SecurityContext.Privileged
			}
			wc.RunAsNonRoot = c.SecurityContext.RunAsNonRoot
		}
		info.Containers = append(info.Containers, wc)
	}
	return info
}

// mapNode 映射 Node（Ready/Pressure 条件 + cordon + taints）。
func mapNode(n *corev1.Node) scan.NodeInfo {
	info := scan.NodeInfo{Name: n.Name, Unschedulable: n.Spec.Unschedulable}
	for _, c := range n.Status.Conditions {
		switch c.Type {
		case corev1.NodeReady:
			info.Ready = c.Status == corev1.ConditionTrue
		case corev1.NodeMemoryPressure:
			info.MemoryPressure = c.Status == corev1.ConditionTrue
		case corev1.NodeDiskPressure:
			info.DiskPressure = c.Status == corev1.ConditionTrue
		case corev1.NodePIDPressure:
			info.PIDPressure = c.Status == corev1.ConditionTrue
		}
	}
	for _, t := range n.Spec.Taints {
		info.Taints = append(info.Taints, t.Key+":"+string(t.Effect))
	}
	sort.Strings(info.Taints) // 确定性
	return info
}

// mapEvent 映射 Event（Message 为不可信自由文本，由引擎消毒）。
func mapEvent(e *corev1.Event) scan.EventInfo {
	info := scan.EventInfo{
		Namespace:    e.Namespace,
		Reason:       e.Reason,
		Message:      e.Message,
		Type:         e.Type,
		Count:        e.Count,
		InvolvedKind: e.InvolvedObject.Kind,
		InvolvedName: e.InvolvedObject.Name,
	}
	if !e.LastTimestamp.IsZero() {
		info.LastSeen = e.LastTimestamp.Time
	} else if !e.EventTime.IsZero() {
		info.LastSeen = e.EventTime.Time
	}
	return info
}
