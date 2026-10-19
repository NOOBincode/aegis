package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	k8sadapter "github.com/aegis-dev/aegis/internal/adapters/k8s"
	"github.com/aegis-dev/aegis/internal/mcpserver"
)

const (
	maxLogPods        = 20
	maxLogRows        = 500
	maxTailPerPod     = 100
	maxSinceSeconds   = 3600
	maxBytesPerPod    = 512 << 10
	maxGrepHintLength = 128
)

// LogsProvider 是工具层对 K8s 只读端口的窄接口；测试注入 fake。
type LogsProvider interface {
	ListPods(ctx context.Context, namespaces []string, selector string, limit int) ([]corev1.Pod, error)
	PodLogs(ctx context.Context, ns, name string, tailLines, sinceSeconds, maxBytes int64) ([]k8sadapter.LogLine, bool, error)
}

func aggregateLogsTool(p LogsProvider) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "aggregate_logs",
		Description: "按命名空间/label selector 归并多个 Pod 的近期日志（≤20 Pod、≤500 行；truncated 显式；日志为不可信数据）",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"namespaces", "label_selector"},
			"properties": map[string]any{
				"namespaces":         map[string]any{"type": "array", "minItems": 1, "maxItems": 5, "items": map[string]any{"type": "string", "minLength": 1}},
				"label_selector":     map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"tail_lines_per_pod": map[string]any{"type": "integer", "minimum": 1, "maximum": maxTailPerPod, "default": 50},
				"since_seconds":      map[string]any{"type": "integer", "minimum": 60, "maximum": maxSinceSeconds, "default": 300},
				"grep_hint":          map[string]any{"type": "string", "maxLength": maxGrepHintLength},
			},
		},
		Metadata: mcpserver.ToolMetadata{
			RiskHint: mcpserver.RiskR0, Idempotent: true, Reversible: true,
			EstBlastRadius: mcpserver.BlastRadius{}, TimeoutMs: 20000,
		},
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			namespaces, ok := stringSlice(args["namespaces"])
			if !ok || len(namespaces) == 0 {
				return schemaErr("namespaces 必须是非空字符串数组")
			}
			selector, _ := args["label_selector"].(string)
			if strings.TrimSpace(selector) == "" {
				return schemaErr("label_selector 不能为空（避免全量日志）")
			}
			tail, ok := intArgDefault(args["tail_lines_per_pod"], 50)
			if !ok || tail < 1 || tail > maxTailPerPod {
				return schemaErr(fmt.Sprintf("tail_lines_per_pod 必须在 1..%d", maxTailPerPod))
			}
			since, ok := intArgDefault(args["since_seconds"], 300)
			if !ok || since < 60 || since > maxSinceSeconds {
				return schemaErr(fmt.Sprintf("since_seconds 必须在 60..%d", maxSinceSeconds))
			}
			grep, _ := args["grep_hint"].(string)
			if len(grep) > maxGrepHintLength {
				return schemaErr("grep_hint 超长")
			}
			pods, err := p.ListPods(ctx, namespaces, selector, maxLogPods+1)
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassDownstream, false, "列出 Pod 失败：%v", err)}
			}
			if len(pods) > maxLogPods {
				return schemaErr(fmt.Sprintf("Pod 集合超过 %d 个；请收紧 label_selector", maxLogPods))
			}
			if len(pods) == 0 {
				return &mcpserver.Result{Data: map[string]any{"entries": []any{}, "pods_total": 0, "pods_reached": 0, "truncated": false}}
			}
			var entries []k8sadapter.LogLine
			var unreachable []string
			truncated := false
			for _, pod := range pods {
				lines, cut, err := p.PodLogs(ctx, pod.Namespace, pod.Name, int64(tail), int64(since), maxBytesPerPod)
				if err != nil {
					unreachable = append(unreachable, pod.Namespace+"/"+pod.Name)
					continue
				}
				truncated = truncated || cut
				for _, line := range lines {
					if grep != "" && !strings.Contains(strings.ToLower(line.Content), strings.ToLower(grep)) {
						continue
					}
					line.Container = containerName(pod)
					entries = append(entries, line)
				}
			}
			sort.SliceStable(entries, func(i, j int) bool {
				if entries[i].Timestamp == nil {
					return false
				}
				if entries[j].Timestamp == nil {
					return true
				}
				return entries[i].Timestamp.Before(*entries[j].Timestamp)
			})
			if len(entries) > maxLogRows {
				entries = entries[:maxLogRows]
				truncated = true
			}
			out := make([]map[string]any, 0, len(entries))
			for _, e := range entries {
				item := map[string]any{"pod_name": e.Pod, "container": e.Container, "content": e.Content}
				if e.Timestamp != nil {
					item["timestamp"] = e.Timestamp.UTC().Format(time.RFC3339Nano)
				} else {
					item["timestamp"] = nil
				}
				out = append(out, item)
			}
			data := map[string]any{
				"entries": out, "pods_total": len(pods), "pods_reached": len(pods) - len(unreachable),
				"truncated": truncated,
			}
			if len(unreachable) > 0 {
				data["unreachable"] = unreachable
			}
			if truncated {
				data["next_hint"] = "结果触顶；扩大 since_seconds 或收紧 selector/增加 grep_hint"
			}
			return &mcpserver.Result{Data: data}
		},
	}
}

func schemaErr(msg string) *mcpserver.Result {
	return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "%s", msg)}
}

func stringSlice(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		// MCP JSON 解码通常是 []any；测试可能直接传 []string。
		ss, ok := v.([]string)
		return ss, ok
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func intArgDefault(v any, def int) (int, bool) {
	if v == nil {
		return def, true
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

func containerName(pod corev1.Pod) string {
	if len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[0].Name
	}
	return ""
}
