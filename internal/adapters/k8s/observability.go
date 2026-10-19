package k8s

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// LogLine 是聚合日志的归一化行；Timestamp 为空表示行首无合法时间戳（仍保留原文）。
type LogLine struct {
	Timestamp *time.Time
	Pod       string
	Container string
	Content   string
}

// ObsEvent 是事件时间线的对齐格式（mcp-servers/events README §4.2 契约）。
type ObsEvent struct {
	Timestamp       time.Time
	Type            string
	Reason          string
	InvolvedKind    string
	InvolvedName    string
	InvolvedNS      string
	SourceComponent string
	Message         string
	Count           int32
	FirstSeen       time.Time
	LastSeen        time.Time
}

// ObservabilityProvider 是 logs/events 两个 MCP server 的只读 K8s 端口实现。
// 只做 list pods / pods.log / list events；不暴露任何写或 Secret 读取能力。
type ObservabilityProvider struct {
	cs kubernetes.Interface
}

// NewObservabilityProvider 构造只读观测数据端口。
func NewObservabilityProvider(cs kubernetes.Interface) *ObservabilityProvider {
	return &ObservabilityProvider{cs: cs}
}

// ListPods 按命名空间/selector 圈定 Pod，最多 limit 个；selector 为空视为全选。
func (p *ObservabilityProvider) ListPods(ctx context.Context, namespaces []string, selector string, limit int) ([]corev1.Pod, error) {
	if limit <= 0 {
		limit = 20
	}
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}
	var out []corev1.Pod
	for _, ns := range namespaces {
		list, err := p.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: int64(limit)})
		if err != nil {
			return nil, fmt.Errorf("list pods ns=%q: %w", ns, err)
		}
		out = append(out, list.Items...)
		if len(out) >= limit {
			return out[:limit], nil
		}
	}
	return out, nil
}

// PodLogs 拉取单 Pod 最近 tailLines 行；返回行、是否因字节上限截断、错误。
// Timestamps=true，行首时间戳解析成功则从 content 中剥离；解析失败保留原文、Timestamp=nil。
func (p *ObservabilityProvider) PodLogs(ctx context.Context, ns, name string, tailLines, sinceSeconds, maxBytes int64) ([]LogLine, bool, error) {
	if maxBytes <= 0 {
		maxBytes = 512 << 10
	}
	tail := tailLines
	opts := &corev1.PodLogOptions{TailLines: &tail, Timestamps: true}
	if sinceSeconds > 0 {
		opts.SinceSeconds = &sinceSeconds
	}
	stream, err := p.cs.CoreV1().Pods(ns).GetLogs(name, opts).Stream(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("get logs %s/%s: %w", ns, name, err)
	}
	defer func() { _ = stream.Close() }()
	sc := bufio.NewScanner(io.LimitReader(stream, maxBytes+1))
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	var lines []LogLine
	var read int64
	truncated := false
	for sc.Scan() {
		raw := sc.Bytes()
		read += int64(len(raw)) + 1
		if read > maxBytes {
			truncated = true
			break
		}
		line := string(raw)
		ts, content := splitTimestamp(line)
		lines = append(lines, LogLine{Timestamp: ts, Pod: name, Container: "", Content: content})
	}
	if err := sc.Err(); err != nil && !truncated {
		return nil, false, fmt.Errorf("scan logs %s/%s: %w", ns, name, err)
	}
	return lines, truncated, nil
}

// ListEvents 拉取多命名空间事件，按时间升序，最多 limit 条；since 为零不过滤。
func (p *ObservabilityProvider) ListEvents(ctx context.Context, namespaces []string, since time.Time, limit int) ([]ObsEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}
	var out []ObsEvent
	for _, ns := range namespaces {
		list, err := p.cs.CoreV1().Events(ns).List(ctx, metav1.ListOptions{Limit: int64(limit)})
		if err != nil {
			return nil, fmt.Errorf("list events ns=%q: %w", ns, err)
		}
		for i := range list.Items {
			e := &list.Items[i]
			ts := eventTime(e)
			if !since.IsZero() && ts.Before(since) {
				continue
			}
			out = append(out, ObsEvent{
				Timestamp: ts, Type: e.Type, Reason: e.Reason,
				InvolvedKind: e.InvolvedObject.Kind, InvolvedName: e.InvolvedObject.Name,
				InvolvedNS: e.InvolvedObject.Namespace, SourceComponent: e.Source.Component,
				Message: e.Message, Count: e.Count,
				FirstSeen: e.FirstTimestamp.Time, LastSeen: e.LastTimestamp.Time,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func eventTime(e *corev1.Event) time.Time {
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.FirstTimestamp.IsZero() {
		return e.FirstTimestamp.Time
	}
	return e.CreationTimestamp.Time
}

// splitTimestamp 解析 kubelet 日志行首的 RFC3339Nano 时间戳。
func splitTimestamp(line string) (*time.Time, string) {
	i := strings.IndexByte(line, ' ')
	if i <= 0 {
		return nil, line
	}
	head := line[:i]
	t, err := time.Parse(time.RFC3339Nano, head)
	if err != nil {
		return nil, line
	}
	return &t, line[i+1:]
}
