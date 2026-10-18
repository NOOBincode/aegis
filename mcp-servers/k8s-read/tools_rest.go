package main

// k8s-read 只读工具（续 tools.go）：get_deployments / get_nodes / get_events / get_pod_logs /
// get_resource_metrics / describe_pod / describe_deployment。

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aegis-dev/aegis/internal/mcpserver"
	"github.com/aegis-dev/aegis/mcp-servers/k8s-read/reader"
)

func getDeployments(r *reader.Reader) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "get_deployments",
		Description: "列举某命名空间下的 Deployment（只读，分页）。返回 name/replicas(期望/可用/更新中)/age。",
		InputSchema: nsObjSchema(),
		Metadata:    roMeta(8000),
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			ns, perr := requireStr(args, "ns")
			if perr != nil {
				return &mcpserver.Result{Err: perr}
			}
			list, err := r.Deployments(ctx, ns, argStr(args, "label_selector"), argLimit(args), argStr(args, "cursor"))
			if err != nil {
				return &mcpserver.Result{Err: toolError(err, "列举 deployments 失败")}
			}
			items := make([]map[string]any, 0, len(list.Items))
			for _, d := range list.Items {
				items = append(items, map[string]any{
					"name": d.Name,
					"replicas": map[string]any{
						"desired":   d.Spec.Replicas,
						"available": d.Status.AvailableReplicas,
						"updated":   d.Status.UpdatedReplicas,
					},
					"age": reader.Age(d.CreationTimestamp),
				})
			}
			res, _ := listData(items, list.Continue)
			return res
		},
	}
}

func getNodes(r *reader.Reader) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "get_nodes",
		Description: "列举集群节点（只读，分页）。返回 name/ready/taints 数/age。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit":  map[string]any{"type": "integer", "description": "返回上限，≤200"},
				"cursor": map[string]any{"type": "string", "description": "分页游标"},
			},
		},
		Metadata: roMeta(8000),
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			list, err := r.Nodes(ctx, argLimit(args), argStr(args, "cursor"))
			if err != nil {
				return &mcpserver.Result{Err: toolError(err, "列举 nodes 失败")}
			}
			items := make([]map[string]any, 0, len(list.Items))
			for _, n := range list.Items {
				ready := "False"
				for _, c := range n.Status.Conditions {
					if c.Type == "Ready" {
						ready = string(c.Status)
					}
				}
				items = append(items, map[string]any{
					"name":         n.Name,
					"ready":        ready,
					"taints_count": len(n.Spec.Taints),
					"age":          reader.Age(n.CreationTimestamp),
				})
			}
			res, _ := listData(items, list.Continue)
			return res
		},
	}
}

func getEvents(r *reader.Reader) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "get_events",
		Description: "列举某命名空间的事件（只读，分页 + 时间窗）。返回 reason/type/object/message/age。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"ns"},
			"properties": map[string]any{
				"ns":     map[string]any{"type": "string", "description": "命名空间（必填）"},
				"since":  map[string]any{"type": "string", "description": "时间窗，如 30m/1h；缺省 1h（仅过滤返回页，分页用 cursor）"},
				"limit":  map[string]any{"type": "integer", "description": "返回上限，≤200"},
				"cursor": map[string]any{"type": "string", "description": "分页游标（上一页 truncated 时返回的 next_cursor）"},
			},
		},
		Metadata: roMeta(8000),
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			ns, perr := requireStr(args, "ns")
			if perr != nil {
				return &mcpserver.Result{Err: perr}
			}
			since, err := parseSince(argStr(args, "since"))
			if err != nil {
				return &mcpserver.Result{Err: mcpserver.NewToolError(mcpserver.ErrClassSchema, false, "since 非法：%v", err)}
			}
			list, err := r.Events(ctx, ns, argLimit(args), argStr(args, "cursor"))
			if err != nil {
				return &mcpserver.Result{Err: toolError(err, "列举 events 失败")}
			}
			cutoff := time.Now().Add(-since)
			items := make([]map[string]any, 0, len(list.Items))
			for _, e := range list.Items {
				var last time.Time
				if !e.LastTimestamp.IsZero() {
					last = e.LastTimestamp.Time
				} else if !e.EventTime.IsZero() {
					last = e.EventTime.Time
				}
				if !last.IsZero() && last.Before(cutoff) {
					continue
				}
				items = append(items, map[string]any{
					"reason":  e.Reason,
					"type":    e.Type,
					"object":  fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
					"message": e.Message,
					"age":     reader.Age(metav1.NewTime(last)),
				})
			}
			res, _ := listData(items, list.Continue)
			return res
		},
	}
}

func getPodLogs(r *reader.Reader) mcpserver.Tool {
	return mcpserver.Tool{
		Name:        "get_pod_logs",
		Description: "读取单 Pod 日志（只读，行数上限 ≤500）。返回带行号的内容。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"ns", "name"},
			"properties": map[string]any{
				"ns":            map[string]any{"type": "string", "description": "命名空间（必填）"},
				"name":          map[string]any{"type": "string", "description": "Pod 名（必填）"},
				"container":     map[string]any{"type": "string", "description": "容器名（多容器时必填）"},
				"tail_lines":    map[string]any{"type": "integer", "description": "尾部行数，≤500"},
				"since_seconds": map[string]any{"type": "integer", "description": "仅取最近 N 秒"},
			},
		},
		Metadata: roMeta(10000),
		Handler: func(ctx context.Context, args map[string]any) *mcpserver.Result {
			ns, perr := requireStr(args, "ns")
			if perr != nil {
				return &mcpserver.Result{Err: perr}
			}
			name, perr := requireStr(args, "name")
			if perr != nil {
				return &mcpserver.Result{Err: perr}
			}
			tail := argInt64(args, "tail_lines")
			if tail == 0 {
				tail = 200 // 默认尾部 200 行
			}
			logs, truncated, err := r.PodLogs(ctx, ns, name, argStr(args, "container"), tail, argInt64(args, "since_seconds"))
			if err != nil {
				return &mcpserver.Result{Err: toolError(err, "读 pod 日志失败")}
			}
			items := make([]map[string]any, 0, tail)
			if s := strings.TrimRight(logs, "\n"); s != "" {
				for i, ln := range strings.Split(s, "\n") {
					items = append(items, map[string]any{"line": i + 1, "content": ln})
				}
			}
			res := &mcpserver.Result{Data: map[string]any{
				"items":        items,
				"object_count": len(items),
				"pod":          name,
				"tail_lines":   tail,
				"max_bytes":    reader.MaxLogBytes,
			}}
			if truncated {
				res.Meta.Truncated = true
			}
			return res
		},
	}
}

// defaultEventsSince 是 get_events 未显式传 since 时的默认时间窗。
const defaultEventsSince = time.Hour

// parseSince 解析事件时间窗（如 30m/1h）；空串走默认 1h。
func parseSince(s string) (time.Duration, error) {
	if s == "" {
		return defaultEventsSince, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("since 必须为正时长")
	}
	return d, nil
}
